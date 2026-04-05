package domain

import "time"

type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

type Metric struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Type      MetricType        `json:"type"`
}

type Batch struct {
	AgentID   string    `json:"agent_id"`
	Metrics   []Metric  `json:"metrics"`
	CreatedAt time.Time `json:"created_at"`
}
