# anomaly-detector

Microservice система для сбора time-series метрик, обнаружения аномалий и управления инцидентами.

---

## Архитектура

Метрики протекают через систему слева направо: от источника данных до инцидента.

**Источник** — fake-service, который генерирует метрики и отдаёт их по HTTP на запрос агента. Агент периодически ходит GET-запросом на `/metrics`, собирает системные метрики (memory, GC, goroutines) и метрики с HTTP-эндпоинта. Собранный батч отправляется в Ingestion — либо через Kafka (основной путь), либо HTTP POST (fallback если Kafka недоступен).

**Ingestion** (порт 8080) принимает метрики двумя способами: HTTP API `POST /api/v1/ingest` и опциональный Kafka consumer из топика `metrics`. Метрики сохраняются в TimescaleDB.

**Analyzer** (порт 8081) работает в двух режимах. Основной — polling: каждые несколько секунд делает `SELECT ... WHERE analyzed_at IS NULL FOR UPDATE SKIP LOCKED`, забирая необработанные метрики. Блокировка `SKIP LOCKED` позволяет нескольким инстансам analyzer-а работать параллельно, не конфликтуя — каждый берёт свой набор метрик. Второй режим — HTTP API `POST /api/v1/analyze` для синхронного анализа входящего батча. Каждая метрика прогоняется через `RuleEngine`: пороговые правила проверяются по имени метрики за O(1), ML-правила — по ключу `agentID:metricName`. Для ML-правил analyzer отправляет запрос в Training Service по HTTP, получает прогноз с доверительным интервалом, и если значение за пределами CI — фиксирует аномалию. Обнаруженные аномалии отправляются в Notifier HTTP POST-ом.

**Training Service** (порт 8085) — Python/FastAPI. Обучает SARIMA модели на исторических данных из TimescaleDB и отвечает на HTTP-запросы от analyzer-а с прогнозом и CI. Модели хранятся как JSON-файлы локально в `models/{agent}__{metric}/metadata.json`. Если задан `DB_DSN`, сервис периодически проверяет актуальность моделей и переобучает их по таймеру.

**Notifier** (порт 8082) получает аномалии от analyzer-а, создаёт инциденты и управляет их жизненным циклом. Инциденты дедуплицируются по ключу `rule|service|metric` — повторная аномалия по тому же правилу не создаёт новый инцидент, а обновляет существующий. Данные хранятся в PostgreSQL. Доступен REST API и Web UI.

**PUSH vs PULL**: Agent → Ingestion это PUSH (HTTP или Kafka). Ingestion → TimescaleDB это PUSH (SQL INSERT). Analyzer → TimescaleDB это PULL (polling с `FOR UPDATE SKIP LOCKED`). Analyzer → Training и Analyzer → Notifier это PUSH (HTTP POST).

### ML-based Anomaly Detection (HTTP → Python)

Analyzer отправляет метрики в Python Training Service по HTTP для ML-обнаружения аномалий:

- **HTTP API**: `POST /api/v1/evaluate` — получает history + value, возвращает anomaly/forecast/CI
- **Python inference**: `infer.py` реализует AR(1) + seasonal AR(1) с доверительными интервалами
- **Training**: `app.py` использует statsmodels SARIMAX для обучения, параметры сохраняются в JSON metadata
- **Хранение моделей**: JSON-файлы в `models/{agent}__{metric}/metadata.json` локально (не MinIO)
- **No CGO**: Go ↔ Python через HTTP, никаких нативных библиотек
- **Auto-retrain**: если `DB_DSN` задан, training service периодически проверяет модели на актуальность и переобучает если `train_interval_min` прошло с последнего обучения

### PUSH vs PULL

| Этап | Модель | Протокол | Описание |
|------|--------|----------|----------|
| Agent → Ingestion | **PUSH** | HTTP POST или Kafka | Метрики отправляются сразу |
| Ingestion → TimescaleDB | PUSH | SQL INSERT | Сохранение метрик |
| Analyzer → TimescaleDB | **PULL** | SQL SELECT ... FOR UPDATE SKIP LOCKED | Polling необработанных метрик |
| Analyzer → Training (ML) | **PUSH** | HTTP POST | ML inference |
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
| `fake-service` | :8090 | Тестовый генератор метрик (CPU-bound воркеры + sinusoidal test signals) |
| `agent` | — | CLI-утилита: собирает метрики (system metrics + HTTP scrape), отправляет в ingestion |
| `ingestion` | :8080 | HTTP API + Kafka consumer → TimescaleDB |
| `analyzer` | :8081 | HTTP API для batch-анализа + polling TimescaleDB, rule evaluation, ML inference |
| `notifier` | :8082 | Incident lifecycle + Web UI |
| `training` | :8085 | FastAPI ML service (train + evaluate) |
| `TimescaleDB` | :5432 | Хранение метрик (time-series) |
| `PostgreSQL` (notifier) | :5433 | Хранение инцидентов |
| `Kafka` | :9092 | Message queue |

