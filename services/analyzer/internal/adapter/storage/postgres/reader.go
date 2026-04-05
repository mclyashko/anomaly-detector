package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// Reader implements port.MetricReader by fetching from PostgreSQL/TimescaleDB.
type Reader struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewReader creates a new PostgreSQL metric reader.
func NewReader(ctx context.Context, dsn string, logger *slog.Logger) (*Reader, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger.Info("connected to PostgreSQL (analyzer)")

	return &Reader{pool: pool, logger: logger}, nil
}

// FetchUnanalyzed returns metrics that haven't been analyzed yet.
// Uses FOR UPDATE SKIP LOCKED to support multiple analyzers running concurrently.
func (r *Reader) FetchUnanalyzed(ctx context.Context, analyzerID string, limit int) ([]core.Metric, error) {
	query := `
		SELECT id, agent_id, name, value, labels, time, metric_type
		FROM metrics
		WHERE analyzed_at IS NULL
		ORDER BY time
		LIMIT $1
		FOR UPDATE SKIP LOCKED
	`

	rows, err := r.pool.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("fetch unanalyzed: %w", err)
	}
	defer rows.Close()

	var metrics []core.Metric
	for rows.Next() {
		var m core.Metric
		var labels []byte
		var metricType string

		if err := rows.Scan(&m.ID, &m.AgentID, &m.Name, &m.Value, &labels, &m.Timestamp, &metricType); err != nil {
			return nil, fmt.Errorf("scan metric: %w", err)
		}

		if len(labels) > 0 {
			if err := json.Unmarshal(labels, &m.Labels); err != nil {
				return nil, fmt.Errorf("unmarshal labels: %w", err)
			}
		}
		m.Type = metricType
		metrics = append(metrics, m)
	}

	return metrics, nil
}

// MarkAnalyzed marks the given metrics as analyzed.
func (r *Reader) MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) error {
	if len(metricIDs) == 0 {
		return nil
	}

	query := `
		UPDATE metrics
		SET analyzed_at = $1, analyzer_id = $2
		WHERE id = ANY($3)
	`

	_, err := r.pool.Exec(ctx, query, time.Now().UTC(), analyzerID, metricIDs)
	if err != nil {
		return fmt.Errorf("mark analyzed: %w", err)
	}

	return nil
}

// Close closes the connection pool.
func (r *Reader) Close() {
	r.pool.Close()
}
