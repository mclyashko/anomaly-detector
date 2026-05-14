# anomaly-detector

Microservice-система для сбора time-series метрик, обнаружения аномалий и управления инцидентами.

---

## Архитектура

Метрики текут слева направо: fake-service → agent → ingestion → TimescaleDB → analyzer → notifier → инциденты.

**fake-service** (порт 8090) генерирует 5 метрик и отдаёт по HTTP. Параллельно меняет `test_signal` между двумя режимами: синусоида с периодом 60 (Signal A, normal) и с периодом 30 (Signal B, anomaly). Это позволяет тестировать ML-детекцию без внесения реальных аномалий.

**agent** собирает метрики с fake-service и system metrics (memory, GC, goroutines) через Go runtime, объединяет в батч и пушит в Kafka топик `metrics`.

**ingestion** — Kafka consumer → TimescaleDB. Два режима хранения: `db` (TimescaleDB) и `memory` (утрачивается при рестарте).

**analyzer** (порт 8081) работает в режиме polling: `SELECT ... FOR UPDATE SKIP LOCKED WHERE analyzed_at IS NULL`. SKIP LOCKED позволяет горизонтально масштабировать несколько инстансов без конфликтов. Также доступен HTTP endpoint `POST /api/v1/analyze` для синхронного анализа.

Каждая метрика прогоняется через RuleEngine:
- `threshold` — по имени метрики
- `lua` — по скрипту в sandbox
- `ml` — отправляется в training service по HTTP

Обнаруженные аномалии пушатся в Kafka топик `anomalies`.

**training** (порт 8085) — Python/FastAPI. Обучает SARIMA модели и отвечает на HTTP-запросы от analyzer-а. Модели хранятся как JSON в `models/{agent}__{metric}/metadata.json`. При наличии `DB_DSN` включается auto-retrain по таймеру.

**notifier** (порт 8082) — Kafka consumer → PostgreSQL. Создаёт инциденты, управляет их жизненным циклом. Дедупликация по ключу `rule|service|metric`. Доступен REST API и Web UI.

### PUSH/PULL

| Этап | Модель | Протокол |
|------|--------|----------|
| Agent → Ingestion | PUSH | Kafka |
| Ingestion → TimescaleDB | PUSH | SQL INSERT |
| Analyzer → TimescaleDB | PULL | `FOR UPDATE SKIP LOCKED` |
| Analyzer → Training | PUSH | HTTP POST |
| Analyzer → Notifier | PUSH | Kafka |
| Notifier → PostgreSQL | PUSH | SQL CRUD |

### High Availability

- **Agent**: N инстансов с разными `AGENT_ID`
- **Ingestion**: Kafka consumer group — каждый инстанс читает свои партиции
- **Analyzer**: `SKIP LOCKED` — каждый инстанс берёт свой набор метрик
- **Notifier**: PostgreSQL unique constraint

---

## Сервисы

| Сервис | Порт | Описание |
|--------|------|----------|
| `fake-service` | :8090 | Генератор метрик + Signal A/B toggle |
| `agent` | — | Собирает и отправляет метрики в Kafka |
| `ingestion` | :8080 | Kafka → TimescaleDB |
| `analyzer` | :8081 | Polling + rule evaluation + ML |
| `notifier` | :8082 | Инциденты, REST API + Web UI |
| `training` | :8085 | SARIMA train + evaluate |
| `TimescaleDB` | :5432 | Метрики |
| `PostgreSQL (notifier)` | :5432 | Инциденты |
| `Kafka` | :9092 | Очередь |

---

## Быстрый старт

```bash
# Все сервисы
make build-up

# Go unit тесты
make test

# Python
make python-setup
.venv/bin/python -m pytest services/training/tests/ -v

# Интеграционные (требуют Docker)
make test-integration
```

---

## Конфигурация

Переменные окружения из `infra/env/*.env`. env-файлы коммитятся — секретов не содержат.

| Сервис | Файл |
|--------|------|
| agent | `env/agent.env` |
| fake-service | `env/fake-service.env` |
| ingestion | `env/ingestion.env` |
| analyzer | `env/analyzer.env` |
| training | `env/training.env` |
| notifier | `env/notifier.env` |

### agent

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `AGENT_ID` | `agent-1` | Уникальный ID |
| `KAFKA_BROKERS` | `kafka:9092` | Kafka брокер |
| `KAFKA_TOPIC` | `metrics` | Топик для метрик |
| `FAKE_SERVICE_METRICS_URL` | `http://fake-service:8090/metrics` | fake-service endpoint |
| `COLLECT_INTERVAL_SEC` | `1` | Интервал сбора |
| `LOG_LEVEL` | `info` | Уровень логирования |

