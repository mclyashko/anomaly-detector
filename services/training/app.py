"""ML Training Service — trains SARIMA models and evaluates anomalies.

The service operates in two modes:
1. HTTP API for training and inference (called by analyzer)
2. Auto-retrain mode — periodically checks models for staleness and retrains
   if data has changed

Workflow:
- On training request, fit statsmodels SARIMAX on the provided signal data
- SARIMAX(1,0,1)(1,1,1,S) — seasonal ARIMA with one AR and one MA coefficient
- On inference: compute a forecast, compare against the confidence interval
- If value is outside CI — anomaly

Models are stored as JSON files in MODELS_DIR: {model_id}/metadata.json
"""

from __future__ import annotations

import asyncio
import json
import logging
import os
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Optional

from fastapi import FastAPI, HTTPException
from contextlib import asynccontextmanager
from pydantic import BaseModel

from infer import evaluate_anomaly
from core.sarima_model import train_sarima

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)

# ─────────────────────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────────────────────

MODELS_DIR = Path(os.getenv("MODELS_DIR", "./models"))
MODELS_DIR.mkdir(exist_ok=True)

ANALYZER_URL = os.getenv("ANALYZER_URL", "http://analyzer:8081")
MODEL_REFRESH_CHECK_SEC = int(os.getenv("MODEL_REFRESH_CHECK_SEC", "60"))
DB_DSN = os.getenv("DB_DSN", "")
PORT = int(os.getenv("PORT", "8085"))
LOG_LEVEL = os.getenv("LOG_LEVEL", "info")

# In-memory cache for quick access
_model_cache: dict[str, dict] = {}

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


def _load_model_from_disk(model_id: str) -> Optional[dict]:
    """Load model metadata from disk."""
    model_path = MODELS_DIR / model_id / "metadata.json"
    if not model_path.exists():
        return None
    with open(model_path) as f:
        return json.load(f)


def _save_model_to_disk(model_id: str, metadata: dict) -> None:
    """Save model metadata to disk."""
    model_path = MODELS_DIR / model_id
    model_path.mkdir(exist_ok=True, parents=True)
    with open(model_path / "metadata.json", "w") as f:
        json.dump(metadata, f, indent=2)


# ─────────────────────────────────────────────────────────────────
# Lifespan & App
# ─────────────────────────────────────────────────────────────────

@asynccontextmanager
async def lifespan(app: FastAPI):
    """Lifespan context manager: startup + background refresh loop."""
    load_existing_models()

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
    """Train a SARIMA model on the provided data and persist its parameters.

    Uses train_sarima() from core/sarima_model.py — single unified implementation.
    """
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

    result = train_sarima(
        timeseries=series,
        seasonality_period=req.seasonality_period,
        order=tuple(req.order),
        seasonal_order=tuple(req.seasonal_order),
        confidence_level=req.confidence_level,
        max_iter=200,
        window_size=max(48, req.seasonality_period + 1),
    )

    params = result.params

    metadata = {
        "model_id": model_id,
        "agent_id": req.agent_id,
        "metric_name": req.metric_name,
        "ar_params": params["ar_params"],
        "ma_params": params["ma_params"],
        "seasonal_ar_params": params["seasonal_ar_params"],
        "seasonal_ma_params": params["seasonal_ma_params"],
        "residual_std": params["residual_std"],
        "confidence_level": result.confidence_level,
        "seasonality_period": req.seasonality_period,
        "window_size": max(48, req.seasonality_period + 1),
        "order": result.order,
        "seasonal_order": result.seasonal_order,
        "trained_at": datetime.now(timezone.utc).isoformat(),
        "aicc": result.aicc,
        "d": params["d"],
        "seasonal_d": params["seasonal_d"],
        "sigma2": params.get("sigma2"),
        "training_n": result.training_n,
        "training_start": result.training_start.isoformat() if result.training_start else None,
        "training_end": result.training_end.isoformat() if result.training_end else None,
    }

    _save_model_to_disk(model_id, metadata)
    _model_cache[model_id] = metadata

    msg = f"trained on {result.training_n} points, aicc={result.aicc:.2f}" if result.aicc else f"trained on {result.training_n} points"

    logger.info(
        "trained: model_id=%s ar=%.4f sar=%.4f rs=%.4f aicc=%s",
        model_id,
        params["ar_params"],
        params["seasonal_ar_params"],
        params["residual_std"],
        result.aicc,
    )

    return TrainResponse(
        model_id=model_id,
        status="trained",
        ar_params=params["ar_params"],
        ma_params=params["ma_params"],
        seasonal_ar_params=params["seasonal_ar_params"],
        seasonal_ma_params=params["seasonal_ma_params"],
        residual_std=params["residual_std"],
        confidence_level=result.confidence_level,
        seasonality_period=req.seasonality_period,
        window_size=max(48, req.seasonality_period + 1),
        order=list(result.order),
        seasonal_order=list(result.seasonal_order),
        aicc=result.aicc if result.aicc else 0.0,
        training_n=result.training_n,
        message=msg,
    )


