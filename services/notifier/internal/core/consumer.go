package core

import "context"

// EventConsumer consumes anomaly events from a message broker and
// forwards them to the incident service for processing.
type EventConsumer interface {
	// Consume starts consuming messages and stops when the context is cancelled.
	Consume(ctx context.Context) error
}
