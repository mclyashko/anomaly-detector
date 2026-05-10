package domain

import "time"

// MetricType defines the metric type — counter (monotonically increasing) or gauge (can change freely).
type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"   // instantaneous value, can be anything
	MetricTypeCounter MetricType = "counter" // monotonically increasing (e.g. request count)
)

// Metric is one sample in a time series from agent to ingestion.
// Name is the metric name (e.g. "system.memory.heap_alloc_bytes"),
// Value is the numeric value, Type is gauge or counter.
type Metric struct {
	Name      string            `json:"name"`             // metric name in "subsystem.metric_name" format
	Value     float64           `json:"value"`            // numeric value
	Labels    map[string]string `json:"labels,omitempty"` // additional labels (e.g. service="fake-service")
	Timestamp time.Time         `json:"timestamp"`        // time when the value was collected
	Type      MetricType        `json:"type"`             // gauge or counter
}

// Batch is a group of metrics from one agent in a single request.
// AgentID identifies the source, CreatedAt is the batch formation time.
type Batch struct {
	AgentID   string    `json:"agent_id"`   // unique agent ID (e.g. "agent-1")
	Metrics   []Metric  `json:"metrics"`    // list of metrics in this batch
	CreatedAt time.Time `json:"created_at"` // time when the batch was created
}
