package core

import (
	"context"
	"log/slog"
	"time"
)

// Notifier is the port for forwarding anomaly events to downstream consumers.
type Notifier interface {
	Notify(ctx context.Context, anomalies []*Anomaly) error
}

// MetricFetcher is the port for reading metrics from storage.
type MetricFetcher interface {
	FetchUnanalyzed(ctx context.Context, analyzerID string, limit int) ([]Metric, error)
	MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) error
}

// AnalyzerService polls TimescaleDB for unanalyzed metrics, evaluates them, and forwards anomalies.
type AnalyzerService struct {
	reader     MetricFetcher
	engine     *RuleEngine
	notifier   Notifier
	analyzerID string
	batchSize  int
	logger     *slog.Logger
}

// NewAnalyzerService creates an analyzer that polls storage for metrics.
func NewAnalyzerService(
	reader MetricFetcher,
	engine *RuleEngine,
	notifier Notifier,
	analyzerID string,
	batchSize int,
	logger *slog.Logger,
) *AnalyzerService {
	return &AnalyzerService{
		reader:     reader,
		engine:     engine,
		notifier:   notifier,
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
			s.processMetrics(ctx)
		}
	}
}

// processMetrics fetches unanalyzed metrics, evaluates them, and marks as analyzed.
func (s *AnalyzerService) processMetrics(ctx context.Context) {
	metrics, err := s.reader.FetchUnanalyzed(ctx, s.analyzerID, s.batchSize)
	if err != nil {
		s.logger.Error("failed to fetch unanalyzed metrics", "err", err)
		return
	}

	if len(metrics) == 0 {
		s.logger.Debug("no unanalyzed metrics")
		return
	}

	s.logger.Info("processing metrics", "count", len(metrics))

	// Evaluate each metric against rules.
	var anomalyIDs []int64
	for _, m := range metrics {
		anomalies := s.engine.Evaluate(m)
		if len(anomalies) > 0 {
			s.logger.Info("anomalies detected",
				"metric_id", m.ID,
				"metric", m.Name,
				"value", m.Value,
				"anomalies", len(anomalies),
			)

			// Send anomalies to notifier.
			if err := s.notifier.Notify(ctx, anomalies); err != nil {
				s.logger.Error("failed to notify anomalies", "err", err, "metric_id", m.ID)
				// Don't mark as analyzed if notification failed.
				continue
			}
		}
		anomalyIDs = append(anomalyIDs, m.ID)
	}

	// Mark all processed metrics as analyzed.
	if len(anomalyIDs) > 0 {
		if err := s.reader.MarkAnalyzed(ctx, s.analyzerID, anomalyIDs); err != nil {
			s.logger.Error("failed to mark metrics as analyzed", "err", err, "count", len(anomalyIDs))
		} else {
			s.logger.Debug("marked metrics as analyzed", "count", len(anomalyIDs))
		}
	}
}
