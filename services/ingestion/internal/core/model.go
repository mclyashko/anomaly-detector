package core

import (
	"fmt"
	"math"
	"time"
)

type MetricType string

const (
	MetricTypeGauge   MetricType = "gauge"
	MetricTypeCounter MetricType = "counter"
)

type Metric struct {
	Name      string
	Value     float64
	Labels    map[string]string
	Timestamp time.Time
	Type      MetricType
}

type Batch struct {
	AgentID    string
	Metrics    []Metric
	ReceivedAt time.Time
}

// ValidationError is returned by HandleBatch on invalid input.
// HTTP adapters detect it via errors.As to return 422.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

// NewValidationError constructs a ValidationError with the given message.
// Exposed so that test packages and adapters can create instances without
// importing an unexported field.
func NewValidationError(msg string) *ValidationError { return &ValidationError{msg: msg} }

func validateBatch(b Batch) error {
	if b.AgentID == "" {
		return &ValidationError{"agent_id is required"}
	}
	if len(b.Metrics) == 0 {
		return &ValidationError{"metrics must not be empty"}
	}
	for i, m := range b.Metrics {
		if m.Name == "" {
			return &ValidationError{fmt.Sprintf("metrics[%d].name is required", i)}
		}
		if m.Timestamp.IsZero() {
			return &ValidationError{fmt.Sprintf("metrics[%d].timestamp is required", i)}
		}
		if math.IsNaN(m.Value) || math.IsInf(m.Value, 0) {
			return &ValidationError{fmt.Sprintf("metrics[%d].value must be finite", i)}
		}
	}
	return nil
}
