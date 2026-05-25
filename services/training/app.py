"""ML Training Service — trains SARIMA models and evaluates anomalies.

Models are stored in-memory only (RAM). All inference uses proper SARIMA
via fit.extend() + get_forecast() through ModelStore.
"""

from __future__ import annotations

import asyncio
import logging
import os
from datetime import datetime, timezone
from typing import Optional

from fastapi import FastAPI, HTTPException
from contextlib import asynccontextmanager
from pydantic import BaseModel

from core.sarima_model import train_sarima
from core.model_store import ModelStore

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)

# ─────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────

ANALYZER_URL = os.getenv("ANALYZER_URL", "http://analyzer:8081")
MODEL_REFRESH_CHECK_SEC = int(os.getenv("MODEL_REFRESH_CHECK_SEC", "60"))
DB_DSN = os.getenv("DB_DSN", "")
PORT = int(os.getenv("PORT", "8085"))

# Model store — holds fitted SARIMAXResults in RAM only
_model_store = ModelStore()

# TimescaleDB repository (lazy init if DB_DSN is set)
_ts_repo = None


def _get_ts_repo():
    """Get or create the TimescaleDB repository."""
    global _ts_repo
    if _ts_repo is None and DB_DSN:
        from adapters.timescaledb_repository import TimescaleDBRepository
        _ts_repo = TimescaleDBRepository(DB_DSN)
    return _ts_repo


# ─────────────────────────────────────────────────────────────────
# Pydantic Models
# ─────────────────────────────────────────────────────────────────

class MLRuleInfo(BaseModel):
    agent_id: str
    metric: str
    train_interval_min: int
    train_data_window: int
    seasonality_period: int
    order: list[int]
    seasonal_order: list[int]


class TrainRequest(BaseModel):
    agent_id: str
    metric_name: str
    signal_data: list[float]
    seasonality_period: int = 24
    order: tuple = (1, 0, 1)
    seasonal_order: tuple = (1, 1, 1, 24)
    confidence_level: float = 0.95


class EvaluateRequest(BaseModel):
    model_id: str
    history: list[float]
    value: float


class AnomalyResponse(BaseModel):
    model_id: str
    anomaly: bool
    forecast: float
    lower_ci: float
    upper_ci: float
    value: float
    message: str


class TrainResponse(BaseModel):
    model_id: str
    status: str
    ar_params: float
    ma_params: float
    seasonal_ar_params: float
    seasonal_ma_params: float
    residual_std: float
    confidence_level: float
    seasonality_period: int
    window_size: int
    order: list[int]
    seasonal_order: list[int]
    aicc: float
    training_n: int
    message: str


# ─────────────────────────────────────────────────────────────────
# FastAPI App
# ─────────────────────────────────────────────────────────────────

def _model_id(agent_id: str, metric_name: str) -> str:
    """Generate model_id from agent_id and metric_name."""
    safe = metric_name.replace(".", "_").replace("/", "_").replace(":", "_")
    return f"{agent_id}__{safe}"


# ─────────────────────────────────────────────────────────────────
# Lifespan & App
# ─────────────────────────────────────────────────────────────────

@asynccontextmanager
async def lifespan(app: FastAPI):
    """Lifespan context manager: startup + background refresh loop."""
    if DB_DSN:
        asyncio.create_task(_refresh_stale_models_loop())
        logger.info("auto-retrain enabled: ANALYZER_URL=%s MODEL_REFRESH_CHECK_SEC=%d",
                     ANALYZER_URL, MODEL_REFRESH_CHECK_SEC)
    else:
        logger.info("auto-retrain disabled: DB_DSN not set")

    yield
    logger.info("training service shutting down")


app = FastAPI(title="ML Training Service", version="1.0.0", lifespan=lifespan)


# ─────────────────────────────────────────────────────────────────
# Routes
# ─────────────────────────────────────────────────────────────────

@app.get("/health")
def health():
    """Health check endpoint."""
    return {"status": "ok", "service": "ml-training"}


@app.post("/api/v1/train", response_model=TrainResponse)
def train(req: TrainRequest):
    """Train a SARIMA model into the in-memory store."""
    import pandas as pd

    model_id = _model_id(req.agent_id, req.metric_name)

    logger.info(
        "training model: agent=%s metric=%s seasonality=%d n=%d",
        req.agent_id,
        req.metric_name,
        req.seasonality_period,
        len(req.signal_data),
    )

    series = pd.Series(req.signal_data, dtype=float)

    entry = _model_store.train(
        agent_id=req.agent_id,
        metric_name=req.metric_name,
        timeseries=series,
        order=tuple(req.order),
        seasonal_order=tuple(req.seasonal_order),
        confidence_level=req.confidence_level,
        max_iter=200,
        window_size=max(48, req.seasonality_period + 1),
    )

    logger.info(
        "trained: model_id=%s rs=%.4f aicc=%s",
        model_id,
        entry.residual_std,
        entry.aicc,
    )

    return TrainResponse(
        model_id=model_id,
        status="trained",
        ar_params=0.0,
        ma_params=0.0,
        seasonal_ar_params=0.0,
        seasonal_ma_params=0.0,
        residual_std=entry.residual_std,
        confidence_level=entry.confidence_level,
        seasonality_period=req.seasonality_period,
        window_size=max(48, req.seasonality_period + 1),
        order=list(entry.order),
        seasonal_order=list(entry.seasonal_order),
        aicc=entry.aicc if entry.aicc else 0.0,
        training_n=entry.training_n,
        message=f"trained on {entry.training_n} points, aicc={entry.aicc:.2f}" if entry.aicc else f"trained on {entry.training_n} points",
    )


