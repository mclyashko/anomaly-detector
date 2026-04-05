package port

import (
	"context"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// RuleLoader reads rule definitions from some source and returns the configs.
type RuleLoader interface {
	Load() ([]core.RuleConfig, error)
}

// NotifierSender delivers anomaly events to the notifier service.
type NotifierSender interface {
	Notify(ctx context.Context, anomalies []*core.Anomaly) error
}

// MetricReader reads unanalyzed metrics from storage.
type MetricReader interface {
	// FetchUnanalyzed returns metrics that haven't been analyzed yet.
	// Uses FOR UPDATE SKIP LOCKED to support multiple analyzers running concurrently.
	FetchUnanalyzed(ctx context.Context, analyzerID string, limit int) ([]core.Metric, error)
	// MarkAnalyzed marks metrics as analyzed by the given analyzer.
	MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) error
}
