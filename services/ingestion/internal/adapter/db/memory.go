package db

import (
	"context"
	"sync"

	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

// MemoryStorage is an in-memory implementation of core.Storage for development.
//
// Production: use db.PostgresStorage backed by pgx + TimescaleDB.
// The actual migration SQL lives in migrations/001_create_schema.sql.
type MemoryStorage struct {
	mu      sync.Mutex
	batches []core.Batch
}

func NewMemoryStorage() *MemoryStorage { return &MemoryStorage{} }

func (s *MemoryStorage) Save(_ context.Context, b core.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, b)
	return nil
}
