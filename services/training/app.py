"""ML Training Service — обучает SARIMA модели и оценивает аномалии.

Сервис работает в двух режимах:
1. HTTP API для обучения и инференса (вызывается analyzer-ом)
2. Автоматический режим — периодически проверяет свои модели на актуальность
   и переобучает их если данные изменились (режим «auto-retrain»)

Принцип работы:
- При поступлении данных на обучение, модель.fit() на statsmodels SARIMAX
- SARIMAX(1,0,1)(1,1,1,S) — упрощённая сезонная ARIMA с одним AR и MA коэффициентом
- На инференсе: вычисляем прогноз по формуле, сравниваем с доверительным интервалом
- Если значение за пределами CI — аномалия

Models хранятся как JSON-файлы в MODELS_DIR: {model_id}/metadata.json
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
    """Обучает SARIMA модель на переданных данных и сохраняет параметры.

    Процесс:
    1. Формируем model_id = "{agent_id}__{safe_metric_name}"
    2. Обучаем SARIMAX с заданным порядком (по умолчанию (1,0,1)(1,1,1,S))
    3. Извлекаем коэффициенты: ar, ma, seasonal_ar, seasonal_ma и residual_std
    4. Сохраняем metadata.json в MODELS_DIR/{model_id}/

    statsmodels SARIMAX возвращает массив params, где:
    - params[0] = ar_params (AR(1))
    - params[1] = ma_params (MA(1))
    - params[2] = seasonal_ar_params (SAR(1))
    - params[3] = seasonal_ma_params (SMA(1))
    """
    from statsmodels.tsa.statespace.sarimax import SARIMAX
    import numpy as np

    model_id = _model_id(req.agent_id, req.metric_name)

    logger.info(
        "training model: agent=%s metric=%s seasonality=%d n=%d",
        req.agent_id,
        req.metric_name,
        req.seasonality_period,
        len(req.signal_data),
    )

    series = np.array(req.signal_data)

    # Fit SARIMA model
    model = SARIMAX(
        series,
        order=req.order,
        seasonal_order=req.seasonal_order,
        enforce_stationarity=False,
        enforce_invertibility=False,
    )
    fit = model.fit(disp=False)

    # Extract parameters — statsmodels returns numpy arrays for SARIMAX
    params = fit.params
    ar_params = float(params[0])
    ma_params = float(params[1])
    seasonal_ar = float(params[2])
    seasonal_ma = float(params[3])
    residual_std = float(np.std(fit.resid))

    # Build metadata
    metadata = {
        "model_id": model_id,
        "agent_id": req.agent_id,
        "metric_name": req.metric_name,
        "ar_params": ar_params,
        "ma_params": ma_params,
        "seasonal_ar_params": seasonal_ar,
        "seasonal_ma_params": seasonal_ma,
        "residual_std": residual_std,
        "confidence_level": req.confidence_level,
        "seasonality_period": req.seasonality_period,
        "window_size": max(48, req.seasonality_period + 1),
        "order": req.order,
        "seasonal_order": req.seasonal_order,
        "trained_at": datetime.now(timezone.utc).isoformat(),
    }

    # Save to disk
    _save_model_to_disk(model_id, metadata)

    # Cache in memory
    _model_cache[model_id] = metadata

    logger.info(
        "trained: model_id=%s ar=%.4f sar=%.4f rs=%.4f",
        model_id,
        ar_params,
        seasonal_ar,
        residual_std,
    )

    return TrainResponse(
        model_id=model_id,
        status="trained",
        ar_params=ar_params,
        ma_params=ma_params,
        seasonal_ar_params=seasonal_ar,
        seasonal_ma_params=seasonal_ma,
        residual_std=residual_std,
        message=f"Model trained and saved to {MODELS_DIR / model_id}",
    )


@app.post("/api/v1/evaluate", response_model=AnomalyResponse)
def evaluate(req: EvaluateRequest):
    """Проверяет одно значение на аномальность относительно обученной модели.

    Логика:
    1. Ищем модель — сначала в памяти (_model_cache), потом на диске
    2. Вызываем evaluate_anomaly() из infer.py — основная логика там
    3. Возвращаем JSON с флагом anomaly, прогнозом и доверительным интервалом

    История (history) должна быть достаточно длинной — хотя бы seasonality_period + 1 точек.
    Иначе model вернёт insufficient history.
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
    except Exception as e:
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
    """Фоновая задача: периодически проверяет модели на актуальность и переобучает.

    Логика:
    1. Раз в MODEL_REFRESH_CHECK_SEC секунд запрашивает ML-правила у analyzer-а
    2. Для каждого правила проверяет — когда последний раз обучалась модель
    3. Если прошло больше train_interval_min минут — переобучает на свежих данных из TimescaleDB
    4. Нужно минимум 3/4 от train_data_window точек для обучения

    Модель считается «stale» (устаревшей), если:
    - Ещё ни разу не обучалась (last_trained = None)
    - Прошло больше train_interval_min минут с последнего обучения
    """
    import requests

    while True:
        await asyncio.sleep(MODEL_REFRESH_CHECK_SEC)
        try:
            rules = _fetch_ml_rules_from_analyzer()
            logger.info(f"[DEBUG] fetched {len(rules)} rules from analyzer")
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
                    logger.info(f"[DEBUG] fetch_recent returned {len(data)} points for {rule.agent_id}/{rule.metric}")
                    # Нужно достаточно данных для有意义ной модели — хотя бы 75% от желаемого окна
                    if len(data) < rule.train_data_window * 3 // 4:
                        logger.warning("insufficient data for training: got %d, want %d",
                                       len(data), rule.train_data_window)
                        continue
                    _train_model_locally(model_id, rule, data)
                else:
                    logger.info(f"model fresh: model_id={model_id} last_trained={last_trained}")

        except Exception:
            logger.exception("failed to refresh stale models")


