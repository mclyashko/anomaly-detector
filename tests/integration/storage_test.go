package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// projectRoot returns the project root directory by locating go.work.
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
CREATE TABLE IF NOT EXISTS metrics (
    id          BIGSERIAL,
    time        TIMESTAMPTZ NOT NULL,
    agent_id    TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    value       DOUBLE PRECISION NOT NULL,
    labels      JSONB,
    metric_type TEXT
);
SELECT create_hypertable('metrics', 'time', if_not_exists => TRUE);
ALTER TABLE metrics ADD PRIMARY KEY (time, id);
CREATE INDEX IF NOT EXISTS idx_metrics_id ON metrics (id);
CREATE INDEX IF NOT EXISTS idx_metrics_agent_time ON metrics (agent_id, time DESC);
CREATE INDEX IF NOT EXISTS idx_metrics_name ON metrics (name);
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

// TestIngestionHTTPEndToEnd starts the ingestion service with TimescaleDB,
// POSTs a batch over HTTP, and verifies the rows end up in the database.
func TestIngestionHTTPEndToEnd(t *testing.T) {
	composeFile := filepath.Join(projectRoot(), "infra", "docker-compose.yml")

	// Start only DB and ingestion (not the full stack).
	if out, err := dockerCompose("-f", composeFile, "up", "-d",
		"timescaledb", "ingestion"); err != nil {
		t.Fatalf("docker compose up failed: %s\n%v", out, err)
	}
	t.Cleanup(func() {
		dockerCompose("-f", composeFile, "down", "-v")
	})

	// Wait for ingestion HTTP server to be healthy.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost:8080/healthz", nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if ctx.Err() != nil {
		t.Fatal("ingestion did not become healthy in time")
	}

	// POST a batch.
	payload := map[string]any{
		"agent_id": "integration-test",
		"metrics": []map[string]any{
			{"name": "cpu_usage", "value": 0.8, "type": "gauge",
				"timestamp": time.Now().UTC().Format(time.RFC3339)},
			{"name": "mem_usage", "value": 0.6, "type": "gauge",
				"timestamp": time.Now().UTC().Format(time.RFC3339)},
		},
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post("http://localhost:8080/api/v1/ingest",
		"application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 202, got %d: %s", resp.StatusCode, b)
	}

	// Verify data was inserted.
	dsn := "postgres://postgres:secret@localhost:5432/anomaly?sslmode=disable"
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()

	conn, err := pgx.Connect(dbCtx, dsn)
	if err != nil {
		t.Fatalf("pgx.Connect failed: %v", err)
	}
	defer conn.Close(dbCtx)

	var count int
	err = conn.QueryRow(dbCtx,
		"SELECT COUNT(*) FROM metrics WHERE agent_id='integration-test'").Scan(&count)
	if err != nil {
		t.Fatalf("SELECT COUNT failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 rows stored, got %d", count)
	}
}

func dockerCompose(args ...string) (string, error) {
	out, err := exec.Command("docker", append([]string{"compose"}, args...)...).CombinedOutput()
	return string(out), err
}
