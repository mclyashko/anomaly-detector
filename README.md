# anomaly-detector

Microservice-based anomaly detection for time-series metrics.

## Services
- **agent** — metric collection (scrape + push)
- **ingestion** — validate, normalize, write to TimescaleDB
- **analyzer** — threshold / stats / ONNX ML / Lua rules pipeline
- **notifier** — fan-out to log / HTTP / console sinks

## Stack
Go · TimescaleDB · Redis · ONNX Runtime · gopher-lua · gRPC

## Run locally
```bash
docker compose -f infra/docker-compose.yml up
```