@app.post("/api/v1/evaluate", response_model=AnomalyResponse)
def evaluate(req: EvaluateRequest):
    """Check whether a value is anomalous using the in-memory model."""
    entry = _model_store.get(req.model_id)
    if entry is None:
        raise HTTPException(status_code=404, detail=f"Model not found: {req.model_id}")

    try:
        result = _model_store.evaluate(req.model_id, req.history, req.value)
    except ValueError as e:
        raise HTTPException(
            status_code=400,
            detail=f"insufficient history for evaluation: {e}",
        )

    return AnomalyResponse(model_id=req.model_id, **result)


@app.get("/api/v1/models")
def list_models():
    """List all models currently in memory."""
    return {
        "models": [
            {
                "model_id": mid,
                "agent_id": _model_store.get(mid).agent_id,
                "metric_name": _model_store.get(mid).metric_name,
                "order": list(_model_store.get(mid).order),
                "seasonal_order": list(_model_store.get(mid).seasonal_order),
                "seasonality_period": _model_store.get(mid).seasonality_period,
                "confidence_level": _model_store.get(mid).confidence_level,
                "trained_at": _model_store.get(mid).trained_at.isoformat(),
                "training_n": _model_store.get(mid).training_n,
                "aicc": _model_store.get(mid).aicc,
                "residual_std": _model_store.get(mid).residual_std,
                "window_size": _model_store.get(mid).window_size,
            }
            for mid in _model_store.list_model_ids()
        ]
    }


@app.get("/api/v1/models/{model_id}")
def get_model(model_id: str):
    """Get model parameters from memory."""
    entry = _model_store.get(model_id)
    if entry is None:
        raise HTTPException(status_code=404, detail=f"Model not found: {model_id}")
    return {
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
    }


@app.get("/api/v1/ml-rules", response_model=list[MLRuleInfo])
def get_ml_rules():
    """Fetch ML rules from analyzer for debugging/inspection."""
    import requests
    try:
        resp = requests.get(f"{ANALYZER_URL}/api/v1/ml-rules", timeout=10)
        resp.raise_for_status()
        return resp.json()
    except requests.RequestException as e:
        logger.warning("failed to fetch ml-rules from analyzer: %s", e)
        raise HTTPException(status_code=502, detail=f"Analyzer unavailable: {e}")


# ─────────────────────────────────────────────────────────────────
# Auto-retrain background task
# ─────────────────────────────────────────────────────────────────

async def _refresh_stale_models_loop():
    """Background task: periodically checks models for staleness and retrains."""
    while True:
        await asyncio.sleep(MODEL_REFRESH_CHECK_SEC)
        try:
            rules = await asyncio.to_thread(_fetch_ml_rules_from_analyzer)
            if not rules:
                continue

            repo = _get_ts_repo()
            if repo is None:
                continue

            for rule in rules:
                model_id = _model_id(rule.agent_id, rule.metric)
                entry = _model_store.get(model_id)
                last_trained = entry.trained_at if entry else None

                if last_trained is None or _is_stale(last_trained, rule.train_interval_min):
                    data = repo.fetch_recent(rule.agent_id, rule.metric, rule.train_data_window)
                    if len(data) < rule.train_data_window:
                        logger.warning("insufficient data for training: got %d, want %d",
                                       len(data), rule.train_data_window)
                        continue
                    _train_model_locally(model_id, rule, data)
                else:
                    logger.info("model fresh: model_id=%s", model_id)

        except (OSError, ConnectionError):
            logger.exception("failed to refresh stale models")


def _fetch_ml_rules_from_analyzer() -> list[MLRuleInfo]:
    """Fetch ML rules configuration from analyzer."""
    import requests
    try:
        resp = requests.get(f"{ANALYZER_URL}/api/v1/ml-rules", timeout=10)
        resp.raise_for_status()
        return [MLRuleInfo(**r) for r in resp.json()]
    except requests.RequestException as e:
        logger.warning("failed to fetch ml-rules from analyzer: %s", e)
        return []


def _is_stale(last_trained: datetime, train_interval_min: int) -> bool:
    """Check if a model is stale based on its last training time."""
    age = datetime.now(timezone.utc) - last_trained
    return age.total_seconds() > train_interval_min * 60


def _train_model_locally(model_id: str, rule: MLRuleInfo, data: list[float]):
    """Train a SARIMA model into the in-memory store."""
    import pandas as pd

    logger.info("training model: model_id=%s seasonality=%d n=%d",
                model_id, rule.seasonality_period, len(data))

    series = pd.Series(data, dtype=float)

    entry = _model_store.train(
        agent_id=rule.agent_id,
        metric_name=rule.metric,
        timeseries=series,
        order=tuple(rule.order),
        seasonal_order=tuple(rule.seasonal_order),
        confidence_level=0.95,
        max_iter=200,
        window_size=max(48, rule.seasonality_period + 1),
    )

    logger.info("trained: model_id=%s rs=%.4f aicc=%s",
                model_id, entry.residual_std, entry.aicc)


# ─────────────────────────────────────────────────────────────────
# Entry Point
# ─────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=PORT)