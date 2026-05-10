package port

import (
	"context"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// Collector scrapes a single source of metrics.
type Collector interface {
	Name() string
	Collect(ctx context.Context) ([]domain.Metric, error)
}