def _fetch_ml_rules_from_analyzer() -> list[MLRuleInfo]:
    """Fetch ML rules configuration from analyzer."""
    import requests
    try:
        resp = requests.get(f"{ANALYZER_URL}/api/v1/ml-rules", timeout=10)
        resp.raise_for_status()
        return [MLRuleInfo(**r) for r in resp.json()]
    except Exception as e:
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
    except Exception:
        return True  # If we can't parse, consider it stale


def _train_model_locally(model_id: str, rule: MLRuleInfo, data: list[float]):
    """Train a SARIMA model and store it locally."""
    from statsmodels.tsa.statespace.sarimax import SARIMAX
    import numpy as np

    logger.info("training model: model_id=%s seasonality=%d n=%d",
                model_id, rule.seasonality_period, len(data))

    series = np.array(data)
    model = SARIMAX(
        series,
        order=tuple(rule.order),
        seasonal_order=tuple(rule.seasonal_order),
        enforce_stationarity=False,
        enforce_invertibility=False,
    )
    fit = model.fit(disp=False)

    params = fit.params
    ar_params = float(params[0])
    ma_params = float(params[1])
    seasonal_ar = float(params[2])
    seasonal_ma = float(params[3])
    residual_std = float(np.std(fit.resid))

    metadata = {
        "model_id": model_id,
        "agent_id": rule.agent_id,
        "metric_name": rule.metric,
        "ar_params": ar_params,
        "ma_params": ma_params,
        "seasonal_ar_params": seasonal_ar,
        "seasonal_ma_params": seasonal_ma,
        "residual_std": residual_std,
        "confidence_level": 0.95,
        "seasonality_period": rule.seasonality_period,
        "window_size": max(48, rule.seasonality_period + 1),
        "order": rule.order,
        "seasonal_order": rule.seasonal_order,
        "trained_at": datetime.now(timezone.utc).isoformat(),
    }

    _save_model_to_disk(model_id, metadata)
    _model_cache[model_id] = metadata

    logger.info("trained: model_id=%s ar=%.4f sar=%.4f rs=%.4f",
                model_id, ar_params, seasonal_ar, residual_std)


# ─────────────────────────────────────────────────────────────────
# Entry Point
# ─────────────────────────────────────────────────────────────────

if __name__ == "__main__":
    import uvicorn

    port = int(os.getenv("PORT", "8085"))
    uvicorn.run(app, host="0.0.0.0", port=port)
