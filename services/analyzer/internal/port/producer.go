package port

import (
	"context"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// EventProducer delivers anomaly events to a message broker.
type EventProducer interface {
	// Produce sends one or more anomaly events to the broker.
	// The broker handles delivery guarantees (at-least-once).
	Produce(ctx context.Context, anomalies []*core.Anomaly) error
}
