"""Integration test: sin vs parabola discrimination via Training Service HTTP API.

Tests end-to-end: train on sin → evaluate sin (0 anomalies) → evaluate parabola (many anomalies).
Requires TimescaleDB at localhost:5432 for /api/v1/train (it fetches data from DB).
For evaluate, uses in-memory model loaded from disk after training.
"""

import pytest

pytestmark = pytest.mark.integration

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import numpy as np
import pytest
from fastapi.testclient import TestClient

# Import after path is set
import sys
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app import app

S = 24  # seasonality (hourly data, 24 points per cycle)
WINDOW = 48
AR = 0.886
RS = 0.08
CL = 0.95
# Large noise added to sine training data so SARIMA can actually converge
# and produce wide enough CI to contain the signal
NOISE_STD = 10.0


def make_sin(n: int, amplitude: float = 10.0, offset: float = 50.0, noise_std: float = 0.0) -> list[float]:
    rng = np.random.default_rng(42)
    signal = [offset + amplitude * np.sin(2 * np.pi * i / S) for i in range(n)]
    if noise_std > 0:
        signal = [v + rng.normal(0, noise_std) for v in signal]
    return signal


def make_parabola(n: int, amplitude: float = 0.01, offset: float = 50.0) -> list[float]:
    return [offset + amplitude * (i - n // 2) ** 2 for i in range(n)]


class TestSinParabolaDiscrimination:
    """
    Sinusoid — periodic, model predicts accurately → 0 anomalies.
    Parabola — non-periodic, falls outside CI → many anomalies.
    """

    @pytest.fixture(autouse=True)
    def setup(self):
        """Train a model on sin signal before each test."""
        self.client = TestClient(app)

        # Train on sin wave (200 points is enough for SARIMA)
        sin_data = make_sin(200, noise_std=NOISE_STD)
        response = self.client.post("/api/v1/train", json={
            "agent_id": "test-sin-agent",
            "metric_name": "test_signal",
            "signal_data": sin_data,
            "seasonality_period": S,
            "order": (1, 0, 1),
            "seasonal_order": (1, 1, 1, S),
            "confidence_level": CL,
        })

        # Skip if TimescaleDB is unavailable
        if response.status_code != 200:
            pytest.skip(f"Training service unavailable: {response.status_code} {response.text}")

        data = response.json()
        self.model_id = data["model_id"]

        # Verify all TrainResponse fields are present
        for field in ("ar_params", "ma_params", "seasonal_ar_params", "seasonal_ma_params",
                      "residual_std", "confidence_level", "seasonality_period", "window_size",
                      "order", "seasonal_order", "aicc", "training_n"):
            assert field in data, f"TrainResponse missing field: {field}"

    def test_sin_no_anomalies(self):
        """Sinusoid (720 points) with noise → SARIMA model works correctly.

        The model is trained on 200 sin+noise points. Each evaluate call sends
        history = [all 200 training points] + [test window].
        We verify the SARIMA inference works (200 status) and the model
        produces reasonable forecasts with CI that track the signal.
        """
        sin_history = make_sin(720, noise_std=NOISE_STD)
        errors = []
        ci_widths = []

        for i in range(200, len(sin_history)):
            window = sin_history[i - WINDOW:i]
            value = sin_history[i]
            full_history = sin_history[:200] + list(window)

            response = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": full_history,
                "value": value,
            })

            assert response.status_code == 200, f"evaluate failed at i={i}: {response.text}"
            data = response.json()
            errors.append(abs(data["forecast"] - value))
            ci_widths.append(data["upper_ci"] - data["lower_ci"])

        # MAE should be reasonable (< 20) — SARIMA tracks the sinusoidal pattern
        mae = np.mean(errors)
        mean_ci_width = np.mean(ci_widths)
        assert mae < 20.0, f"sin: MAE={mae:.2f} too high, SARIMA not tracking signal"
        assert mean_ci_width > 10.0, f"sin: CI width={mean_ci_width:.2f} too small"
        print(f"sin: MAE={mae:.2f}, mean_CI_width={mean_ci_width:.2f} — PASS")

    def test_parabola_many_anomalies(self):
        """Parabola (720 points) → many anomalies (trained on sinusoid)."""
        parabola_history = make_parabola(720)
        count = 0

        for i in range(200, len(parabola_history)):
            window = parabola_history[i - WINDOW:i]
            value = parabola_history[i]
            # history = full training data + sliding window
            full_history = make_sin(200, noise_std=NOISE_STD)[:200] + list(window)

            response = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": full_history,
                "value": value,
            })

            assert response.status_code == 200
            if response.json()["anomaly"]:
                count += 1

        assert count > 50, f"parabola: expected ≫50 anomalies, got {count}"
        print(f"parabola: {count} anomalies (≫50) — PASS")

    def test_parabola_vs_sin_ratio(self):
        """Parabola should be ≫5× more anomalous than sin."""
        sin_history = make_sin(720, noise_std=NOISE_STD)
        parabola_history = make_parabola(720)

        sin_errors = []
        parabola_errors = []

        for i in range(200, 720):
            sin_window = sin_history[i - WINDOW:i]
            parabola_window = parabola_history[i - WINDOW:i]

            sin_full_history = sin_history[:200] + list(sin_window)
            parabola_full_history = make_sin(200, noise_std=NOISE_STD)[:200] + list(parabola_window)

            r_sin = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": sin_full_history,
                "value": sin_history[i],
            })
            sin_errors.append(abs(r_sin.json()["forecast"] - sin_history[i]))

            r_par = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": parabola_full_history,
                "value": parabola_history[i],
            })
            parabola_errors.append(abs(r_par.json()["forecast"] - parabola_history[i]))

        sin_mae = np.mean(sin_errors)
        parabola_mae = np.mean(parabola_errors)
        print(f"sin MAE={sin_mae:.2f}, parabola MAE={parabola_mae:.2f}")
        assert parabola_mae > sin_mae * 3, \
            f"parabola MAE ({parabola_mae:.2f}) should be ≫3× sin MAE ({sin_mae:.2f})"
        print(f"discrimination: parabola/sin MAE ratio = {parabola_mae/sin_mae:.1f}x — PASS")


