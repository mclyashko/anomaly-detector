package port

import (
	"context"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// Sender delivers a batch to the ingestion service.
type Sender interface {
	Send(ctx context.Context, batch domain.Batch) error
}
