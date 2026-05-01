"""Tests for core/training_pipeline.py."""

import pytest
from unittest.mock import MagicMock

from training.core.training_pipeline import _next_version, ModelConfig, TrainingOutcome, TrainingPipeline


class TestNextVersion:
    def test_none_returns_v1(self):
        assert _next_version(None) == "v1"

    def test_v1_returns_v2(self):
        assert _next_version("agent-1__metric__sarima__v1") == "v2"

    def test_v9_returns_v10(self):
        assert _next_version("agent-1__metric__sarima__v9") == "v10"

    def test_invalid_version_returns_v1(self):
        assert _next_version("latest") == "v1"
        assert _next_version("") == "v1"


class TestTrainingPipeline:
    def test_fetch_no_data_returns_failure_outcome(self):
        tsdb = MagicMock()
        storage = MagicMock()
        tsdb.fetch_metrics.return_value = MagicMock(__len__=lambda self: 0)

        config = ModelConfig(
            agent_id="agent-1",
            metric_name="http.latency",
            model_type="sarima",
            training_window_days=30,
            seasonality_period=24,
            confidence_level=0.95,
        )
        pipeline = TrainingPipeline(tsdb, storage, config)
        outcome = pipeline.run()

        assert not outcome.success
        assert "no data found" in outcome.error

    def test_unknown_model_type_returns_failure(self):
        import pandas as pd

        tsdb = MagicMock()
        storage = MagicMock()
        series = pd.Series([1.0, 2.0, 3.0], index=pd.date_range("2024-01-01", periods=3, freq="h"))
        tsdb.fetch_metrics.return_value = series

        config = ModelConfig(
            agent_id="agent-1",
            metric_name="http.latency",
            model_type="unknown_model",
            training_window_days=30,
            seasonality_period=1,
        )
        pipeline = TrainingPipeline(tsdb, storage, config)
        outcome = pipeline.run()

        assert not outcome.success
        assert "unknown model type" in outcome.error