---

## Быстрый старт

```bash
# Все сервисы (включая ML training service)
make build-up-ml

# Без ML training service
make build-up

# Go unit тесты
make test

# Python ML unit тесты
.venv/bin/python -m pytest services/training/tests/ -v

# Интеграционные тесты (требуют запущенных сервисов)
make test-integration
```

---

## Конфигурация

### analyzer

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `DB_DSN` | `postgres://postgres:secret@timescale:5432/anomaly?sslmode=disable` | TimescaleDB DSN |
| `NOTIFIER_URL` | `http://localhost:8082/api/v1/notifications` | Notifier endpoint |
| `ANALYZER_ID` | `analyzer-1` | Уникальный ID анализатора |
| `ANALYZER_POLL_INTERVAL_SEC` | `10` | Интервал polling (в docker-compose: 5 сек) |
| `ANALYZER_BATCH_SIZE` | `100` | Метрик за один poll |
| `RULES_FILE` | `rules.yaml` | Файл с правилами |
| `ML_SERVICE_URL` | `""` | URL ML Training Service (в docker-compose: `http://training:8085`) |

### training

| Переменная | По умолчанию | Описание |
|------------|--------------|----------|
| `PORT` | `8085` | HTTP порт |
| `MODELS_DIR` | `./models` | Директория для хранения моделей (JSON metadata) |
| `DB_DSN` | — | TimescaleDB DSN. Если задано — включается auto-retrain |
| `ANALYZER_URL` | `http://analyzer:8081` | URL analyzer для получения ML-правил (auto-retrain) |
| `MODEL_REFRESH_CHECK_SEC` | `60` | Период проверки моделей на актуальность (auto-retrain) |

---

## API

### Ingestion

```
POST /api/v1/ingest
{
  "agent_id": "agent-1",
  "metrics": [
    {
      "name": "http_latency_avg_ms",
      "value": 12.5,
      "type": "gauge",
      "timestamp": "2026-04-02T10:00:00Z",
      "labels": {"service": "fake-service"}
    }
  ]
}
202 Accepted
```

### Training Service (ML)

```
POST /api/v1/evaluate
{
  "model_id": "agent-1__http_latency_avg_ms",
  "history": [50.0, 51.2, 49.8, ...],
  "value": 52.1
}

Response:
{
  "model_id": "agent-1__http_latency_avg_ms",
  "anomaly": true,
  "forecast": 51.0,
  "lower_ci": 49.5,
  "upper_ci": 52.5,
  "value": 52.1,
  "message": "value=52.1000 outside CI [49.5000, 52.5000]"
}
```

### Analyzer

Analyzer работает в двух режимах:

**Polling (основной)**: периодически опрашивает TimescaleDB:
```
SELECT ... WHERE analyzed_at IS NULL FOR UPDATE SKIP LOCKED
```
Каждый инстанс берёт разные метрики блокировкой `SKIP LOCKED`.

**HTTP API** (входящий batch):
```
POST /api/v1/analyze
{
  "agent_id": "agent-1",
  "metrics": [...]
}
```
Весь pipeline: fetch → evaluate → mark → notify происходит синхронно.

**Дополнительные endpoints**:
- `GET /api/v1/ml-rules` — возвращает конфигурацию ML-правил (используется training service для auto-retrain)
- `GET /healthz` — health check

### Notifier

| Endpoint | Метод | Описание |
|----------|-------|---------|
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
  # Пороговое правило: срабатывает если value > 100
  - name: high_latency
    enabled: true
    metric: http_latency_avg_ms
    type: threshold
    condition: "value > 100"
    severity: warning

  # ML правило: отправляет метрику в Python training service для оценки аномальности.
  # Все ML параметры required для type=ml.
  - name: signal_anomaly
    enabled: true
    metric: test_signal
    agent_id: agent-1      # пусто = все агенты
    type: ml
    severity: critical
    train_interval_min: 30  # переобучать каждые 30 минут
    train_data_window: 200 # сколько точек брать из TimescaleDB для обучения
    seasonality_period: 60   # S — период сезонности (точек на цикл)
    order: [1, 0, 1]       # ARIMA order [p, d, q]
    seasonal_order: [1, 1, 1, 60]  # SARIMA seasonal [P, D, Q, S]
```

Поддерживаемые типы правил:
- `threshold` — простое выражение (реализовано)
- `lua` — кастомная логика в Lua sandbox (реализовано)
- `ml` — ML-обнаружение аномалий по CI через HTTP (реализовано)

**Threshold rule**: `agent_id` может быть пустым — тогда правило применяется к метрикам от любого агента.
**ML rule**: `agent_id` обязателен для scoping модели (модель шлётся как `{agent_id}__{metric}`).

---

## ML Inference Algorithm (Python)

```
forecast = last_val + ar_params * (last_val - prev_val)
          + seasonal_ar_params * (last_val - val_S_ago)

