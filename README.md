# anomaly-detector

Microservice система для сбора time-series метрик, обнаружения аномалий и управления инцидентами.

---

## Архитектура

```
┌────────────────────────────────────────────────────────────────────────────────┐
│                           METRIC FLOW                                           │
│                                                                                 │
│  ┌─────────────┐         ┌─────────────┐         ┌─────────────────────────┐  │
│  │             │  GET    │             │         │                         │  │
│  │ Fake-Service│◀────────│   Agent     │         │   Kafka                 │  │
│  │  :8090      │ /metrics│   :8084    │         │   Topic: metrics        │  │
│  │             │         │             │────────▶│                         │  │
│  │ (HTTP scrap │         │ Collectors: │  Kafka  │                         │  │
│  │  /metrics)  │         │  - System   │         └───────────┬─────────────┘  │
│  └─────────────┘         │  - HTTP     │                   │                  │
│                          └──────┬──────┘                   │                  │
│                                 │ HTTP POST                  │                  │
│                                 │ (fallback)                 │                  │
│                                 ▼                           ▼                  │
│                    ┌─────────────────────────────────────────────────┐        │
│                    │                  Ingestion                       │        │
│                    │                   :8080                         │        │
│                    │                                                   │        │
│                    │  HTTP API: POST /api/v1/ingest                   │        │
│                    │  Kafka Consumer (optional): topic=metrics         │        │
│                    │  Storage: TimescaleDB                            │        │
│                    └───────────────────────┬─────────────────────────┘        │
│                                            │                                    │
└────────────────────────────────────────────┼────────────────────────────────────┘
                                             │
                                             │ Pull (FOR UPDATE SKIP LOCKED)
                                             ▼
┌────────────────────────────────────────────────────────────────────────────────┐
│                           ANALYZER                                             │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────┐         │
│  │  TimescaleDB: SELECT ... WHERE analyzed_at IS NULL                 │         │
│  │  FOR UPDATE SKIP LOCKED                                          │         │
│  └────────────────────────────────────┬─────────────────────────────┘         │
│                                       │                                          │
│  ┌────────────────────────────────────▼─────────────────────────────┐         │
│  │  RuleEngine: O(1) lookup по имени метрики                        │         │
│  │  Threshold rules: value > 0.8                                     │         │
│  │  Lua rules: custom logic в sandbox                                 │         │
│  │  ML rules: (stub)                                                 │         │
│  └────────────────────────────────────┬─────────────────────────────┘         │
│                                       │ Anomalies                         │
│                                       ▼                                    │
│  ┌──────────────────────────────────────────────────────────────────┐         │
│  │  NotifierClient: HTTP POST /api/v1/notifications                  │         │
│  └────────────────────────────────────┬─────────────────────────────┘         │
└───────────────────────────────────────┼─────────────────────────────────────┘
                                        │ HTTP POST
                                        ▼
┌────────────────────────────────────────────────────────────────────────────────┐
│                           NOTIFIER                                             │
│                                                                                 │
│  ┌──────────────────────────────────────────────────────────────────┐         │
│  │  Incident Lifecycle: OPEN → UPDATED → ESCALATED → RESOLVED        │         │
│  │  Deduplication: UNIQUE(rule, service, metric)                    │         │
│  │  Storage: PostgreSQL                                              │         │
│  └────────────────────────────────────┬─────────────────────────────┘         │
│                                       │                                         │
│                                       ▼                                         │
│  ┌──────────────────────────────────────────────────────────────────┐         │
│  │  REST API: /api/v1/incidents                                     │         │
│  │  Web UI: /ui/incidents                                           │         │
│  │  Channels: LogChannel (slog)                                     │         │
│  └──────────────────────────────────────────────────────────────────┘         │
└────────────────────────────────────────────────────────────────────────────────┘
```

### PUSH vs PULL

| Этап | Модель | Протокол | Описание |
|------|--------|----------|----------|
| Agent → Ingestion | **PUSH** | HTTP POST или Kafka | Метрики отправляются сразу |
| Ingestion → TimescaleDB | PUSH | SQL INSERT | Сохранение метрик |
| Analyzer → TimescaleDB | **PULL** | SQL SELECT ... FOR UPDATE SKIP LOCKED | Polling необработанных метрик |
| Analyzer → Notifier | **PUSH** | HTTP POST | Отправка аномалий |
| Notifier → PostgreSQL | PUSH | SQL CRUD | Управление инцидентами |

### High Availability

| Сервис | Масштабирование | Механизм |
|--------|---------------|----------|
| Agent | N × | Независимые инстансы, разные `AGENT_ID` |
| Ingestion (HTTP) | N × | Независимые инстансы, один TimescaleDB |
| Ingestion (Kafka) | N × | Consumer group: каждый инстанс читает разные партиции |
| Analyzer | N × | `FOR UPDATE SKIP LOCKED` — каждый берёт разные метрики |
| Notifier | N × | PostgreSQL + `UNIQUE` constraint |

---

## Сервисы

| Сервис | Порт | Описание |
|---------|------|----------|
| `fake-service` | :8090 | Тестовый генератор метрик (CPU-bound воркеры) |
| `agent` | — | Собирает метрики, отправляет в Ingestion |
| `ingestion` | :8080 | HTTP API + Kafka consumer → TimescaleDB |
| `analyzer` | :8081 | Polling TimescaleDB, rule evaluation |
| `notifier` | :8082 | Incident lifecycle + Web UI |
| `TimescaleDB` | :5432 | Хранение метрик |
| `PostgreSQL` (notifier) | :5433 | Хранение инцидентов |
| `Kafka` | :9092 | Message queue |

