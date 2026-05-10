package core

import (
	"context"
	"log/slog"
	"time"
)

// MetricFetcher is the port for reading metrics from storage.
type MetricFetcher interface {
	FetchUnanalyzed(ctx context.Context, analyzerID string, limit int) ([]Metric, error)
	MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) (int64, error)
}

// EventProducer is the port for publishing anomaly events to a broker.
type EventProducer interface {
	Produce(ctx context.Context, anomalies []*Anomaly) error
}

// AnalyzerService polls TimescaleDB for unanalyzed metrics, evaluates them, and publishes anomalies.
type AnalyzerService struct {
	reader     MetricFetcher
	engine     *RuleEngine
	producer   EventProducer
	analyzerID string
	batchSize  int
	logger     *slog.Logger
}

// NewAnalyzerService creates an analyzer that polls storage for metrics.
func NewAnalyzerService(
	reader MetricFetcher,
	engine *RuleEngine,
	producer EventProducer,
	analyzerID string,
	batchSize int,
	logger *slog.Logger,
) *AnalyzerService {
	return &AnalyzerService{
		reader:     reader,
		engine:     engine,
		producer:   producer,
		analyzerID: analyzerID,
		batchSize:  batchSize,
		logger:     logger,
	}
}

// Run starts the polling loop. It blocks until the context is cancelled.
func (s *AnalyzerService) Run(ctx context.Context, pollInterval time.Duration) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	s.logger.Info("analyzer polling started", "interval", pollInterval)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("analyzer polling stopped")
			return
		case <-ticker.C:
			s.ProcessMetrics(ctx)
		}
	}
}

// ProcessBatch implements BatchHandler for the HTTP debug endpoint.
func (s *AnalyzerService) ProcessBatch(ctx context.Context, batch Batch) {
	if len(batch.Metrics) == 0 {
		return
	}
	anomalies := s.engine.EvaluateBatch(batch)
	if len(anomalies) == 0 {
		return
	}
	var anomalyIDs []int64
	for _, a := range anomalies {
		anomalyIDs = append(anomalyIDs, a.MetricID)
	}
	if err := s.producer.Produce(ctx, anomalies); err != nil {
		s.logger.Error("failed to publish anomalies via HTTP debug path", "err", err, "count", len(anomalies))
		return
	}
	s.logger.Info("anomalies published via HTTP debug", "anomaly_count", len(anomalies))
	if _, err := s.reader.MarkAnalyzed(ctx, s.analyzerID, anomalyIDs); err != nil {
		s.logger.Warn("failed to mark anomalous metrics via HTTP", "err", err)
	}
}

// ProcessMetrics fetches unanalyzed metrics, evaluates them in batch, and
// publishes anomalies to Kafka. Anomalous metrics are marked only after successful publish.
func (s *AnalyzerService) ProcessMetrics(ctx context.Context) {
	metrics, err := s.reader.FetchUnanalyzed(ctx, s.analyzerID, s.batchSize)
	if err != nil {
		s.logger.Error("failed to fetch unanalyzed metrics", "err", err)
		return
	}

	if len(metrics) == 0 {
		s.logger.Debug("no unanalyzed metrics")
		return
	}

	batch := Batch{Metrics: metrics}
	anomalies := s.engine.EvaluateBatch(batch)

	anomalyMetricIDs := make(map[int64]bool, len(anomalies))
	for _, a := range anomalies {
		anomalyMetricIDs[a.MetricID] = true
	}

	var normalIDs, notifyIDs []int64
	for _, m := range metrics {
		if anomalyMetricIDs[m.ID] {
			notifyIDs = append(notifyIDs, m.ID)
		} else {
			normalIDs = append(normalIDs, m.ID)
		}
	}

	s.logger.Info("metrics processed",
		"total", len(metrics),
		"anomalies", len(anomalies),
		"to_notify", len(notifyIDs),
		"to_mark_normal", len(normalIDs),
	)

	if len(normalIDs) > 0 {
		rows, err := s.reader.MarkAnalyzed(ctx, s.analyzerID, normalIDs)
		if err != nil {
			s.logger.Error("failed to mark normal metrics as analyzed, will retry next poll",
				"err", err, "count", len(normalIDs), "ids", normalIDs)
			return
		}
		if rows != int64(len(normalIDs)) {
			s.logger.Error("mark analyzed: row count mismatch, metrics may be lost",
				"expected", len(normalIDs), "affected", rows, "ids", normalIDs)
			return
		}
	}

	if len(notifyIDs) > 0 {
		if err := s.producer.Produce(ctx, anomalies); err != nil {
			s.logger.Error("failed to publish anomalies to Kafka, will retry next poll",
				"err", err, "count", len(notifyIDs), "ids", notifyIDs)
			return
		}
		s.logger.Info("anomalies published to Kafka",
			"anomaly_count", len(anomalies),
			"metric_count", len(notifyIDs))

		rows, err := s.reader.MarkAnalyzed(ctx, s.analyzerID, notifyIDs)
		if err != nil {
			s.logger.Error("anomalies published but failed to mark anomalous metrics, will retry",
				"err", err, "count", len(notifyIDs), "ids", notifyIDs)
			return
		}
		if rows != int64(len(notifyIDs)) {
			s.logger.Error("mark analyzed: row count mismatch for anomalous metrics",
				"expected", len(notifyIDs), "affected", rows, "ids", notifyIDs)
			return
		}
	}
}
