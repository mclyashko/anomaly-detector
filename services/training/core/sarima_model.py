"""SARIMA model training for time-series anomaly detection.

This module trains a SARIMA (Seasonal ARIMA) model on historical metric data
and produces a forecast function that can be used for anomaly scoring.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from datetime import datetime

import numpy as np
import pandas as pd

logger = logging.getLogger(__name__)


@dataclass
class TrainingResult:
    """Result of a SARIMA training run."""

    # Model order: (p, d, q)
    order: tuple[int, int, int]
    # Seasonal order: (P, D, Q, s)
    seasonal_order: tuple[int, int, int, int]
    # Confidence level used for intervals
    confidence_level: float
    # Number of data points used for training
    training_n: int
    # Start of training time range
    training_start: datetime | None
    # End of training time range
    training_end: datetime | None
    # AICC score (lower = better fit, use for model comparison)
    aicc: float | None
    # Raw fitted model parameters (for ONNX export)
    params: dict


def train_sarima(
    timeseries: pd.Series,
    seasonality_period: int,
    order: tuple[int, int, int],
    seasonal_order: tuple[int, int, int, int],
    confidence_level: float = 0.95,
    max_iter: int = 100,
    window_size: int = 48,
) -> TrainingResult:
    """Train a SARIMA model on the given time series.

    Args:
        timeseries:        Pandas Series with datetime index, sorted ascending.
        seasonality_period: Seasonal period (e.g. 24 for hourly data with daily season).
        order:             ARIMA order (p, d, q) — e.g. (1, 0, 1).
        seasonal_order:    Seasonal ARIMA order (P, D, Q, s) — e.g. (1, 1, 1, 24).
        confidence_level:  Confidence level for prediction intervals (0 < level < 1).
        max_iter:          Maximum EM algorithm iterations for fitting.

    Returns:
        TrainingResult with model parameters and metadata.

    Raises:
        ValueError: If timeseries has fewer than 2 * seasonality_period data points
                    when seasonality_period > 1, or fewer than 10 points overall.
    """
    if len(timeseries) < 10:
        raise ValueError(
            f"timeseries must have at least 10 points for SARIMA, got {len(timeseries)}"
        )

    if seasonality_period > 1 and len(timeseries) < 2 * seasonality_period:
        raise ValueError(
            f"timeseries must have at least 2 * seasonality_period points "
            f"({2 * seasonality_period}) for seasonal SARIMA, got {len(timeseries)}"
        )

    # Import statsmodels lazily so the module doesn't hard-require it at import time.
    from statsmodels.tsa.statespace.sarimax import SARIMAX

    logger.info(
        "training SARIMA: order=%s, seasonal_order=%s, points=%d",
        order,
        seasonal_order,
        len(timeseries),
    )

    # Fit SARIMAX model.
    model = SARIMAX(
        timeseries,
        order=order,
        seasonal_order=seasonal_order,
        enforce_stationarity=False,
        enforce_invertibility=False,
    )

    fit = model.fit(disp=False, maxiter=max_iter)

    # Compute aicc if available.
    aicc: float | None = None
    if hasattr(fit, "aicc") and fit.aicc is not None:
        aicc = float(fit.aicc)

    # Extract key parameters for ONNX export.
    # statsmodels SARIMAX names parameters by their lag notation:
    #   ar.L1 — AR(1) coefficient (Lag 1)
    #   ma.L1 — MA(1) coefficient (Lag 1)
    #   ar.S.L{s} — Seasonal AR coefficient (Lag s, e.g. ar.S.L24 for hourly data)
    #   ma.S.L{s} — Seasonal MA coefficient
    # Not all parameters are present when seasonality is small — fallback to 0.0.
    params = {
        "order": order,
        "seasonal_order": seasonal_order,
        "confidence_level": confidence_level,
        # Filtered (differenced) series coefficients.
        "ar_params": fit.params.get("ar.L1", 0.0) if "ar.L1" in fit.params.index else 0.0,
        "ma_params": fit.params.get("ma.L1", 0.0) if "ma.L1" in fit.params.index else 0.0,
        "seasonal_ar_params": (
            float(fit.params.get(f"ar.S.L{seasonality_period}", 0.0))
            if f"ar.S.L{seasonality_period}" in fit.params.index
            else 0.0
        ),
        "seasonal_ma_params": (
            float(fit.params.get(f"ma.S.L{seasonality_period}", 0.0))
            if f"ma.S.L{seasonality_period}" in fit.params.index
            else 0.0
        ),
        # Residual standard deviation for confidence intervals.
        "residual_std": float(fit.resid.std()),
        # Scale / sigma2 from the model.
        "sigma2": float(fit.params.get("sigma2", 1.0)),
        # Differencing order.
        "d": order[1],
        "seasonal_d": seasonal_order[1],
        "seasonality_period": seasonality_period,
        "window_size": window_size,
        # Mean for the stationary series (used in forecast).
        "forecast_mean": float(fit.forecast(1).iloc[0]) if len(timeseries) > 0 else 0.0,
    }

    logger.info(
        "SARIMA training complete: aicc=%s, residual_std=%.4f",
        aicc,
        params["residual_std"],
    )

    # Try to extract training time range from the series index.
    # If the index is not datetime-like (e.g. plain integer positions), use None.
    training_start: datetime | None = None
    training_end: datetime | None = None
    if len(timeseries) > 0:
        idx = timeseries.index[0]
        if hasattr(idx, "to_pydatetime"):
            training_start = idx.to_pydatetime()
            training_end = timeseries.index[-1].to_pydatetime()

    return TrainingResult(
        order=order,
        seasonal_order=seasonal_order,
        confidence_level=confidence_level,
        training_n=len(timeseries),
        training_start=training_start,
        training_end=training_end,
        aicc=aicc,
        params=params,
    )


def build_forecast(
    params: dict,
    recent_values: np.ndarray,
    horizon: int = 1,
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    """Build a point forecast and confidence intervals from SARIMA parameters.

    This is a simplified linear forecast using the fitted AR/MA coefficients.
    For production use, the full SARIMAX model should be re-evaluated.

    Args:
        params:         TrainingResult.params dictionary.
        recent_values: Last N values of the differenced series (most recent last).
        horizon:       Number of steps ahead to forecast.

    Returns:
        Tuple of (point_forecast, lower_bound, upper_bound), all as numpy arrays
        of shape (horizon,).
    """
    ar = params["ar_params"]
    ma = params["ma_params"]
    seasonal_ar = params["seasonal_ar_params"]
    seasonal_ma = params["seasonal_ma_params"]
    residual_std = params["residual_std"]
    seasonality_period = params["seasonality_period"]
    d = params["d"]
    seasonal_d = params["seasonal_d"]
    confidence_level = params["confidence_level"]

    # Simplified forecast using last value + AR correction.
    # This is a naive forecast for ONNX export — the real SARIMAX handles
    # the full recursive forecast.
    n = len(recent_values)
    forecasts = np.zeros(horizon)
    lower = np.zeros(horizon)
    upper = np.zeros(horizon)

    # Z-score for the given confidence level.
    from scipy.stats import norm

    z = norm.ppf((1 + confidence_level) / 2)

    last_val = recent_values[-1] if n > 0 else 0.0

    for h in range(horizon):
        # Simple AR(1)-like correction.
        ar_correction = ar * (recent_values[-1] - (recent_values[-2] if n > 1 else recent_values[-1]))
        forecasts[h] = last_val + ar_correction
        # Expand uncertainty for multi-step forecasts.
        margin = z * residual_std * np.sqrt(h + 1)
        lower[h] = forecasts[h] - margin
        upper[h] = forecasts[h] + margin

    return forecasts, lower, upper
