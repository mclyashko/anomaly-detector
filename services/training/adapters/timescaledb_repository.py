"""TimescaleDB adapter — implements TimescaleDBPort."""

from __future__ import annotations

import logging
from datetime import datetime, timedelta, timezone

import pandas as pd
import psycopg2

logger = logging.getLogger(__name__)


class TimescaleDBRepository:
    """Fetches historical metrics from TimescaleDB for training."""

    def __init__(self, dsn: str) -> None:
        """Initialize the repository with a PostgreSQL DSN.

        Args:
            dsn: PostgreSQL connection string.
                 Example: "postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable"
        """
        self._dsn = dsn

    def fetch_metrics(
        self,
        agent_id: str,
        metric_name: str,
        days: int,
    ) -> pd.Series:
        """Fetch historical time series from TimescaleDB.

        Query selects (time, value) pairs for the given agent and metric,
        ordered ascending by time for time-series processing.
        """
        cutoff = datetime.now(timezone.utc) - timedelta(days=days)

        query = """
            SELECT time, value
            FROM metrics
            WHERE agent_id = %s
              AND name = %s
              AND time > %s
            ORDER BY time ASC
        """

        try:
            conn = psycopg2.connect(self._dsn)
            cur = conn.cursor()
            cur.execute(query, (agent_id, metric_name, cutoff))
            rows = cur.fetchall()
            cur.close()
            conn.close()
        except Exception:
            logger.exception("TimescaleDB fetch failed for %s/%s", agent_id, metric_name)
            return pd.Series(dtype=float)

        if not rows:
            logger.warning(
                "no data for agent_id=%s metric_name=%s days=%d",
                agent_id,
                metric_name,
                days,
            )
            return pd.Series(dtype=float)

        times = pd.to_datetime([r[0] for r in rows], utc=True)
        values = [float(r[1]) for r in rows]
        series = pd.Series(values, index=times)
        series.index.name = "time"
        return series

    def fetch_recent(
        self,
        agent_id: str,
        metric_name: str,
        limit: int,
    ) -> list[float]:
        """Fetch the most recent N values from TimescaleDB for training.

        Returns values in ascending time order (oldest first), suitable for
        SARIMA training.
        """
        query = """
            SELECT value
            FROM metrics
            WHERE agent_id = %s
              AND name = %s
            ORDER BY time DESC
            LIMIT %s
        """

        try:
            conn = psycopg2.connect(self._dsn)
            cur = conn.cursor()
            cur.execute(query, (agent_id, metric_name, limit))
            rows = cur.fetchall()
            cur.close()
            conn.close()
        except Exception:
            logger.exception("TimescaleDB fetch_recent failed for %s/%s limit=%d", agent_id, metric_name, limit)
            return []

        if not rows:
            logger.warning(
                "no data for agent_id=%s metric_name=%s limit=%d",
                agent_id,
                metric_name,
                limit,
            )
            return []

        return [float(r[0]) for r in rows][::-1]