### fake-service

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `HTTP_PORT` | `8090` | HTTP порт |
| `WORKER_COUNT` | `4` | Количество воркеров |
| `LOG_LEVEL` | `info` | Уровень логирования |

### ingestion

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `ENABLE_BROKER` | `true` | Включить Kafka consumer |
| `BROKER_BROKERS` | `kafka:9092` | Kafka брокеры |
| `BROKER_TOPIC` | `metrics` | Топик для чтения |
| `BROKER_GROUP_ID` | `ingestion-group` | Consumer group |
| `BROKER_WORKERS` | `8` | Воркеры |
| `BROKER_CAPACITY` | `256` | Размер буфера |
| `STORAGE_MODE` | `db` | `db` или `memory` |
| `DB_DSN` | `postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable` | TimescaleDB DSN |
| `LOG_LEVEL` | `info` | Уровень логирования |

### analyzer

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `HTTP_PORT` | `8081` | HTTP порт |
| `DB_DSN` | `postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable` | TimescaleDB DSN |
| `KAFKA_BROKERS` | `kafka:9092` | Kafka брокеры |
| `KAFKA_ANOMALIES_TOPIC` | `anomalies` | Топик для аномалий |
| `ANALYZER_ID` | `analyzer-1` | Уникальный ID |
| `ANALYZER_POLL_INTERVAL_SEC` | `5` | Интервал polling |
| `ANALYZER_BATCH_SIZE` | `100` | Метрик за poll |
| `RULES_FILE` | `/rules.yaml` | Файл с правилами |
| `ML_SERVICE_URL` | `http://training:8085` | Training Service |

### training

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `DB_DSN` | — | TimescaleDB DSN (включает auto-retrain) |
| `PORT` | `8085` | HTTP порт |
| `LOG_LEVEL` | `info` | Уровень логирования |
| `MODELS_DIR` | `/app/models` | Директория для моделей |
| `ANALYZER_URL` | `http://analyzer:8081` | Для получения ML-правил |
| `MODEL_REFRESH_CHECK_SEC` | `60` | Период проверки моделей |

### notifier

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `HTTP_PORT` | `8082` | HTTP порт |
| `LOG_LEVEL` | `info` | Уровень логирования |
| `DB_DSN` | `postgres://postgres:secret@timescaledb:5432/notifier?sslmode=disable` | PostgreSQL DSN |
| `KAFKA_BROKERS` | `kafka:9092` | Kafka брокеры |
| `KAFKA_ANOMALIES_TOPIC` | `anomalies` | Топик для аномалий |
| `ANALYZER_URL` | `http://analyzer:8081` | URL analyzer service (Rules page) |

---

## API

### fake-service

| Endpoint | Метод | Описание |
|----------|-------|---------|
| `/metrics` | GET | Все метрики |
| `/signal-state` | GET | Активный сигнал (A/B) |
| `/signal-switch` | POST | Переключить сигнал |

### training

| Endpoint | Метод | Описание |
|----------|-------|---------|
| `/health` | GET | Health check |
| `/api/v1/train` | POST | Обучить модель |
| `/api/v1/evaluate` | POST | Оценить метрику |
| `/api/v1/models` | GET | Список моделей |
| `/api/v1/models/{model_id}` | GET | Метаданные модели |
| `/api/v1/ml-rules` | GET | Конфигурация ML-правил |

```
POST /api/v1/evaluate
{
  "model_id": "agent-1__test_signal",
  "history": [50.0, 51.2, 49.8, ...],
  "value": 52.1
}
→ {"anomaly": true, "forecast": 51.0, "lower_ci": 49.5, "upper_ci": 52.5, ...}
```

### analyzer

| Endpoint | Метод | Описание |
|----------|-------|---------|
| `/api/v1/analyze` | POST | Анализ батча метрик |
| `/api/v1/ml-rules` | GET | Конфигурация ML-правил |
| `/healthz` | GET | Health check |

### notifier

| Endpoint | Метод | Описание |
|----------|-------|---------|
| `/healthz` | GET | Health check |
| `/api/v1/notifications` | POST | Принять аномалию |
| `/api/v1/incidents` | GET | Список инцидентов (фильтрация: `?status=OPEN&severity=critical&page=1&page_size=20`) |
| `/api/v1/incidents/{id}` | GET | Детали инцидента |
| `/api/v1/incidents/{id}/escalate` | POST | Эскалация |
| `/api/v1/incidents/{id}/resolve` | POST | Закрытие |
| `/api/v1/incidents/{id}/comment` | POST | Комментарий |
| `/api/v1/rules` | GET | Правила из analyzer (Rules page) |
| `/rules` | GET | Web UI — Rules page |
| `/ui/incidents` | GET | Web UI — Incidents list |

---

## Правила (analyzer)

