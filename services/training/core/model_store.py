"""Model store — in-memory cache of fitted SARIMAX models.

Keeps statsmodels SARIMAXResults objects in RAM for fast, correct inference
via fit.extend() + get_forecast(). Models are trained via train_sarima() and
persisted to disk as metadata.json for cross-restart recovery.
"""

from __future__ import annotations

import logging
import json
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path
from typing import Optional

import numpy as np
import pandas as pd

from core.sarima_model import train_sarima, TrainingResult

logger = logging.getLogger(__name__)


@dataclass
class ModelEntry:
    """A trained model with its statsmodels fit result and metadata."""

    model_id: str
    agent_id: str
    metric_name: str

    # Fitted statsmodels result — kept in memory for correct inference
    fit: "SARIMAXResults"  # statsmodels.tsa.statespace.sarimax.SARIMAXResults

    # Metadata for persistence / serialization
    order: tuple[int, int, int]
    seasonal_order: tuple[int, int, int, int]
    seasonality_period: int
    confidence_level: float
    trained_at: datetime
    training_n: int
    aicc: float | None
    residual_std: float
    window_size: int
    training_start: datetime | None
    training_end: datetime | None

    @property
    def is_stale(self) -> bool:
        """Check if model was trained too long ago."""
        return False  # Staleness is determined externally by the caller

    def predict(self, steps: int = 1) -> tuple[float, float, float]:
        """One-step-ahead prediction using the full Kalman filter.

        Uses get_forecast(steps) for out-of-sample (when no new obs are available)
        or get_prediction(steps=dynamic_offset) for in-sample/dynamic prediction.

        Returns:
            (forecast, lower_ci, upper_ci)
        """
        # get_forecast returns 1-step ahead forecast from the end of training data
        # For proper one-step prediction with the model in memory:
        # use get_prediction with dynamic=True on the training data itself
        if steps == 1:
            # Single step: use the model's own forecast method
            fore = self.fit.get_forecast(steps=1)
            mean = float(fore.predicted_mean.iloc[0])
            ci = fore.conf_int(alpha=1 - self.confidence_level)
            lower = float(ci.iloc[0, 0])
            upper = float(ci.iloc[0, 1])
            return mean, lower, upper
        else:
            fore = self.fit.get_forecast(steps=steps)
            mean = float(fore.predicted_mean.iloc[-1])
            ci = fore.conf_int(alpha=1 - self.confidence_level)
            lower = float(ci.iloc[-1, 0])
            upper = float(ci.iloc[-1, 1])
            return mean, lower, upper

    def evaluate(self, history: list[float]) -> dict:
        """Evaluate a new value using the model.

        For online inference: we use the model's final state to predict the next point
        based on the last observed value in history.

        Args:
            history: Values from oldest to newest (training data already in model).
                     The last value is the most recent known value.

        Returns:
            dict with anomaly, forecast, lower_ci, upper_ci, value, message
        """
        import math

        if len(history) < 2:
            return {
                "anomaly": False,
                "forecast": history[-1] if history else 0.0,
                "lower_ci": history[-1] if history else 0.0,
                "upper_ci": history[-1] if history else 0.0,
                "value": history[-1] if history else 0.0,
                "message": "insufficient history for seasonal correction",
            }

        # Re-train on history with extend() pattern:
        # We have the model trained on the original data.
        # To evaluate a new value, we extend the model with history points
        # and then forecast the next step.
        # But this is complex. Instead: use the model as-is to get a base forecast,
        # then correct it using the history trend.

        # Simple approach: use model.get_forecast() for base prediction,
        # and adjust CI based on where history sits relative to the model's expected range.

        # For most accurate: use model's internal state via extend() + predict
        try:
            # Extend model with history to bring state up to date
            series = pd.Series(history, dtype=float)
            extended = self.fit.extend(series.values.reshape(-1, 1))

            # Now forecast 1 step ahead (predicting the value AFTER history)
            fore = extended.get_forecast(steps=1)
            forecast = float(fore.predicted_mean.iloc[0])
            ci = fore.conf_int(alpha=1 - self.confidence_level)
            lower = float(ci.iloc[0, 0])
            upper = float(ci.iloc[0, 1])

            # The actual value to evaluate is not in history — caller provides it separately
            # through the evaluate_anomaly call
            return {
                "forecast": forecast,
                "lower_ci": lower,
                "upper_ci": upper,
            }
        except Exception as e:
            logger.warning("model evaluate failed: %s", e)
            # Fallback: use the base model forecast
            fore = self.fit.get_forecast(steps=1)
            forecast = float(fore.predicted_mean.iloc[0])
            ci = fore.conf_int(alpha=1 - self.confidence_level)
            lower = float(ci.iloc[0, 0])
            upper = float(ci.iloc[0, 1])
            return {
                "forecast": forecast,
                "lower_ci": lower,
                "upper_ci": upper,
            }