CI_half_width = normQuantile((1 + confidence_level) / 2) * residual_std
CI = [forecast - CI_half_width, forecast + CI_half_width]
anomaly = value < CI_lower OR value > CI_upper
```

Где `normQuantile` — квантиль стандартного нормального распределения (inverse CDF), вычисляется через `scipy.special.erfcinv`.

---

## Incident Lifecycle

Жизненный цикл инцидента: OPEN → UPDATED → ESCALATED → RESOLVED.

Новый инцидент создаётся при первой аномалии со статусом OPEN. Повторные аномалии по тому же правилу переводят инцидент в UPDATED. Если инцидент не разрешён и поступает новая аномалия — можно вызвать эскалацию, которая переводит инцидент в ESCALATED. Разрешение инцидента переводит его в RESOLVED. Разрешённый инцидент остаётся в базе как историческая запись.

Дедупликация работает по ключу `rule|service|metric`: если по такому ключу уже есть открытый инцидент, новая аномалия не создаёт новый инцидент, а обновляет существующий (переводит в UPDATED).

---

## Fake-Service Signal Toggle (ML Testing)

Fake-service генерирует тестовый signal `test_signal` и предоставляет REST API для переключения между двумя паттернами — **Signal A** и **Signal B**. Это позволяет тестировать ML-обнаружение аномалий без внесения реальных аномалий.

### Сигналы

| Сигнал | Формула | Период | Применение |
|--------|---------|--------|------------|
| **Signal A** (normal) | `sin(2π * t / 60) * 4 + noise` | 60 точек ≈ 60 сек | Обучающая выборка для SARIMA |
| **Signal B** (anomaly) | `sin(2π * t / 30) * 4 + noise` | 30 точек ≈ 30 сек | Тест: SARIMA с S=60 обнаруживает аномалию |

Signal A это синусоида с периодом 60 точек. Signal B — синусоида с периодом 30 точек, то есть вдвое быстрее. SARIMA модель, обученная на Signal A с seasonality_period=60, ожидает период 60. Когда активен Signal B, реальный период 30 — модель выдаёт прогноз мимо реальных значений, и они выходят за доверительный интервал. Шум ≈ ±0.021 — детерминированный, от t по модулю 7, достаточный для реалистичности但不 меняющий паттерн.

### REST API

```bash
# Текущее состояние
curl http://localhost:8090/signal-state
{"active_signal":"A","signal_a":1.532,"signal_b":-1.932}

# Переключить сигнал
curl -X POST http://localhost:8090/signal-switch
{"active_signal":"B"}

# Метрики (включает текущие значения обоих сигналов)
curl http://localhost:8090/metrics
{"http_requests_total":3651,"http_errors_total":0,"http_latency_avg_ms":0.047,"worker_ops_total":957495546,"test_signal":-1.932,"active_signal":"B"}
```

### Принцип работы

1. **Training**: SARIMA модель обучается на Signal A с `seasonality_period=60`
2. **Normal mode**: Signal A активен → значения попадают в доверительный интервал → **нет аномалий**
3. **Test mode**: Signal B активен → период 30 вместо 60 → значения **выходят за CI** → аномалии

Это позволяет проверять весь pipeline целиком:
```
Signal B → agent → kafka → ingestion → TimescaleDB → analyzer (ML rule) → notifier → incident
```

### Отображение в UI

Alerts видны в Notifier UI: http://localhost:8082/incidents

---

## Тестирование

```bash
# End-to-end тест аномалии:
# 1. Переключить на Signal B (паттерн с другим периодом)
curl -X POST http://localhost:8090/signal-switch

# 2. Подождать 10-20 секунд пока накопятся метрики

# 3. Проверить incidents
curl http://localhost:8082/incidents
```

| Тест | Расположение | Описание |
|------|------------|----------|
| `TestDockerBuild` | `tests/integration/` | Все Dockerfile собираются |
| `TestStorageAndMigration` | `tests/integration/` | TimescaleDB migrations |
| `TestIngestionHTTPEndToEnd` | `tests/integration/` | POST → Ingestion → TimescaleDB |
| `TestNotifierEndToEnd` | `tests/integration/` | Full lifecycle: create → update → escalate → comment → resolve |
| `TestNotifierDeduplication` | `tests/integration/` | Дубликаты аномалий → один инцидент |
| `TestSinParabolaDiscrimination` | `services/training/tests/` | Синусоида vs парабола — 0 vs много аномалий |
| `TestEvaluateEndpointStructure` | `services/training/tests/` | Структура ответа /api/v1/evaluate |
| **Signal Toggle Test** | Manual | Signal A → Signal B → инциденты создаются |