Правила в `rules.yaml`. Типы: `threshold`, `lua`, `ml`, `ks`. Severity: `critical`, `warning`, `info`.

### threshold

Пороговое выражение — `ParseCondition`:
```
value > 100
value >= 0.8
```

### lua

Lua скрипт в sandbox:
```yaml
- name: cpu_anomaly
  enabled: true
  metric: cpu_usage
  type: lua
  script: /rules/cpu_rule.lua
  severity: warning
```

### ml

SARIMA через HTTP. `agent_id` обязателен — модель шлётся как `{agent_id}__{metric}`:
```yaml
- name: new_signal_ml
  enabled: true
  agent_id: agent-1
  metric: test_signal
  type: ml
  severity: critical
  train_interval_min: 30
  train_data_window: 200
  seasonality_period: 60
  order: [1, 0, 1]
  seasonal_order: [1, 1, 1, 60]
```

### ks

Двухвыборочный тест Колмогорова-Смирнова. Сравнивает последние `min_window_size` значений с предшествующими `reference_window` значениями. Если p-value < significance_level — аномалия. Полностью на Go, без внешнего сервиса.

```yaml
- name: test_signal_ks
  enabled: false
  agent_id: agent-1
  metric: test_signal
  type: ks
  severity: critical
  reference_window: 60
  min_window_size: 30
  significance_level: 0.05
```

### Пример rules.yaml

```yaml
rules:
  - name: new_signal_ml
    enabled: true
    agent_id: agent-1
    metric: test_signal
    type: ml
    severity: critical
    train_interval_min: 30
    train_data_window: 200
    seasonality_period: 60
    order: [1, 0, 1]
    seasonal_order: [1, 1, 1, 60]

  - name: high_latency
    enabled: false
    metric: http_latency_avg_ms
    type: threshold
    condition: "value > 100"
    severity: warning
```

---

## Incident Lifecycle

Статусы: `OPEN` → `UPDATED` → `ESCALATED` → `RESOLVED`.

- Первая аномалия → `OPEN`
- Повторная по тому же ключу `rule|service|metric` → `UPDATED`. `ESCALATED` не деградирует.
- `POST /escalate` → `ESCALATED` (из `OPEN` или `UPDATED`)
- `POST /resolve` → `RESOLVED` (из любого статуса кроме `RESOLVED`)
- Новая аномалия на `RESOLVED` → `OPEN` (переоткрытие)

```
Аномалия #1 → OPEN
Аномалия #2 → UPDATED
/escalate → ESCALATED
Аномалия #4 → ESCALATED (остается)
/resolve → RESOLVED
Аномалия #5 → OPEN (переоткрыт)
```

**Дедупликация**: ключ `rule|service|metric`. Открытый инцидент обновляется, новый не создаётся. Unique constraint включает `status` — одновременно могут существовать `RESOLVED` и `OPEN` с одним ключом.

---

## Signal Toggle (тестирование ML)

fake-service генерирует `test_signal` в двух режимах:

| Сигнал | Формула | Период |
|--------|---------|--------|
| **Signal A** (normal) | `sin(2π * t / 60) * 4 + noise` | 60 точек |
| **Signal B** (anomaly) | `sin(2π * t / 30) * 4 + noise` | 30 точек |

SARIMA обучается на Signal A с `seasonality_period=60`. Когда активен Signal B, период вдвое короче — модель не может предсказать значения, они выходят за доверительный интервал, создаются инциденты.

```bash
# Переключить на Signal B
curl -X POST http://localhost:8090/signal-switch

# Проверить инциденты
curl http://localhost:8082/api/v1/incidents

# Вернуть Signal A
curl -X POST http://localhost:8090/signal-switch
```

---

## Тестирование

### Go интеграционные (`tests/integration/`)

```bash
make test-integration
```

| Тест | Описание |
|------|----------|
| `TestDockerBuild` | Все Dockerfile собираются |
| `TestStorageAndMigration` | TimescaleDB migrations + batch insert |
| `TestNotifierEndToEnd` | Create → update → escalate → comment → resolve |
| `TestNotifierDeduplication` | Дубликаты аномалий → один инцидент |
| `TestNotifierInvalidStatusTransitions` | Невалидные переходы статусов |

### Python unit (`services/training/tests/`)

```bash
.venv/bin/python -m pytest services/training/tests/ -v
```

| Тест | Описание |
|------|----------|
| `TestTrainSarima` | Обучение на sin/parabola |
| `TestInfer` | Forecast и CI |
| `TestModelNaming` | Формат `model_id = {agent}__{metric}` |
| `TestSinParabolaDiscrimination` | Sin → 0 аномалий, parabola → много |
| `TestEvaluateEndpointStructure` | Структура ответа /api/v1/evaluate |

### Нагрузочное тестирование (k6)

```bash
make load-test-up
```