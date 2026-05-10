package db

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrator applies numbered SQL migration files to a database.
type Migrator struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
}

// NewMigrator returns a Migrator ready to run.
func NewMigrator(pool *pgxpool.Pool, logger *slog.Logger) *Migrator {
	return &Migrator{pool: pool, logger: logger}
}

// Up runs all pending migrations in order.
func (m *Migrator) Up(ctx context.Context) error {
	m.logger.Info("running database migrations")

	// Ensure the schema_migrations tracking table exists.
	if _, err := m.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations directory: %w", err)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // 001_, 002_ … ensures order

	for _, name := range names {
		if m.isApplied(ctx, name) {
			m.logger.Debug("migration already applied", "version", name)
			continue
		}

		sql, err := migrationsFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		m.logger.Info("applying migration", "version", name)
		if _, err := m.pool.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}

		if err := m.recordApplied(ctx, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		m.logger.Info("migration applied", "version", name)
	}

	m.logger.Info("migrations complete")
	return nil
}

func (m *Migrator) isApplied(ctx context.Context, version string) bool {
	var exists bool
	_ = m.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version).Scan(&exists)
	return exists
}

func (m *Migrator) recordApplied(ctx context.Context, version string) error {
	_, err := m.pool.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
	return err
}
