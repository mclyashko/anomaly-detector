"""Ports — interfaces that define the boundaries of the core domain.

These are Protocol classes (structural typing) that adapters must implement.
The core domain has zero dependencies on infrastructure.
"""

from __future__ import annotations

from typing import Protocol

import pandas as pd


class TimescaleDBPort(Protocol):
    """Port for fetching historical metric data from TimescaleDB."""

    def fetch_metrics(
        self,
        agent_id: str,
        metric_name: str,
        days: int,
    ) -> pd.Series:
        """Fetch historical time series for model training.

        Args:
            agent_id:    Identifier of the metric source (e.g. "agent-1").
            metric_name: Name of the metric (e.g. "http.latency_avg_ms").
            days:       Number of past days to fetch.

        Returns:
            Pandas Series with datetime index (ascending) and float values.
            Returns an empty Series if no data is found.
        """
        ...
