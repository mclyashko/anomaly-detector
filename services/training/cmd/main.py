"""Training service entry point.

Wires dependencies and runs the training pipeline on a schedule.
"""

from __future__ import annotations

import logging
import os
import sys
import time
from datetime import datetime, timezone

import yaml

# Add parent directory to path so 'training' package is importable.
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from training.adapters.minio_client import MinIOStorageClient
from training.adapters.timescaledb_repository import TimescaleDBRepository
from training.core.training_pipeline import ModelConfig, TrainingOutcome, TrainingPipeline

logging.basicConfig(
    level=logging.INFO,
    format="%(asctime)s %(levelname)s %(name)s: %(message)s",
)
logger = logging.getLogger(__name__)


def load_config(config_path: str) -> dict:
    """Load YAML training configuration."""
    with open(config_path, "r") as f:
        return yaml.safe_load(f)


def run_all(timescaledb_dsn: str, minio_endpoint: str, minio_access: str, minio_secret: str, minio_bucket: str, config_path: str) -> list[TrainingOutcome]:
    """Run the training pipeline for all models in the config."""
    tsdb = TimescaleDBRepository(timescaledb_dsn)
    storage = MinIOStorageClient(minio_endpoint, minio_access, minio_secret, minio_bucket)

    with open(config_path, "r") as f:
        cfg = yaml.safe_load(f)

    outcomes: list[TrainingOutcome] = []

    for model_cfg in cfg.get("models", []):
        config = ModelConfig(
            agent_id=model_cfg["agent_id"],
            metric_name=model_cfg["metric_name"],
            model_type=model_cfg.get("model_type", "sarima"),
            training_window_days=model_cfg.get("training_window_days", 30),
            seasonality_period=model_cfg.get("seasonality_period", 1),
            confidence_level=model_cfg.get("confidence_level", 0.95),
            window_size=model_cfg.get("window_size", 48),
        )
        pipeline = TrainingPipeline(tsdb, storage, config)
        outcome = pipeline.run()
        outcomes.append(outcome)

        if outcome.success:
            logger.info(
                "✓ trained: %s version=%s n=%d aicc=%s",
                outcome.model_name,
                outcome.version,
                outcome.training_n,
                outcome.aicc,
            )
        else:
            logger.error(
                "✗ failed: agent=%s metric=%s error=%s",
                outcome.agent_id,
                outcome.metric_name,
                outcome.error,
            )

    return outcomes


