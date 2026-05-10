package core

import "context"

// Storage persists incoming metric batches.
// Implement with TimescaleDB (pgx) for production.
type Storage interface {
	Save(ctx context.Context, b Batch) error
}
