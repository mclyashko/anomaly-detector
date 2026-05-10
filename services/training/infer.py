"""SARIMA inference — computes forecast and confidence intervals for anomaly detection.

The model uses a simplified SARIMA(1,0,1)(1,0,1) with one seasonal component.
Idea: if the signal behaves predictably (has trend and seasonality),
a sharp deviation from the forecast is an anomaly.
"""

from __future__ import annotations

import math
from scipy.special import erfcinv


def norm_quantile(p: float) -> float:
    """Quantile of the standard normal distribution (inverse CDF).

    Returns the z-score such that P(Z < z) = p for a standard normal Z.
    Used to build confidence intervals.

    Method: initial approximation via erfcinv (inverse error function),
    then Newton refinement for precision to 1e-12.
    """
    if p <= 0:
        return float("-inf")
    if p >= 1:
        return float("inf")
    if p == 0.5:
        return 0.0
    if p < 0.5:
        return -norm_quantile(1 - p)

    x = erfcinv(2 * (1 - p)) * math.sqrt(2)

    for _ in range(10):
        cdf = 0.5 * (1 + math.erf(x / math.sqrt(2)))
        pdf = math.exp(-x * x / 2) / math.sqrt(2 * math.pi)
        delta = (cdf - p) / pdf
        x -= delta
        if abs(delta) < 1e-12:
            break
    return x


def evaluate_anomaly(
    history: list[float],
    value: float,
    ar_params: float,
    ma_params: float,
    seasonal_ar_params: float,
    seasonal_ma_params: float,
    residual_std: float,
    seasonality_period: int,
    confidence_level: float = 0.95,
) -> dict:
    """Determine whether the current value is anomalous relative to the history.

    Forecast formula — simplified SARIMA(1,0,1)(1,0,1):

        forecast = last
               + AR1 * (last - prev)          # short-term trend
               + MA1 * (last - prev)          # smoothing
               + SAR1 * (last - val_S_back)  # seasonal trend
               + SMA1 * (last - val_S_back)   # seasonal smoothing

    If value is outside [forecast +/- z * residual_std],
    where z is the normal quantile for the given confidence_level,
    it is flagged as anomalous.

    Args:
        history: Values from oldest to newest. At least 2 required,
                 or seasonality_period + 1 for the seasonal component.
        value: Current value to check.
        ar_params: Autoregression coefficient AR(1) — determines short-term trend strength.
        ma_params: Moving average coefficient MA(1).
        seasonal_ar_params: Seasonal AR coefficient — reacts to deviation from S periods ago.
        seasonal_ma_params: Seasonal MA coefficient.
        residual_std: Standard deviation of model residuals.
                      Determines confidence interval width.
                      Larger std = wider CI = less sensitive model.
        seasonality_period: Seasonal period S. For hourly data with daily seasonality S=24,
                            for minute data with hourly seasonality S=60.
        confidence_level: Confidence level for CI, default 0.95 (95%).

    Returns:
        Dictionary with keys:
        - anomaly: True if value is outside the confidence interval
        - forecast: Expected value
        - lower_ci, upper_ci: Confidence interval bounds
        - value: Original value (for convenience)
        - message: Human-readable message
    """
    history = list(history)
    n = len(history)

    if n < 2:
        return {
            "anomaly": False,
            "forecast": value,
            "lower_ci": value,
            "upper_ci": value,
            "value": value,
            "message": "insufficient history",
        }

    last = history[-1]
    prev = history[-2]

    ar_correction = ar_params * (last - prev)
    ma_correction = ma_params * (last - prev)

    seasonal_correction = 0.0
    seasonal_ma_correction = 0.0
    if seasonality_period > 0 and n > seasonality_period:
        seasonal_val = history[-seasonality_period - 1]
        seasonal_correction = seasonal_ar_params * (last - seasonal_val)
        seasonal_ma_correction = seasonal_ma_params * (last - seasonal_val)

    forecast = last + ar_correction + ma_correction + seasonal_correction + seasonal_ma_correction

    z = norm_quantile((1 + confidence_level) / 2)
    half_width = z * residual_std
    lower_ci = forecast - half_width
    upper_ci = forecast + half_width

    is_anomaly = (value < lower_ci or value > upper_ci)

    if is_anomaly:
        msg = f"value={value:.4f} outside CI [{lower_ci:.4f}, {upper_ci:.4f}]"
    else:
        msg = f"value={value:.4f} inside CI [{lower_ci:.4f}, {upper_ci:.4f}]"

    return {
        "anomaly": is_anomaly,
        "forecast": float(forecast),
        "lower_ci": float(lower_ci),
        "upper_ci": float(upper_ci),
        "value": float(value),
        "message": msg,
    }
