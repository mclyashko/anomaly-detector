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
	ID           int64     `json:"id"`
	Rule         string    `json:"rule"`
	Metric       string    `json:"metric"`
	MetricID     int64     `json:"metric_id"`
	Value        float64   `json:"value"`
	Condition    string    `json:"condition"`
	Severity     Severity  `json:"severity"`
	Timestamp    time.Time `json:"timestamp"`
	AgentID      string    `json:"agent_id,omitempty"`
	Message      string    `json:"message,omitempty"`
	Service      string    `json:"service,omitempty"`
	Forecast     float64   `json:"forecast,omitempty"`
	ExpectedValue float64  `json:"expected_value,omitempty"`
	LowerCI      float64   `json:"lower_ci,omitempty"`
	UpperCI      float64   `json:"upper_ci,omitempty"`
}

// MLRuleInfo describes an ML rule's training configuration for the training service.
type MLRuleInfo struct {
	AgentID           string `json:"agent_id"`
	Metric            string `json:"metric"`
	TrainIntervalMin  int    `json:"train_interval_min"`
	TrainDataWindow   int    `json:"train_data_window"`
	SeasonalityPeriod int    `json:"seasonality_period"`
	Order             []int  `json:"order"`
	SeasonalOrder     []int  `json:"seasonal_order"`
}
