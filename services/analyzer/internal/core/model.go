package core

import (
	"time"
)

// Metric represents a single time-series data point received from ingestion.
type Metric struct {
	ID        int64             `json:"id"`
	AgentID   string            `json:"agent_id"`
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Type      string            `json:"type"`
}

// Batch is the incoming payload from the ingestion service.
type Batch struct {
	AgentID    string    `json:"agent_id"`
	ReceivedAt time.Time `json:"received_at"`
	Metrics    []Metric  `json:"metrics"`
}

// Severity levels for anomaly events.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Anomaly is produced when a rule fires.
type Anomaly struct {
	Rule      string    `json:"rule"`
	Metric    string    `json:"metric"`
	Value     float64   `json:"value"`
	Condition string    `json:"condition"`
	Severity  Severity  `json:"severity"`
	Timestamp time.Time `json:"timestamp"`
	AgentID   string    `json:"agent_id,omitempty"`
	Message   string    `json:"message,omitempty"`
}
