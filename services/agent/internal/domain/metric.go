package domain

import "time"

// MetricType определяет тип метрики — counter (монотонно растёт) или gauge (может произвольно меняться).
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"   // мгновенное значение, может быть любым
	MetricTypeCounter MetricType = "counter" // монотонно увеличивается (например, количество запросов)
)

// Metric — один samples в time series от agent к ingestion.
// Name — имя метрики (например "system.memory.heap_alloc_bytes"),
// Value — числовое значение, Type — gauge или counter.
type Metric struct {
	Name      string            `json:"name"`      // имя метрики в формате " subsystem.metric_name"
	Value     float64           `json:"value"`     // числовое значение
	Labels    map[string]string `json:"labels,omitempty"` // дополнительные лейблы (например service="fake-service")
	Timestamp time.Time         `json:"timestamp"` // время когда значение было считано
	Type      MetricType        `json:"type"`      // gauge или counter
}

// Batch — группа метрик от одного agent за один запрос.
// AgentID идентифицирует источник, CreatedAt — время формирования батча.
type Batch struct {
	AgentID   string    `json:"agent_id"` // уникальный ID агента (например "agent-1")
	Metrics   []Metric  `json:"metrics"`   // список метрик в этом батче
	CreatedAt time.Time `json:"created_at"` // время когда батч был создан
}