---

## Быстрый старт

```bash
# Все сервисы через Docker Compose
docker compose -f infra/docker-compose.yml up --build

# Или каждый руками
cd services/{service} && go run ./cmd/
```

---

## Конфигурация

### agent

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `AGENT_ID` | `agent-1` | Идентификатор агента |
| `ENABLE_KAFKA` | `true` | Использовать Kafka вместо HTTP |
| `KAFKA_BROKERS` | `localhost:9092` | Kafka брокеры (через запятую) |
| `KAFKA_TOPIC` | `metrics` | Kafka топик |
| `INGESTION_URL` | `http://localhost:8080/api/v1/ingest` | HTTP endpoint (когда `ENABLE_KAFKA=false`) |
| `FAKE_SERVICE_METRICS_URL` | `http://localhost:8090/metrics` | Scrape target |
| `COLLECT_INTERVAL_SEC` | `10` | Интервал сбора метрик |

### ingestion

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `HTTP_PORT` | `8080` | HTTP порт |
| `STORAGE_MODE` | `memory` | `memory` или `postgres` |
| `DB_DSN` | `postgres://postgres:secret@timescale:5432/anomaly?sslmode=disable` | TimescaleDB DSN |
| `ENABLE_BROKER` | `true` | Включить Kafka consumer |
| `BROKER_BROKERS` | `localhost:9092` | Kafka брокеры |
| `BROKER_TOPIC` | `metrics` | Kafka топик |
| `BROKER_GROUP_ID` | `ingestion-group` | Kafka consumer group |

### analyzer

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `DB_DSN` | `postgres://postgres:secret@timescale:5432/anomaly?sslmode=disable` | TimescaleDB DSN |
| `NOTIFIER_URL` | `http://localhost:8082/api/v1/notifications` | Notifier endpoint |
| `ANALYZER_ID` | `analyzer-1` | Уникальный ID анализатора |
| `POLL_INTERVAL` | `5s` | Интервал polling |
| `BATCH_SIZE` | `100` | Метрик за один poll |
| `RULES_FILE` | `rules.yaml` | Файл с правилами |

### notifier

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `HTTP_ADDR` | `:8082` | Адрес HTTP сервера |
| `DB_DSN` | `postgres://postgres:secret@notifier:5432/notifier?sslmode=disable` | PostgreSQL DSN |
| `LOG_LEVEL` | `info` | Уровень логирования |

---

## API

### Ingestion

```
POST /api/v1/ingest
{
  "agent_id": "agent-1",
  "metrics": [
    {
      "name": "http.latency_avg_ms",
      "value": 12.5,
      "type": "gauge",
      "timestamp": "2026-04-02T10:00:00Z",
      "labels": {"service": "fake-service"}
    }
  ]
}
→ 202 Accepted
```

### Analyzer (internal)

Analyzer не имеет внешнего HTTP API. Он:
1. Polls TimescaleDB: `SELECT ... WHERE analyzed_at IS NULL FOR UPDATE SKIP LOCKED`
2. Mark как `analyzed_at = NOW()` после обработки
3. Отправляет аномалии в Notifier

### Notifier

| Endpoint | Метод | Описание |
|----------|-------|----------|
| `/api/v1/notifications` | POST | Принять аномалию от analyzer |
| `/api/v1/incidents` | GET | Список всех инцидентов |
| `/api/v1/incidents/:id` | GET | Детали инцидента |
| `/api/v1/incidents/:id/escalate` | POST | Эскалация (OPEN→ESCALATED) |
| `/api/v1/incidents/:id/resolve` | POST | Закрытие с резолюцией |
| `/api/v1/incidents/:id/comment` | POST | Добавить комментарий |
| `/ui/incidents` | GET | Web UI |

---

## Правила (analyzer)

```yaml
rules:
  - name: high_latency
    metric: http.latency_avg_ms
    type: threshold
    condition: "value > 100"
    severity: warning

  - name: very_high_latency
    metric: http.latency_avg_ms
    type: threshold
    condition: "value > 500"
    severity: critical
```

Поддерживаемые типы правил:
- `threshold` — простое выражение (реализовано)
- `lua` — кастомная логика в Lua sandbox (реализовано)
- `ml` — machine learning (stub)

---

## Incident Lifecycle

```
     ┌────────────────────────────────────────────────────────────┐
     │                                                            │
     │  ┌──────┐    anomaly    ┌─────────┐    escalate    ┌──────────┐
     └──│ OPEN │◀──────────────│ UPDATED │◀──────────────│ ESCALATED│
        └──┬───┘               └────┬────┘               └────┬─────┘
           │                        │                         │
           │        ┌─────────────┘                         │
           │        │                                       │
           │        ▼                                       │
           │   ┌──────────┐                                 │
           └──│ RESOLVED │◀────────────────────────────────┘
               └──────────┘
                 resolve
```

---

## Тестирование

```bash
# Unit тесты
make test

# Интеграционные тесты (требуют Docker)
make test-integration

# Линтинг
make lint

# Форматирование
make fmt
```

| Тест | Описание |
|------|----------|
| `TestDockerBuild` | Все Dockerfile собираются |
| `TestStorageAndMigration` | TimescaleDB migrations |
| `TestIngestionHTTPEndToEnd` | POST → Ingestion → TimescaleDB |
| `TestNotifierEndToEnd` | Full lifecycle: create → update → escalate → comment → resolve |
| `TestNotifierDeduplication` | Дубликаты аномалий → один инцидент |
