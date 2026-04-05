package db

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/db/postgres"
)

// NewPostgresStorage is a convenience wrapper that creates a *postgres.Storage
// and also exposes its underlying connection pool for use by the migrator.
func NewPostgresStorage(ctx context.Context, dsn string, logger *slog.Logger) (*postgres.Storage, *pgxpool.Pool, error) {
	st, err := postgres.NewStorage(ctx, dsn, logger)
	if err != nil {
		return nil, nil, err
	}
	return st, st.Pool(), nil
}
