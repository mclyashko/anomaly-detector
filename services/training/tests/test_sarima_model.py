"""Tests for core/sarima_model.py."""

import numpy as np
import pandas as pd
import pytest

from training.core.sarima_model import build_forecast, train_sarima


def make_series(values: list[float], freq: str = "h") -> pd.Series:
    """Create a pandas Series with datetime index for testing."""
    n = len(values)
    start = pd.Timestamp("2024-01-01", tz="UTC")
    index = pd.date_range(start=start, periods=n, freq=freq)
    return pd.Series(values, index=index, dtype=float)


class TestTrainSarima:
    def test_raises_when_too_few_points(self):
        series = make_series([1.0, 2.0])  # only 2 points
        with pytest.raises(ValueError, match="at least 10 points"):
            train_sarima(series, seasonality_period=1, order=(1, 1, 1), seasonal_order=(0, 0, 0, 0))

    def test_raises_when_seasonal_but_insufficient_data(self):
        # seasonality=24 but only 10 points
        series = make_series([1.0] * 10)
        with pytest.raises(ValueError, match=r"2 \* seasonality_period"):
            train_sarima(series, seasonality_period=24, order=(1, 1, 1), seasonal_order=(1, 1, 1, 24))

    def test_returns_training_result(self):
        # 30 points of sinusoidal-ish data.
        values = [float(50 + 10 * np.sin(i / 5)) + np.random.normal(0, 1) for i in range(30)]
        series = make_series(values)
        result = train_sarima(series, seasonality_period=1, order=(1, 1, 1), seasonal_order=(0, 0, 0, 0))

        assert result.order == (1, 1, 1)
        assert result.seasonal_order[3] == 0  # seasonality_period=1 → non-seasonal
        assert result.training_n == 30
        assert result.training_start is not None
        assert result.training_end is not None
        assert "residual_std" in result.params
        assert result.params["seasonality_period"] == 1

    def test_non_seasonal_when_period_is_one(self):
        series = make_series([float(i) for i in range(20)])
        result = train_sarima(series, seasonality_period=1, order=(1, 1, 1), seasonal_order=(0, 0, 0, 0))
        assert result.seasonal_order == (0, 0, 0, 0)
        assert result.order == (1, 1, 1)

    def test_seasonal_when_period_greater_than_one(self):
        series = make_series([float(i % 24) for i in range(60)], freq="h")
        result = train_sarima(series, seasonality_period=24, order=(1, 1, 1), seasonal_order=(1, 1, 1, 24))
        assert result.seasonal_order[3] == 24  # s = 24


class TestBuildForecast:
    def test_returns_three_arrays(self):
        params = {
            "ar_params": 0.5,
            "ma_params": 0.1,
            "seasonal_ar_params": 0.0,
            "seasonal_ma_params": 0.0,
            "residual_std": 0.1,
            "confidence_level": 0.95,
            "seasonality_period": 1,
            "d": 1,
            "seasonal_d": 0,
        }
        recent = np.array([1.0, 1.5, 2.0, 2.5])
        forecasts, lower, upper = build_forecast(params, recent, horizon=1)

        assert len(forecasts) == 1
        assert len(lower) == 1
        assert len(upper) == 1
        assert upper[0] > forecasts[0]
        assert lower[0] < forecasts[0]

    def test_multi_step_forecast_expands_uncertainty(self):
        params = {
            "ar_params": 0.3,
            "ma_params": 0.0,
            "seasonal_ar_params": 0.0,
            "seasonal_ma_params": 0.0,
            "residual_std": 1.0,
            "confidence_level": 0.95,
            "seasonality_period": 1,
            "d": 1,
            "seasonal_d": 0,
        }
        recent = np.array([1.0, 2.0, 3.0, 4.0])
        _, lower_1, upper_1 = build_forecast(params, recent, horizon=1)
        _, lower_3, upper_3 = build_forecast(params, recent, horizon=3)

        # Step 2 of 3-step (2 steps ahead) should have wider CI than step 1 of 1-step (1 step ahead).
        assert (upper_3[1] - lower_3[1]) > (upper_1[0] - lower_1[0])