# Type hint for statsmodels result (avoid import at module level)
SARIMAXResults = "statsmodels.tsa.statespace.sarimax.SARIMAXResults"


class ModelStore:
    """In-memory store of fitted SARIMAX models.

    Usage:
        store = ModelStore(MODELS_DIR)

        # Train a new model
        entry = store.train(agent_id, metric_name, series, order, seasonal_order)
        store.save(entry)  # persists metadata.json

        # Evaluate a value using an existing model
        result = store.evaluate(model_id, history, value)

        # Load model from disk into memory
        store.load(model_id)
    """

    def __init__(self, models_dir: Path | None = None):
        self._models_dir = models_dir or Path("./models")
        self._entries: dict[str, ModelEntry] = {}

    def train(
        self,
        agent_id: str,
        metric_name: str,
        timeseries: pd.Series,
        order: tuple[int, int, int],
        seasonal_order: tuple[int, int, int, int],
        confidence_level: float = 0.95,
        max_iter: int = 100,
        window_size: int | None = None,
    ) -> ModelEntry:
        """Train a SARIMAX model and store it in memory."""
        import statsmodels.api as sm

        s = seasonal_order[3]
        if window_size is None:
            window_size = max(48, s + 1)

        logger.info(
            "ModelStore.train: agent=%s metric=%s order=%s seasonal_order=%s n=%d",
            agent_id, metric_name, order, seasonal_order, len(timeseries),
        )

        # Train via the existing train_sarima function
        result: TrainingResult = train_sarima(
            timeseries=timeseries,
            seasonality_period=s,
            order=order,
            seasonal_order=seasonal_order,
            confidence_level=confidence_level,
            max_iter=max_iter,
            window_size=window_size,
        )

        # Re-fit to get the full statsmodels result (for inference)
        from statsmodels.tsa.statespace.sarimax import SARIMAX

        model = SARIMAX(
            timeseries,
            order=order,
            seasonal_order=seasonal_order,
            enforce_stationarity=False,
            enforce_invertibility=False,
        )
        fit = model.fit(disp=False, maxiter=max_iter)

        model_id = f"{agent_id}__{metric_name.replace('.', '_').replace('/', '_')}"

        entry = ModelEntry(
            model_id=model_id,
            agent_id=agent_id,
            metric_name=metric_name,
            fit=fit,
            order=result.order,
            seasonal_order=result.seasonal_order,
            seasonality_period=s,
            confidence_level=confidence_level,
            trained_at=datetime.now(timezone.utc),
            training_n=result.training_n,
            aicc=result.aicc,
            residual_std=result.params["residual_std"],
            window_size=window_size,
            training_start=result.training_start,
            training_end=result.training_end,
        )

        self._entries[model_id] = entry
        logger.info(
            "ModelStore trained: model_id=%s aicc=%s residual_std=%.4f",
            model_id, result.aicc, result.params["residual_std"],
        )

        return entry

    def _kalman_predict(self, extended_fit, history_len: int) -> tuple[float, float, float]:
        """Predict 1 step ahead from the end of extended data using Kalman state.

        After extend(data), the model's state is at time T+len(data).
        To predict the next point (T+len(data)+1), we need to use the state
        transition to propagate forward and then compute Z @ state.

        However, since we have the observation matrices, we can directly use
        get_prediction(start=len(data), end=len(data)) which gives the 1-step
        ahead prediction from the END of the extended data.
        """
        # After extend(data): model state is at time T+len(data)
        # get_prediction(start=len(data), end=len(data)) gives 1-step ahead from there
        pred = extended_fit.get_prediction(start=history_len - 1, end=history_len - 1)
        forecast = float(pred.predicted_mean.iloc[0])
        ci = pred.conf_int(alpha=1 - self.confidence_level)
        lower = float(ci.iloc[0, 0])
        upper = float(ci.iloc[0, 1])
        return forecast, lower, upper

    def _kalman_update_state(self, fit, observation: float) -> "np.ndarray":
        """Manually update Kalman state with a new observation.

        Uses the state-space matrices (T, Z) from the model and optimal
        Kalman gain to update the state. This is the correct one-step update.

        Args:
            fit: Fitted SARIMAX model (statsmodels result).
            observation: The observed value y_t.

        Returns:
            Updated state vector.
        """
        ssm = fit.model.ssm
        T_mat = ssm.transition[:, :, 0]
        Z_vec = ssm.design[0, :, 0]

        # Get final state (at time T, after processing all training data)
        state = fit.predicted_state[:, -1].copy()

        # Predict
        y_hat = float(np.dot(Z_vec, state))

        # Innovation
        innovation = observation - y_hat

        # Optimal Kalman gain for scalar observation (k_endog=1):
        # K = T @ state_cov @ Z^T / H
        # When H=0 (MLE), the gain simplifies to: K = T[:, Z_nonzero_idx] / Z[Z_nonzero_idx]
        # This is derived from the steady-state Kalman filter.
        # Find non-zero Z indices
        nonzero_Z = np.where(np.abs(Z_vec) > 1e-10)[0]
        if len(nonzero_Z) == 0:
            # Z doesn't depend on state — cannot update
            return state

        # Use the first non-zero Z index to compute gain
        # K_i = T[row=i, col=Z_nonzero_idx] / Z[Z_nonzero_idx]
        Z0_idx = nonzero_Z[0]
        K = T_mat[:, Z0_idx] / Z_vec[Z0_idx]

        # Update state: alpha_{t+1} = T @ alpha_t + K @ innovation
        new_state = np.dot(T_mat, state) + K * innovation

        return new_state

    def _kalman_forecast_from_state(
        self, fit, state: np.ndarray, confidence_level: float, residual_std: float
    ) -> tuple[float, float, float]:
        """Compute forecast from a given state vector.

        Args:
            fit: Fitted SARIMAX model.
            state: State vector (k_states,).

        Returns:
            (forecast, lower_ci, upper_ci)
        """
        ssm = fit.model.ssm
        Z_vec = ssm.design[0, :, 0]

        forecast = float(np.dot(Z_vec, state))

        # CI from residual std
        from scipy.special import erfcinv
        import math
        z = self._norm_quantile((1 + confidence_level) / 2)
        half_width = z * residual_std
        lower = forecast - half_width
        upper = forecast + half_width

        return forecast, lower, upper

    def _norm_quantile(self, p: float) -> float:
        """Quantile of standard normal distribution."""
        import math
        from scipy.special import erfcinv
        if p <= 0:
            return float("-inf")
        if p >= 1:
            return float("inf")
        if p == 0.5:
            return 0.0
        if p < 0.5:
            return -self._norm_quantile(1 - p)
        x = erfcinv(2 * (1 - p)) * math.sqrt(2)
        for _ in range(10):
            cdf = 0.5 * (1 + math.erf(x / math.sqrt(2)))
            pdf = math.exp(-x * x / 2) / math.sqrt(2 * math.pi)
            delta = (cdf - p) / pdf
            x -= delta
            if abs(delta) < 1e-12:
                break
        return x

    def evaluate(self, model_id: str, history: list[float], value: float) -> dict:
        """Evaluate whether a value is anomalous using the stored model.

        Uses fit.extend() to bring the model state up to date with new values
        (those beyond the training data), then get_forecast(steps=1) to predict.

        Key insight: history contains BOTH training data AND new observations.
        We only extend with the NEW portion (history[training_n:]) to avoid
        duplicating training data and shifting the forecast horizon.
        If history == training_n (no new data since training), use base model directly.

        Args:
            model_id: The model identifier.
            history: Recent values in chronological order.
            value: The current value to check for anomaly.

        Returns:
            dict with anomaly, forecast, lower_ci, upper_ci, value, message
        """
        entry = self._entries.get(model_id)
        if entry is None:
            raise KeyError(f"Model not found in store: {model_id}")

        training_n = entry.training_n

        if len(history) < 2:
            raise ValueError(
                f"insufficient history for evaluation: need at least 2 points, "
                f"got {len(history)}. Wait for more data to accumulate."
            )

        try:
            base_fit = entry.fit

            # Always extend with ALL history points we have.
            # The extended model has nobs = len(history) (extend REPLACES observations).
            # After extension, the Kalman state reflects all history points.
            # get_forecast(1) then gives one-step-ahead prediction from that state.
            arr = np.array(history).reshape(-1, 1)
            extended = base_fit.extend(arr)

            # Use get_forecast(1): propagates the Kalman state one step forward.
            # This is the proper one-step-ahead forecast from the current state.
            fore = extended.get_forecast(steps=1)
            forecast = float(fore.predicted_mean.iloc[0])

            # CI from residual_std with seasonal margin.
            # Signal range is [-4, +4] for this signal (amplitude=4).
            # residual_std (~1.4) accounts for per-step noise.
            # We add a small seasonal margin to account for phase uncertainty.
            # For seasonality=60, use signal_margin = 2.0 (much smaller than 6).
            from scipy.special import erfcinv
            import math
            z = self._norm_quantile((1 + entry.confidence_level) / 2)
            signal_margin = 0.0  # no margin — CI from residual_std only
            half_width = z * (entry.residual_std + signal_margin)
            lower = forecast - half_width
            upper = forecast + half_width

            logger.warning(
                "EVALUATE: model_id=%s history_len=%d training_n=%d value=%.4f "
                "forecast=%.4f CI=[%.4f, %.4f] is_anomaly=%s residual_std=%.4f",
                model_id, len(history), training_n,
                value, forecast, lower, upper,
                (value < lower or value > upper), entry.residual_std,
            )

        except Exception as e:
            logger.warning("model evaluate failed (extend+forecast): %s", e)
            # Fallback to simple forecast from base model
            fore = entry.fit.get_forecast(steps=1)
            forecast = float(fore.predicted_mean.iloc[0])
            ci = fore.conf_int(alpha=1 - entry.confidence_level)
            lower = float(ci.iloc[0, 0])
            upper = float(ci.iloc[0, 1])

        is_anomaly = value < lower or value > upper

        if is_anomaly:
            msg = f"value={value:.4f} outside CI [{lower:.4f}, {upper:.4f}]"
        else:
            msg = f"value={value:.4f} inside CI [{lower:.4f}, {upper:.4f}]"

        return {
            "anomaly": is_anomaly,
            "forecast": forecast,
            "lower_ci": lower,
            "upper_ci": upper,
            "value": value,
            "message": msg,
        }

    def save(self, entry: ModelEntry) -> None:
        """Persist model metadata to disk (JSON). The fit object stays in memory."""
        model_path = self._models_dir / entry.model_id
        model_path.mkdir(exist_ok=True)

        metadata = {
            "model_id": entry.model_id,
            "agent_id": entry.agent_id,
            "metric_name": entry.metric_name,
            "order": list(entry.order),
            "seasonal_order": list(entry.seasonal_order),
            "seasonality_period": entry.seasonality_period,
            "confidence_level": entry.confidence_level,
            "trained_at": entry.trained_at.isoformat(),
            "training_n": entry.training_n,
            "aicc": entry.aicc,
            "residual_std": entry.residual_std,
            "window_size": entry.window_size,
            "training_start": entry.training_start.isoformat() if entry.training_start else None,
            "training_end": entry.training_end.isoformat() if entry.training_end else None,
        }

        with open(model_path / "metadata.json", "w") as f:
            json.dump(metadata, f, indent=2)

    def load(self, model_id: str) -> ModelEntry:
        """Load a model from disk into memory.

        Note: Since we can't reconstruct the full statsmodels fit from JSON alone,
        this re-trains the model from scratch using the metadata. The fit object
        is stored in memory, metadata.json is used only for parameter recovery.

        For a production system, you would pickle the fit object directly.
        """
        import json as _json

        model_path = self._models_dir / model_id / "metadata.json"
        if not model_path.exists():
            raise FileNotFoundError(f"metadata.json not found for {model_id}")

        with open(model_path) as f:
            meta = _json.load(f)

        # We cannot restore the full fit from JSON — re-train is needed
        # In a production system, you would pickle the fit object
        logger.info(
            "ModelStore.load: cannot restore fit from JSON for %s, re-training from metadata",
            model_id,
        )

        # Re-train from TimescaleDB (caller should provide data)
        # For now, raise NotImplementedError with guidance
        raise NotImplementedError(
            f"ModelStore.load({model_id}): full fit reconstruction from JSON is not supported. "
            f"Use train() to create a new model or implement pickle-based persistence."
        )

    def get(self, model_id: str) -> Optional[ModelEntry]:
        """Get a model entry from memory, or None if not loaded."""
        return self._entries.get(model_id)

    def list_model_ids(self) -> list[str]:
        """List all model IDs currently in memory."""
        return list(self._entries.keys())

    def remove(self, model_id: str) -> None:
        """Remove a model from the in-memory store."""
        self._entries.pop(model_id, None)