@app.post("/api/v1/evaluate", response_model=AnomalyResponse)
def evaluate(req: EvaluateRequest):
    """Check whether a value is anomalous relative to a trained model.

    Logic:
    1. Look up the model — first in memory (_model_cache), then on disk
    2. Call evaluate_anomaly() from infer.py — inference logic lives there
    3. Return JSON with anomaly flag, forecast, and confidence interval bounds

    History must be at least seasonality_period + 1 points.
    Otherwise the model returns "insufficient history".
    """
    # Try cache first, then disk
    meta = _model_cache.get(req.model_id)
    if meta is None:
        meta = _load_model_from_disk(req.model_id)
        if meta is None:
            raise HTTPException(status_code=404, detail=f"Model not found: {req.model_id}")
        _model_cache[req.model_id] = meta

    result = evaluate_anomaly(
        history=req.history,
        value=req.value,
        ar_params=meta["ar_params"],
        ma_params=meta["ma_params"],
        seasonal_ar_params=meta["seasonal_ar_params"],
        seasonal_ma_params=meta["seasonal_ma_params"],
        residual_std=meta["residual_std"],
        seasonality_period=meta["seasonality_period"],
        confidence_level=meta.get("confidence_level", 0.95),
    )

    return AnomalyResponse(
        model_id=req.model_id,
        **result,
    )


@app.get("/api/v1/models")
def list_models():
    """List all trained models."""
    models = []
    if not MODELS_DIR.exists():
        return {"models": models}

    for path in MODELS_DIR.iterdir():
        if path.is_dir():
            meta_path = path / "metadata.json"
            if meta_path.exists():
                with open(meta_path) as f:
                    models.append(json.load(f))
    return {"models": models}


@app.get("/api/v1/models/{model_id}")
def get_model(model_id: str):
    """Get model parameters."""
    meta = _model_cache.get(model_id)
    if meta is None:
        meta = _load_model_from_disk(model_id)
        if meta is None:
            raise HTTPException(status_code=404, detail=f"Model not found: {model_id}")
    return meta


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
# Startup helpers (called from lifespan)
# ─────────────────────────────────────────────────────────────────

def load_existing_models():
    """Pre-load all existing models into cache."""
    if not MODELS_DIR.exists():
        logger.info("models directory does not exist, starting fresh")
        return

    count = 0
    for path in MODELS_DIR.iterdir():
        if path.is_dir():
            meta_path = path / "metadata.json"
            if meta_path.exists():
                with open(meta_path) as f:
                    meta = json.load(f)
                _model_cache[meta["model_id"]] = meta
                count += 1
    logger.info("loaded %d existing models into cache", count)


# ─────────────────────────────────────────────────────────────────
# Auto-retrain background task
# ─────────────────────────────────────────────────────────────────

