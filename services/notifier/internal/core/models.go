package core

import (
	"encoding/json"
	"fmt"
	"time"
)

// IncidentStatus represents the lifecycle state of an incident.
type IncidentStatus string

const (
	StatusOpen      IncidentStatus = "OPEN"
	StatusUpdated   IncidentStatus = "UPDATED"
	StatusEscalated IncidentStatus = "ESCALATED"
	StatusResolved  IncidentStatus = "RESOLVED"
)

// Incident represents a tracked anomaly incident.
type Incident struct {
	ID         string         `json:"id"`
	Rule       string         `json:"rule"`
	Service    string         `json:"service"`
	Metric     string         `json:"metric"`
	Status     IncidentStatus `json:"status"`
	Severity   string         `json:"severity"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
	ResolvedAt *time.Time     `json:"resolved_at,omitempty"`
	Resolution string         `json:"resolution,omitempty"`
}

// IncidentEvent represents an anomaly event attached to an incident.
type IncidentEvent struct {
	ID         string          `json:"id"`
	IncidentID string          `json:"incident_id"`
	Payload    json.RawMessage `json:"payload"`
	Timestamp  time.Time       `json:"timestamp"`
	CreatedAt  time.Time       `json:"created_at"`
}

// IncidentComment represents a comment on an incident.
type IncidentComment struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

// AnomalyPayload is the incoming event from the analyzer.
type AnomalyPayload struct {
	Rule      string  `json:"rule"`
	Service   string  `json:"service"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Severity  string  `json:"severity"`
	Message   string  `json:"message"`
	Timestamp int64   `json:"timestamp"`
}

// TimestampSetter is implemented by types that can parse timestamps from various formats.
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
		// Try parsing as Unix timestamp first.
		var t int64
		if _, err := fmt.Sscanf(val, "%d", &t); err == nil {
			a.Timestamp = t
			return nil
		}
		// Fallback: try parsing as time.RFC3339.
		if tm, err := time.Parse(time.RFC3339, val); err == nil {
			a.Timestamp = tm.Unix()
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

func (a *AnomalyPayload) UnmarshalJSON(data []byte) error {
	type rawPayload struct {
		Rule      any `json:"rule"`
		Service   any `json:"service"`
		Metric    any `json:"metric"`
		Value     any `json:"value"`
		Severity  any `json:"severity"`
		Message   any `json:"message"`
		Timestamp any `json:"timestamp"`
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
		return fmt.Sprintf("%v", val)
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
	case nil:
		return 0
	default:
		return 0
	}
}

// IncidentWithDetails includes related events and comments.
type IncidentWithDetails struct {
	Incident
	Events   []IncidentEvent   `json:"events"`
	Comments []IncidentComment `json:"comments"`
}

// DedupKey returns the deduplication key for an incident.
func (a *AnomalyPayload) DedupKey() string {
	return a.Rule + "|" + a.Service + "|" + a.Metric
}
