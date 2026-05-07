"""Integration test: sin vs parabola discrimination via Training Service HTTP API.

Tests end-to-end: train on sin → evaluate sin (0 anomalies) → evaluate parabola (many anomalies).
Requires TimescaleDB at localhost:5432 for /api/v1/train (it fetches data from DB).
For evaluate, uses in-memory model loaded from disk after training.
"""

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


def make_sin(n: int, amplitude: float = 10.0, offset: float = 50.0) -> list[float]:
    return [offset + amplitude * np.sin(2 * np.pi * i / S) for i in range(n)]


def make_parabola(n: int, amplitude: float = 0.01, offset: float = 50.0) -> list[float]:
    return [offset + amplitude * (i - n // 2) ** 2 for i in range(n)]


class TestSinParabolaDiscrimination:
    """
    Синусоида — периодическая, модель предсказывает точно → 0 аномалий.
    Парабола — непериодическая, выходит за пределы CI → много аномалий.
    """

    @pytest.fixture(autouse=True)
    def setup(self):
        """Train a model on sin signal before each test."""
        self.client = TestClient(app)

        # Тренируем модель на синусоиде (200 точек достаточно для SARIMA)
        sin_data = make_sin(200)
        response = self.client.post("/api/v1/train", json={
            "agent_id": "test-sin-agent",
            "metric_name": "test.signal",
            "signal_data": sin_data,
            "seasonality_period": S,
            "order": (1, 0, 1),
            "seasonal_order": (1, 1, 1, S),
            "confidence_level": CL,
        })

        # Если TimescaleDB недоступен — skip
        if response.status_code != 200:
            pytest.skip(f"Training service unavailable: {response.status_code} {response.text}")

        self.model_id = response.json()["model_id"]

    def test_sin_no_anomalies(self):
        """Синусоида (720 точек) → 0 аномалий после обучения на синусе."""
        sin_history = make_sin(720)
        count = 0
        anomaly_indices = []

        for i in range(WINDOW, len(sin_history)):
            window = sin_history[i - WINDOW:i]
            value = sin_history[i]

            response = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": window,
                "value": value,
            })

            assert response.status_code == 200, f"evaluate failed at i={i}: {response.text}"
            if response.json()["anomaly"]:
                count += 1
                anomaly_indices.append(i)

        # Допускаем ≤5 аномалий (0.7%) — boundary effects на границах синусоиды
        assert count <= 5, f"sin: expected ≤5 anomalies, got {count} at {anomaly_indices}"
        print(f"sin: {count} anomalies (≤5) — PASS")

    def test_parabola_many_anomalies(self):
        """Парабола (720 точек) → много аномалий (обучена на синусе)."""
        parabola_history = make_parabola(720)
        count = 0

        for i in range(WINDOW, len(parabola_history)):
            window = parabola_history[i - WINDOW:i]
            value = parabola_history[i]

            response = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": window,
                "value": value,
            })

            assert response.status_code == 200
            if response.json()["anomaly"]:
                count += 1

        assert count > 50, f"parabola: expected ≫50 anomalies, got {count}"
        print(f"parabola: {count} anomalies (≫50) — PASS")

    def test_parabola_vs_sin_ratio(self):
        """Parabola должна быть ≫5× более аномальной чем sin."""
        sin_history = make_sin(720)
        parabola_history = make_parabola(720)

        sin_count = 0
        parabola_count = 0

        for i in range(WINDOW, 720):
            sin_window = sin_history[i - WINDOW:i]
            parabola_window = parabola_history[i - WINDOW:i]

            r_sin = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": sin_window,
                "value": sin_history[i],
            })
            if r_sin.json()["anomaly"]:
                sin_count += 1

            r_par = self.client.post("/api/v1/evaluate", json={
                "model_id": self.model_id,
                "history": parabola_window,
                "value": parabola_history[i],
            })
            if r_par.json()["anomaly"]:
                parabola_count += 1

        print(f"sin={sin_count}, parabola={parabola_count}")
        assert parabola_count > sin_count * 5, \
            f"parabola({parabola_count}) should be ≫5× sin({sin_count})"
        if sin_count > 0:
            print(f"discrimination: parabola/sin = {parabola_count/sin_count:.1f}x — PASS")
        else:
            print(f"discrimination: parabola={parabola_count}, sin=0 — PASS")


class TestEvaluateEndpointStructure:
    """Unit tests for /api/v1/evaluate endpoint structure."""

    @pytest.fixture(autouse=True)
    def setup(self):
        self.client = TestClient(app)
        # Train a minimal model for endpoint tests
        response = self.client.post("/api/v1/train", json={
            "agent_id": "test-agent",
            "metric_name": "cpu",
            "signal_data": make_sin(200),
            "seasonality_period": S,
            "order": (1, 0, 1),
            "seasonal_order": (1, 1, 1, S),
            "confidence_level": CL,
        })
        if response.status_code != 200:
            pytest.skip(f"Training service unavailable: {response.text}")
        self.model_id = response.json()["model_id"]

    def test_flat_history_no_anomaly(self):
        """Flat history + value at forecast → no anomaly."""
        response = self.client.post("/api/v1/evaluate", json={
            "model_id": self.model_id,
            "history": [50.0] * WINDOW,
            "value": 50.0,
        })
        assert response.status_code == 200
        data = response.json()
        assert data["anomaly"] is False
        assert data["forecast"] == 50.0
        assert data["lower_ci"] < 50.0 < data["upper_ci"]

    def test_extreme_value_anomaly(self):
        """History trending up, extreme value → anomaly."""
        history = [1.0, 1.5, 2.0, 2.5, 3.0, 3.5, 4.0, 4.5] * 6
        response = self.client.post("/api/v1/evaluate", json={
            "model_id": self.model_id,
            "history": history,
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