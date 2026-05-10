package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func projectRoot() string {
	wd, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.work")); err == nil {
			return wd
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return wd
		}
		wd = parent
	}
}

func dockerCompose(args ...string) (string, error) {
	out, err := exec.Command("docker", append([]string{"compose"}, args...)...).CombinedOutput()
	return string(out), err
}

// TestStorageAndMigration verifies that migrations run successfully and
// batch inserts work against a real TimescaleDB instance. This catches issues like:
// - TimescaleDB primary key requirements (partitioning column must be in PK)
// - pgx parameter placeholders (each row needs unique $N indexes)
func TestStorageAndMigration(t *testing.T) {
	composeFile := filepath.Join(projectRoot(), "tests", "integration", "docker-compose.yml")
	if composeFile == "" {
		t.Fatal("could not find project root")
	}

	// Start TimescaleDB
	if out, err := dockerCompose("-f", composeFile, "up", "-d"); err != nil {
		t.Fatalf("docker compose up failed: %s\n%v", out, err)
	}
	t.Cleanup(func() {
		dockerCompose("-f", composeFile, "down", "-v")
	})

	// Wait for TimescaleDB to be healthy
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	dsn := "postgres://postgres:secret@localhost:5432/anomaly?sslmode=disable"
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil {
			conn.Close(ctx)
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("TimescaleDB did not become healthy in time")
	}

	// Run the same migration that the ingestion service applies.
	// This verifies the schema is valid for TimescaleDB hypertables.
	migrationSQL := `
-- 001_create_schema.sql
-- Creates the base TimescaleDB schema for metric storage.

CREATE TABLE IF NOT EXISTS metrics (
    id          BIGSERIAL,
    time        TIMESTAMPTZ NOT NULL,
    agent_id    TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    labels      JSONB,
    metric_type TEXT,
    analyzed_at TIMESTAMPTZ DEFAULT NULL,
    analyzer_id TEXT DEFAULT NULL
);

-- Partition by time using TimescaleDB's hypertable for efficient time-series queries.
SELECT create_hypertable('metrics', 'time', if_not_exists => TRUE);

-- Primary key must include the partitioning column (time), so use a composite pk.
-- This also satisfies the unique index requirement for hypertables.
-- BIGSERIAL already creates an implicit pk on 'id', so we need to drop it first.
DO $$
BEGIN
    -- Drop the implicit bigserial primary key if it exists
    IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'metrics_pkey' AND contype = 'p') THEN
        ALTER TABLE metrics DROP CONSTRAINT metrics_pkey;
    END IF;
    -- Add composite primary key (time, id)
    ALTER TABLE metrics ADD PRIMARY KEY (time, id);
EXCEPTION
    WHEN undefined_object THEN NULL; -- already migrated
END
$$;

-- Index on id for direct lookups (non-unique, since time is in the pk).
CREATE INDEX IF NOT EXISTS idx_metrics_id ON metrics (id);

-- Index on agent_id + time for per-agent range queries.
CREATE INDEX IF NOT EXISTS idx_metrics_agent_time ON metrics (agent_id, time DESC);

-- Index on metric name for filtered queries.
CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics (name);

-- Index for analyzer polling: unanalyzed metrics, ordered by time.
-- Composite (analyzed_at, time) satisfies both WHERE and ORDER BY — no Sort node needed.
CREATE INDEX IF NOT EXISTS idx_metrics_analyzed_time ON metrics (analyzed_at, time) WHERE analyzed_at IS NULL;
`
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect failed: %v", err)
	}
	if _, err := conn.Exec(ctx, migrationSQL); err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	conn.Close(ctx)

	// Verify the migration schema by inserting rows using the same multi-row
	// VALUES syntax that the storage layer generates.
	conn2, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect failed: %v", err)
	}
	defer conn2.Close(ctx)

	now := time.Now().UTC()
	labels := `{"service":"test"}`
	_, err = conn2.Exec(ctx, `
		INSERT INTO metrics (time, agent_id, name, value, labels, metric_type)
		VALUES ($1,$2,$3,$4,$5,$6),($7,$8,$9,$10,$11,$12)`,
		now, "test-agent", "cpu_usage", 0.75, labels, "gauge",
		now, "test-agent", "mem_usage", 0.50, labels, "gauge",
	)
	if err != nil {
		t.Fatalf("multi-row INSERT failed: %v", err)
	}

	var count int
	err = conn2.QueryRow(ctx, "SELECT COUNT(*) FROM metrics WHERE agent_id='test-agent'").Scan(&count)
	if err != nil {
		t.Fatalf("SELECT COUNT failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}
}