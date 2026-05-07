"""Tests for infer.py — AR(1) anomaly detection logic."""

import math
import os
import sys

# Add services/training to path so 'infer' is importable.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import pytest
from infer import evaluate_anomaly, norm_quantile


class TestNormQuantile:
    """Test the normal quantile (inverse CDF) function."""

    def test_midpoint_returns_zero(self):
        assert norm_quantile(0.5) == pytest.approx(0.0, abs=1e-10)

    def test_95_percentile(self):
        # z(0.975) ≈ 1.96
        z = norm_quantile(0.975)
        assert abs(z - 1.95996) < 0.001

    def test_99_percentile(self):
        # z(0.995) ≈ 2.576
        z = norm_quantile(0.995)
        assert abs(z - 2.5758) < 0.001

    def test_symmetry(self):
        # Q(p) = -Q(1-p)
        for p in [0.1, 0.2, 0.3, 0.4]:
            assert norm_quantile(p) == pytest.approx(-norm_quantile(1 - p), abs=1e-10)

    def test_below_05_negative(self):
        assert norm_quantile(0.1) < 0

    def test_above_05_positive(self):
        assert norm_quantile(0.9) > 0

    def test_at_boundaries(self):
        assert math.isinf(norm_quantile(0.0))
        assert math.isinf(norm_quantile(1.0))


class TestEvaluateAnomaly:
    """Test the main evaluate_anomaly function."""

    def test_no_anomaly_inside_ci(self):
        # Test with flat history: value == forecast, well within CI
        history = [50.0] * 48
        value = 50.0

        result = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.886,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.08,
            seasonality_period=24,
            confidence_level=0.95,
        )

        # value equals forecast (flat history) — no anomaly
        assert not result["anomaly"]
        assert "forecast" in result
        assert "lower_ci" in result
        assert "upper_ci" in result
        assert result["lower_ci"] < result["forecast"] < result["upper_ci"]

    def test_anomaly_outside_upper_ci(self):
        history = [1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5] * 6
        value = 100.0

        result = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.1,
            seasonality_period=24,
            confidence_level=0.95,
        )

        assert result["anomaly"] == True
        assert value > result["upper_ci"]

    def test_anomaly_outside_lower_ci(self):
        history = [100.0] * 48
        value = 1.0

        result = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.5,
            seasonality_period=24,
            confidence_level=0.95,
        )

        assert result["anomaly"] == True
        assert value < result["lower_ci"]

    def test_seasonal_ar_effect(self):
        # With seasonal AR, forecast adjusts for seasonal difference.
        # For n=48, S=24: seasonal_val = history[-S-1] = history[-25] = history[23]
        # history[23] = 40 (seasonal trough), last = 50
        # seasonal_correction = 0.5 * (50 - 40) = 5
        S = 24
        history = [50.0] * 48
        history[23] = 40.0  # seasonal trough at index 23 = history[-25]

        result_no_seasonal = evaluate_anomaly(
            history=history,
            value=50.0,
            ar_params=0.0,  # no ar correction to isolate seasonal effect
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=1.0,
            seasonality_period=S,
            confidence_level=0.95,
        )

        result_with_seasonal = evaluate_anomaly(
            history=history,
            value=50.0,
            ar_params=0.0,  # no ar correction to isolate seasonal effect
            ma_params=0.0,
            seasonal_ar_params=0.5,
            seasonal_ma_params=0.0,
            residual_std=1.0,
            seasonality_period=S,
            confidence_level=0.95,
        )

        # With seasonal AR, forecast should be higher due to +5 correction
        assert result_with_seasonal["forecast"] > result_no_seasonal["forecast"]
        assert result_no_seasonal["forecast"] == 50.0
        assert result_with_seasonal["forecast"] == 55.0

    def test_insufficient_history_returns_no_anomaly(self):
        history = [1.0]
        value = 10.0

        result = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.1,
            seasonality_period=24,
            confidence_level=0.95,
        )

        assert "forecast" in result
        assert "anomaly" in result

    def test_message_contains_values(self):
        history = [1.0, 2.0, 3.0, 4.0, 5.0] * 10
        value = 10.0

        result = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.1,
            seasonality_period=24,
            confidence_level=0.95,
        )

        msg = result["message"]
        assert "value=" in msg
        assert "CI [" in msg

    def test_ci_width_increases_with_residual_std(self):
        history = [1.0, 2.0, 3.0, 4.0, 5.0] * 10
        value = 5.0

        result_narrow = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.1,
            seasonality_period=24,
            confidence_level=0.95,
        )

        result_wide = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=1.0,
            seasonality_period=24,
            confidence_level=0.95,
        )

        narrow_width = result_narrow["upper_ci"] - result_narrow["lower_ci"]
        wide_width = result_wide["upper_ci"] - result_wide["lower_ci"]
        assert wide_width > narrow_width

    def test_higher_confidence_wider_ci(self):
        history = [1.0, 2.0, 3.0, 4.0, 5.0] * 10
        value = 5.0

        result_95 = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.5,
            seasonality_period=24,
            confidence_level=0.95,
        )

        result_99 = evaluate_anomaly(
            history=history,
            value=value,
            ar_params=0.5,
            ma_params=0.0,
            seasonal_ar_params=0.0,
            seasonal_ma_params=0.0,
            residual_std=0.5,
            seasonality_period=24,
            confidence_level=0.99,
        )

        ci_95_width = result_95["upper_ci"] - result_95["lower_ci"]
        ci_99_width = result_99["upper_ci"] - result_99["lower_ci"]
        assert ci_99_width > ci_95_width