def train_single(
    timescaledb_dsn: str,
    minio_endpoint: str,
    minio_access: str,
    minio_secret: str,
    minio_bucket: str,
    agent_id: str,
    metric_name: str,
    model_type: str,
    window_size: int,
    seasonality_period: int,
    training_window_days: int = 1,
    confidence_level: float = 0.95,
) -> TrainingOutcome:
    """Train a single model and upload to MinIO. Returns immediately."""
    tsdb = TimescaleDBRepository(timescaledb_dsn)
    storage = MinIOStorageClient(minio_endpoint, minio_access, minio_secret, minio_bucket)
    config = ModelConfig(
        agent_id=agent_id,
        metric_name=metric_name,
        model_type=model_type,
        training_window_days=training_window_days,
        seasonality_period=seasonality_period,
        confidence_level=confidence_level,
    )
    pipeline = TrainingPipeline(tsdb, storage, config)
    return pipeline.run()


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description="Training service")
    sub = parser.add_subparsers(dest="command", required=True)

    # Periodic training mode (default)
    serve = sub.add_parser("serve", help="Run scheduled training loop")
    serve.add_argument("--timescaledb-dsn", default=os.environ.get("TIMESCALEDBS_DSN"))
    serve.add_argument("--minio-endpoint", default=os.environ.get("MINIO_ENDPOINT"))
    serve.add_argument("--minio-access", default=os.environ.get("MINIO_ACCESS_KEY"))
    serve.add_argument("--minio-secret", default=os.environ.get("MINIO_SECRET_KEY"))
    serve.add_argument("--minio-bucket", default=os.environ.get("MINIO_BUCKET"))
    serve.add_argument("--config", default=os.environ.get("TRAINING_CONFIG"))
    serve.add_argument("--interval", default=os.environ.get("TRAINING_INTERVAL", "6h"))

    # Single-shot training mode for tests
    single = sub.add_parser("train-single", help="Train and upload one model then exit")
    single.add_argument("--timescaledb-dsn", required=True)
    single.add_argument("--minio-endpoint", required=True)
    single.add_argument("--minio-access", required=True)
    single.add_argument("--minio-secret", required=True)
    single.add_argument("--minio-bucket", required=True)
    single.add_argument("--agent-id", required=True)
    single.add_argument("--metric-name", required=True)
    single.add_argument("--model-type", default="sarima")
    single.add_argument("--window-size", type=int, default=48)
    single.add_argument("--seasonality-period", type=int, default=24)
    single.add_argument("--training-window-days", type=int, default=1)
    single.add_argument("--confidence-level", type=float, default=0.95)

    args = parser.parse_args()

    if args.command == "train-single":
        outcome = train_single(
            timescaledb_dsn=args.timescaledb_dsn,
            minio_endpoint=args.minio_endpoint,
            minio_access=args.minio_access,
            minio_secret=args.minio_secret,
            minio_bucket=args.minio_bucket,
            agent_id=args.agent_id,
            metric_name=args.metric_name,
            model_type=args.model_type,
            window_size=args.window_size,
            seasonality_period=args.seasonality_period,
            training_window_days=args.training_window_days,
            confidence_level=args.confidence_level,
        )
        if outcome.success:
            logger.info("trained: %s version=%s", outcome.model_name, outcome.version)
        else:
            logger.error("failed: %s", outcome.error)
            sys.exit(1)
        return

    # serve mode
    timescaledb_dsn = args.timescaledb_dsn or os.environ["TIMESCALEDBS_DSN"]
    minio_endpoint = args.minio_endpoint or os.environ["MINIO_ENDPOINT"]
    minio_access = args.minio_access or os.environ["MINIO_ACCESS_KEY"]
    minio_secret = args.minio_secret or os.environ["MINIO_SECRET_KEY"]
    minio_bucket = args.minio_bucket or os.environ["MINIO_BUCKET"]
    config_path = args.config or os.environ["TRAINING_CONFIG"]
    training_interval = args.interval or os.environ.get("TRAINING_INTERVAL", "6h")

    logger.info(
        "training service starting: interval=%s config=%s",
        training_interval,
        config_path,
    )

    # Run once immediately on startup.
    logger.info("=== Training run started at %s ===", datetime.now(timezone.utc).isoformat())
    outcomes = run_all(timescaledb_dsn, minio_endpoint, minio_access, minio_secret, minio_bucket, config_path)
    success_count = sum(1 for o in outcomes if o.success)
    logger.info(
        "=== Training run complete: %d/%d succeeded ===",
        success_count,
        len(outcomes),
    )

    # Schedule periodic runs.
    import schedule
    interval_seconds = _parse_interval(training_interval)
    schedule.every(interval_seconds).seconds.do(
        lambda: run_all(
            timescaledb_dsn,
            minio_endpoint,
            minio_access,
            minio_secret,
            minio_bucket,
            config_path,
        )
    )
    logger.info("scheduled training every %s", training_interval)

    # Run scheduler loop.
    while True:
        schedule.run_pending()
        time.sleep(1)


def _parse_interval(interval: str) -> int:
    """Parse interval string like '6h', '30m', '1d' to seconds."""
    interval = interval.strip().lower()
    if interval.endswith("h"):
        return int(interval[:-1]) * 3600
    if interval.endswith("m"):
        return int(interval[:-1]) * 60
    if interval.endswith("d"):
        return int(interval[:-1]) * 86400
    return int(interval)  # fallback: treat as seconds


if __name__ == "__main__":
    main()
