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
-- BIGSERIAL already creates an implicit pk on `id`, so we need to drop it first.
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