async def _refresh_stale_models_loop():
    """Background task: periodically checks models for staleness and retrains.

    Logic:
    1. Every MODEL_REFRESH_CHECK_SEC seconds, fetch ML rules from analyzer
    2. For each rule, check when the model was last trained
    3. If more than train_interval_min minutes have passed, retrain on fresh data from TimescaleDB
    4. Need at least 75% of train_data_window points for training

    A model is "stale" when:
    - It has never been trained (last_trained = None)
    - More than train_interval_min minutes have passed since last training
    """
    import requests

    while True:
        await asyncio.sleep(MODEL_REFRESH_CHECK_SEC)
        try:
            rules = await asyncio.to_thread(_fetch_ml_rules_from_analyzer)
            logger.debug("fetched %d rules from analyzer", len(rules))
            if not rules:
                logger.info("no ML rules found in analyzer")
                continue

            repo = _get_ts_repo()
            if repo is None:
                logger.warning("DB_DSN not set, cannot fetch training data")
                continue

            for rule in rules:
                model_id = _model_id(rule.agent_id, rule.metric)
                last_trained = _model_last_trained(model_id)
                if last_trained is None or _is_stale(last_trained, rule.train_interval_min):
                    logger.info("model stale, retraining: model_id=%s interval_min=%d",
                                model_id, rule.train_interval_min)
                    data = repo.fetch_recent(rule.agent_id, rule.metric, rule.train_data_window)
                    logger.debug("fetch_recent returned %d points for %s/%s",
                                len(data), rule.agent_id, rule.metric)
                    # Need at least 75% of the training window for a meaningful model.
                    if len(data) < rule.train_data_window * 3 // 4:
                        logger.warning("insufficient data for training: got %d, want %d",
                                       len(data), rule.train_data_window)
                        continue
                    _train_model_locally(model_id, rule, data)
                else:
                    logger.info("model fresh: model_id=%s last_trained=%s", model_id, last_trained)

        except (OSError, ConnectionError) as exc:
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


def _model_last_trained(model_id: str) -> Optional[str]:
    """Get last_trained timestamp from model metadata, or None if not found."""
    meta = _model_cache.get(model_id)
    if meta is None:
        meta = _load_model_from_disk(model_id)
    if meta is None:
        return None
    return meta.get("trained_at")


def _is_stale(last_trained: str, train_interval_min: int) -> bool:
    """Check if a model is stale based on its last training time."""
    try:
        trained = datetime.fromisoformat(last_trained.replace("Z", "+00:00"))
        age = datetime.now(timezone.utc) - trained
        return age.total_seconds() > train_interval_min * 60
    except (ValueError, AttributeError):
        # ISO format mismatch — treat as stale so it gets retrained
        return True


def _train_model_locally(model_id: str, rule: MLRuleInfo, data: list[float]):
    """Train a SARIMA model and store it locally."""
    import pandas as pd

    logger.info("training model: model_id=%s seasonality=%d n=%d",
                model_id, rule.seasonality_period, len(data))

    series = pd.Series(data, dtype=float)

    result = train_sarima(
        timeseries=series,
        seasonality_period=rule.seasonality_period,
        order=tuple(rule.order),
        seasonal_order=tuple(rule.seasonal_order),
        confidence_level=0.95,
        max_iter=200,
        window_size=max(48, rule.seasonality_period + 1),
    )

    params = result.params

    metadata = {
        "model_id": model_id,
        "agent_id": rule.agent_id,
        "metric_name": rule.metric,
        "ar_params": params["ar_params"],
        "ma_params": params["ma_params"],
        "seasonal_ar_params": params["seasonal_ar_params"],
        "seasonal_ma_params": params["seasonal_ma_params"],
        "residual_std": params["residual_std"],
        "confidence_level": result.confidence_level,
        "seasonality_period": rule.seasonality_period,
        "window_size": max(48, rule.seasonality_period + 1),
        "order": result.order,
        "seasonal_order": result.seasonal_order,
        "trained_at": datetime.now(timezone.utc).isoformat(),
        "aicc": result.aicc,
        "d": params["d"],
        "seasonal_d": params["seasonal_d"],
        "sigma2": params.get("sigma2"),
        "training_n": result.training_n,
        "training_start": result.training_start.isoformat() if result.training_start else None,
        "training_end": result.training_end.isoformat() if result.training_end else None,
    }

    _save_model_to_disk(model_id, metadata)
    _model_cache[model_id] = metadata

    logger.info("trained: model_id=%s ar=%.4f sar=%.4f rs=%.4f aicc=%s",
                model_id, params["ar_params"], params["seasonal_ar_params"],
                params["residual_std"], result.aicc)


# ─────────────────────────────────────────────────────────────────
# Entry Point
# ─────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=PORT)
