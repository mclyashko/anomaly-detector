package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

// Storage persists metric batches to TimescaleDB via pgx.
type Storage struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewStorage connects to the database and returns a ready Storage.
func NewStorage(ctx context.Context, dsn string, logger *slog.Logger) (*Storage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pgx pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	logger.Info("postgres connected")
	return &Storage{pool: pool, logger: logger}, nil
}

// Close releases the connection pool.
func (s *Storage) Close() {
	s.pool.Close()
}

// Pool returns the underlying pgx connection pool so callers (e.g. the migrator)
// can run raw SQL against the same pool.
func (s *Storage) Pool() *pgxpool.Pool { return s.pool }

// Save inserts every metric in the batch as a separate row.
// Batch-level fields (agent_id, received_at) are denormalised into each row.
func (s *Storage) Save(ctx context.Context, b core.Batch) error {
	if len(b.Metrics) == 0 {
		return nil
	}

	// Build a single INSERT with a VALUES clause per row.
	// pgxExec expects one $N placeholder per value, repeating for each row.
	var args []any
	var vals []string
	for _, m := range b.Metrics {
		labels, err := json.Marshal(m.Labels)
		if err != nil {
			s.logger.Warn("skipping metric: failed to marshal labels",
				"name", m.Name, "err", err)
			continue
		}
		offset := len(args) + 1 // 1-based placeholder index within this query
		vals = append(vals, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d)",
			offset, offset+1, offset+2, offset+3, offset+4, offset+5))
		args = append(args, m.Timestamp, b.AgentID, m.Name, m.Value, labels, string(m.Type))
	}

	if len(vals) == 0 {
		return nil
	}

	sql := fmt.Sprintf("INSERT INTO metrics (time,agent_id,name,value,labels,metric_type) VALUES %s",
		strings.Join(vals, ","))
	_, err := s.pool.Exec(ctx, sql, args...)
	if err != nil {
		return fmt.Errorf("batch insert %d rows: %w", len(vals), err)
	}
	return nil
}
