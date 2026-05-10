package port

import "context"

// MetricConsumer receives metric batches from a transport (HTTP, Kafka, etc.)
// and submits them to the ingestion service.
// Implementations must be safe for concurrent use.
type MetricConsumer interface {
	// Start begins consuming messages. It blocks until the context is cancelled
	// or an unrecoverable error occurs.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the consumer. It must be called after Start returns.
	Stop()
}
