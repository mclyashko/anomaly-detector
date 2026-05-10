package core

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"time"
)

// IncidentStatus is the lifecycle status of an incident.
// OPEN: newly created
// UPDATED: new data arrived for the same incident (rule+service+metric)
// ESCALATED: escalated
// RESOLVED: closed with resolution
type IncidentStatus string

const (
	StatusOpen      IncidentStatus = "OPEN"
	StatusUpdated   IncidentStatus = "UPDATED"
	StatusEscalated IncidentStatus = "ESCALATED"
	StatusResolved  IncidentStatus = "RESOLVED"
)

// Incident is an anomalous incident.
// Value, Forecast, LowerCI, UpperCI, Message are the latest ML data.
// Events store the full history of all anomalies.
type Incident struct {
	ID            string         `json:"id"`
	Rule          string         `json:"rule"`
	Service       string         `json:"service"`
	Metric        string         `json:"metric"`
	Status        IncidentStatus `json:"status"`
	Severity      string         `json:"severity"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ResolvedAt    *time.Time     `json:"resolved_at,omitempty"`
	Resolution    string         `json:"resolution,omitempty"`
	Value         float64        `json:"value,omitempty"`
	Forecast      float64        `json:"forecast,omitempty"`
	ExpectedValue float64        `json:"expected_value,omitempty"`
	LowerCI       float64        `json:"lower_ci,omitempty"`
	UpperCI       float64        `json:"upper_ci,omitempty"`
	Message       string         `json:"message,omitempty"`
}

// IncidentEvent is a single anomaly event tied to an incident.
// Payload stores JSON with full anomaly data (including ML info).
type IncidentEvent struct {
	ID         string          `json:"id"`
	IncidentID string          `json:"incident_id"`
	Payload    json.RawMessage `json:"payload"`
	Timestamp  time.Time       `json:"timestamp"`  // time from the metric itself (when it was recorded)
	CreatedAt  time.Time       `json:"created_at"` // when the event was actually created in the DB
}

// IncidentComment is a comment on an incident.
type IncidentComment struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

// AnomalyPayload is an incoming event from the analyzer.
// Uses custom UnmarshalJSON to parse timestamps from different formats
// (RFC3339 string or Unix timestamp).
type AnomalyPayload struct {
	Rule          string  `json:"rule"`
	Service       string  `json:"service"`
	Metric        string  `json:"metric"`
	MetricID      int64   `json:"metric_id"`
	Value         float64 `json:"value"`
	Severity      string  `json:"severity"`
	Message       string  `json:"message"`
	Timestamp     int64   `json:"timestamp"` // Unix timestamp in seconds
	AgentID       string  `json:"agent_id,omitempty"`
	Condition     string  `json:"condition,omitempty"`
	ID            int64   `json:"id,omitempty"`
	Forecast      float64 `json:"forecast,omitempty"`
	ExpectedValue float64 `json:"expected_value,omitempty"`
	LowerCI       float64 `json:"lower_ci,omitempty"`
	UpperCI       float64 `json:"upper_ci,omitempty"`
}

// SetTimestamp parses timestamps from RFC3339 or Unix format.
func (a *AnomalyPayload) SetTimestamp(v any) error {
	switch val := v.(type) {
	case float64:
		a.Timestamp = int64(val)
		return nil
	case string:
		if val == "" {
			a.Timestamp = 0
			return nil
		}
		// Try parsing as RFC3339 first.
		if tm, err := time.Parse(time.RFC3339, val); err == nil {
			a.Timestamp = tm.Unix()
			return nil
		}
		// Fallback: try parsing as Unix timestamp string.
		var t int64
		if _, err := fmt.Sscanf(val, "%d", &t); err == nil {
			a.Timestamp = t
			return nil
		}
		return fmt.Errorf("cannot parse timestamp: %s", val)
	case nil:
		a.Timestamp = 0
		return nil
	default:
		return fmt.Errorf("unexpected timestamp type: %T", val)
	}
}

// UnmarshalJSON parses JSON from the analyzer.
// Supports flexible types for all fields (string/float/int) and
// handles timestamps in any format correctly (RFC3339, Unix, float).
// After parsing, calls SetTimestamp for unified timestamp handling.
func (a *AnomalyPayload) UnmarshalJSON(data []byte) error {
	type rawPayload struct {
		Rule          any `json:"rule"`
		Service       any `json:"service"`
		Metric        any `json:"metric"`
		Value         any `json:"value"`
		Severity      any `json:"severity"`
		Message       any `json:"message"`
		Timestamp     any `json:"timestamp"`
		Forecast      any `json:"forecast"`
		ExpectedValue any `json:"expected_value"`
		LowerCI       any `json:"lower_ci"`
		UpperCI       any `json:"upper_ci"`
	}

	var r rawPayload
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}

	a.Rule = toString(r.Rule)
	a.Service = toString(r.Service)
	a.Metric = toString(r.Metric)
	a.Value = toFloat(r.Value)
	a.Severity = toString(r.Severity)
	a.Message = toString(r.Message)
	a.Forecast = toFloat(r.Forecast)
	a.ExpectedValue = toFloat(r.ExpectedValue)
	a.LowerCI = toFloat(r.LowerCI)
	a.UpperCI = toFloat(r.UpperCI)
	return a.SetTimestamp(r.Timestamp)
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		return fmt.Sprintf("%g", val)
	case nil:
		return ""
	default:
		slog.Warn("toString: unhandled type, using empty string", "type", fmt.Sprintf("%T", v))
		return ""
	}
}

func toFloat(v any) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case float32:
		return float64(val)
	case int:
		return float64(val)
	case int64:
		return float64(val)
	case string:
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
		slog.Warn("toFloat: cannot parse string as float64, using 0", "value", val)
		return 0
	case nil:
		return 0
	default:
		slog.Warn("toFloat: unhandled type, using 0", "type", fmt.Sprintf("%T", v))
		return 0
	}
}

// IncidentWithDetails includes related events and comments.
type IncidentWithDetails struct {
	Incident
	Events   []IncidentEvent   `json:"events"`
	Comments []IncidentComment `json:"comments"`
}

// DedupKey returns the deduplication key for the incident.
// Incidents are deduplicated by rule+service+metric — this prevents
// creating a new incident if an open one already exists for the same rule.
func (a *AnomalyPayload) DedupKey() string {
	return a.Rule + "|" + a.Service + "|" + a.Metric
}
