package core

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// IngestionService orchestrates validation and storage of metric batches.
type IngestionService struct {
	storage Storage
	logger  *slog.Logger
}

// NewIngestionService creates an ingestion service with only storage (no forwarding).
func NewIngestionService(storage Storage, logger *slog.Logger) *IngestionService {
	return &IngestionService{
		storage: storage,
		logger:  logger,
	}
}

// HandleBatch validates and persists a batch to storage.
// This is the main entry point for metric ingestion.
func (s *IngestionService) HandleBatch(ctx context.Context, b Batch) error {
	if err := validateBatch(b); err != nil {
		return err
	}

	b.ReceivedAt = time.Now().UTC()

	if err := s.storage.Save(ctx, b); err != nil {
		s.logger.Error("storage save failed", "agent_id", b.AgentID, "err", err)
		return fmt.Errorf("storage: %w", err)
	}

	s.logger.Debug("batch stored", "agent_id", b.AgentID, "metrics", len(b.Metrics))
	return nil
}