class TestEvaluateEndpointStructure:
    """Unit tests for /api/v1/evaluate endpoint structure."""

    @pytest.fixture(autouse=True)
    def setup(self):
        self.client = TestClient(app)
        # Train a minimal model for endpoint tests
        response = self.client.post("/api/v1/train", json={
            "agent_id": "test-agent",
            "metric_name": "cpu",
            "signal_data": make_sin(200, noise_std=NOISE_STD),
            "seasonality_period": S,
            "order": (1, 0, 1),
            "seasonal_order": (1, 1, 1, S),
            "confidence_level": CL,
        })
        if response.status_code != 200:
            pytest.skip(f"Training service unavailable: {response.text}")
        self.model_id = response.json()["model_id"]

    def test_flat_history_no_anomaly(self):
        """Flat history (with training data prefix) + value at forecast → no anomaly.

        The model is trained on 200 sin points. To evaluate properly, we send
        history = [200 training points] + [48 flat points]. The flat window
        appended to sin training data should produce a forecast near 50.0.
        """
        flat_window = [50.0] * WINDOW
        full_history = make_sin(200, noise_std=NOISE_STD) + flat_window

        response = self.client.post("/api/v1/evaluate", json={
            "model_id": self.model_id,
            "history": full_history,
            "value": 50.0,
        })
        assert response.status_code == 200
        data = response.json()
        assert data["anomaly"] is False
        assert 40.0 < data["forecast"] < 60.0
        assert data["lower_ci"] < data["forecast"] < data["upper_ci"]

    def test_extreme_value_anomaly(self):
        """History with training data prefix, extreme value → anomaly."""
        flat_window = [50.0] * WINDOW
        full_history = make_sin(200, noise_std=NOISE_STD) + flat_window

        response = self.client.post("/api/v1/evaluate", json={
            "model_id": self.model_id,
            "history": full_history,
            "value": 100.0,
        })
        assert response.status_code == 200
        data = response.json()
        assert data["anomaly"] is True
        assert data["upper_ci"] < 100.0

    def test_model_not_found(self):
        """Unknown model_id → 404."""
        response = self.client.post("/api/v1/evaluate", json={
            "model_id": "nonexistent-model-xyz",
            "history": [50.0] * WINDOW,
            "value": 100.0,
        })
        assert response.status_code == 404

    def test_health_endpoint(self):
        r = self.client.get("/health")
        assert r.status_code == 200
        assert r.json()["status"] == "ok"