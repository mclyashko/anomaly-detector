package postgres

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps the PostgreSQL connection pool.
type DB struct {
	pool *pgxpool.Pool
}

// New creates a new database connection pool.
func New(ctx context.Context, databaseURL string, logger *slog.Logger) (*DB, error) {
	// Ensure the database exists before connecting to it.
	if err := ensureDatabase(ctx, databaseURL, logger); err != nil {
		return nil, fmt.Errorf("failed to ensure database exists: %w", err)
	}

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Verify connection.
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	logger.Info("connected to PostgreSQL")

	// Run migrations.
	if _, err := pool.Exec(ctx, schemaSQL); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to run migrations: %w", err)
	}

	logger.Info("database migrations applied")

	return &DB{pool: pool}, nil
}

// ensureDatabase creates the target database if it doesn't exist.
func ensureDatabase(ctx context.Context, databaseURL string, logger *slog.Logger) error {
	// Replace database name in URL with 'postgres' to connect to default db.
	// e.g., postgres://...@host:5432/notifier -> postgres://...@host:5432/postgres
	postgresURL := strings.Replace(databaseURL, "/notifier?", "/postgres?", 1)

	pool, err := pgxpool.New(ctx, postgresURL)
	if err != nil {
		return fmt.Errorf("failed to connect to postgres db: %w", err)
	}
	defer pool.Close()

	// Check if database exists.
	var exists bool
	err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname = 'notifier')").Scan(&exists)
	if err != nil {
		return fmt.Errorf("failed to check database existence: %w", err)
	}

	if !exists {
		logger.Info("creating notifier database")
		_, err = pool.Exec(ctx, "CREATE DATABASE notifier")
		if err != nil {
			return fmt.Errorf("failed to create database: %w", err)
		}
	}

	return nil
}

// Close closes the connection pool.
func (db *DB) Close() {
	db.pool.Close()
}

// Pool returns the underlying connection pool.
func (db *DB) Pool() *pgxpool.Pool {
	return db.pool
}
