"""Training orchestration pipeline.

For each (agent_id, metric_name, model_type) in the config:
    1. Fetch historical time series from TimescaleDB
    2. Train SARIMA model
    3. Determine next version (v1, v2, ...)
    4. Export to ONNX
    5. Upload to MinIO with metadata
    6. Log outcome
"""

from __future__ import annotations

import logging
from dataclasses import dataclass
from datetime import datetime, timezone
from typing import TYPE_CHECKING

from . import naming
from .model_export import export_sarima_to_onnx
from .sarima_model import train_sarima

if TYPE_CHECKING:
    from ..ports.storage_port import ModelStoragePort
    from ..ports.timescaledb_port import TimescaleDBPort

logger = logging.getLogger(__name__)


@dataclass
class ModelConfig:
    """Configuration for a single model training."""

    agent_id: str
    metric_name: str
    model_type: str
    training_window_days: int
    seasonality_period: int
    confidence_level: float = 0.95
    window_size: int = 48


@dataclass
class TrainingOutcome:
    """Outcome of a single model training run."""

    agent_id: str
    metric_name: str
    model_type: str
    version: str
    model_name: str
    success: bool
    error: str | None = None
    training_n: int = 0
    aicc: float | None = None


class TrainingPipeline:
    """Orchestrates fetching data, training, and uploading a model."""

    def __init__(
        self,
        timescaledb: "TimescaleDBPort",
        storage: "ModelStoragePort",
        config: ModelConfig,
    ) -> None:
        self._tsdb = timescaledb
        self._storage = storage
        self._config = config

    def run(self) -> TrainingOutcome:
        """Execute the full training pipeline for this model's config.

        Returns:
            TrainingOutcome describing the result.
        """
        cfg = self._config
        logger.info(
            "starting pipeline: agent=%s metric=%s type=%s",
            cfg.agent_id,
            cfg.metric_name,
            cfg.model_type,
        )

        try:
            # Step 1: Fetch historical data.
            series = self._tsdb.fetch_metrics(
                agent_id=cfg.agent_id,
                metric_name=cfg.metric_name,
                days=cfg.training_window_days,
            )
            if len(series) == 0:
                return TrainingOutcome(
                    agent_id=cfg.agent_id,
                    metric_name=cfg.metric_name,
                    model_type=cfg.model_type,
                    version="",
                    model_name="",
                    success=False,
                    error=f"no data found for agent_id={cfg.agent_id}, metric_name={cfg.metric_name}",
                )

            logger.info(
                "fetched %d points for %s/%s",
                len(series),
                cfg.agent_id,
                cfg.metric_name,
            )

            # Step 2: Train model.
            if cfg.model_type == "sarima":
                result = train_sarima(
                    timeseries=series,
                    seasonality_period=cfg.seasonality_period,
                    confidence_level=cfg.confidence_level,
                    window_size=cfg.window_size,
                )
            else:
                return TrainingOutcome(
                    agent_id=cfg.agent_id,
                    metric_name=cfg.metric_name,
                    model_type=cfg.model_type,
                    version="",
                    model_name="",
                    success=False,
                    error=f"unknown model type: {cfg.model_type}",
                )

            # Step 3: Determine next version.
            latest = self._storage.latest_version(
                agent_id=cfg.agent_id,
                metric_name=cfg.metric_name,
                model_type=cfg.model_type,
            )
            version = _next_version(latest)
            model_full_name = naming.model_name(
                cfg.agent_id, cfg.metric_name, cfg.model_type, version
            )

            logger.info(
                "uploading model %s (version=%s, aicc=%s, n=%d)",
                model_full_name,
                version,
                result.aicc,
                result.training_n,
            )

            # Step 4: Export to ONNX.
            onnx_bytes = export_sarima_to_onnx(result.params, window_size=cfg.window_size)

            # Step 5: Build metadata.
            # Этот dict сохраняется как metadata.json рядом с ONNX моделью в MinIO.
            # Все поля кроме SARIMA params также записываются в TimescaleDB при обучении.
            metadata = {
                # Идентификация модели
                "agent_id": cfg.agent_id,
                "metric_name": cfg.metric_name,
                "model_type": cfg.model_type,
                "version": version,
                "model_name": model_full_name,
                # Времена обучения
                "training_timestamp": datetime.now(timezone.utc).isoformat(),
                "training_window_days": cfg.training_window_days,
                "training_n": result.training_n,
                "training_start": (
                    result.training_start.isoformat() if result.training_start else None
                ),
                "training_end": (
                    result.training_end.isoformat() if result.training_end else None
                ),
                # SARIMA config
                "seasonality_period": cfg.seasonality_period,
                "confidence_level": cfg.confidence_level,
                "aicc": result.aicc,
                "order": result.order,
                "seasonal_order": result.seasonal_order,
                # SARIMA параметры — критичны для Go-side инференса в infer.py.
                # ar_params, ma_params: краткосрочная авторегрессия и скользящее среднее.
                # seasonal_ar_params, seasonal_ma_params: сезонная коррекция (реагирует на S периодов назад).
                # residual_std: std остатков модели — определяет ширину доверительного интервала.
                "ar_params": result.params.get("ar_params"),
                "ma_params": result.params.get("ma_params"),
                "seasonal_ar_params": result.params.get("seasonal_ar_params"),
                "seasonal_ma_params": result.params.get("seasonal_ma_params"),
                "residual_std": result.params.get("residual_std"),
                # d, seasonal_d: порядки разностей (d=1 значит ряд был один раз продифференцирован).
                "d": result.params.get("d"),
                "seasonal_d": result.params.get("seasonal_d"),
                "window_size": result.params.get("window_size", 48),
            }

            # Step 6: Upload.
            path = self._storage.upload(model_full_name, onnx_bytes, metadata)
            logger.info("model uploaded: %s", path)

            return TrainingOutcome(
                agent_id=cfg.agent_id,
                metric_name=cfg.metric_name,
                model_type=cfg.model_type,
                version=version,
                model_name=model_full_name,
                success=True,
                training_n=result.training_n,
                aicc=result.aicc,
            )

        except Exception as exc:  # noqa: BLE001
            logger.exception(
                "pipeline failed: agent=%s metric=%s",
                cfg.agent_id,
                cfg.metric_name,
            )
            return TrainingOutcome(
                agent_id=cfg.agent_id,
                metric_name=cfg.metric_name,
                model_type=cfg.model_type,
                version="",
                model_name="",
                success=False,
                error=str(exc),
            )


def _next_version(latest: str | None) -> str:
    """Determine the next semantic version given the latest existing version.

    Examples:
        None       -> "v1"
        "v1"       -> "v2"
        "v9"       -> "v10"
        "v1-beta"  -> "v2" (strip non-numeric suffix)
    """
    if latest is None:
        return "v1"

    # Strip non-numeric suffix (e.g. "v1-beta" -> "v1").
    import re
    m = re.search(r"v(\d+)", latest)
    if not m:
        return "v1"
    n = int(m.group(1))
    return f"v{n + 1}"
