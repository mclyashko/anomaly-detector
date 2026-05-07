package core

import (
	"encoding/json"
	"fmt"
	"time"
)

// IncidentStatus — статус жизненного цикла инцидента.
// OPEN: только создан
// UPDATED: пришли новые данные по тому же инциденту (rule+service+metric)
// ESCALATED: эскалирован
// RESOLVED: закрыт с резолюцией
type IncidentStatus string

const (
	StatusOpen      IncidentStatus = "OPEN"
	StatusUpdated   IncidentStatus = "UPDATED"
	StatusEscalated IncidentStatus = "ESCALATED"
	StatusResolved  IncidentStatus = "RESOLVED"
)

// Incident — аномальный инцидент.
// Value, Forecast, LowerCI, UpperCI, Message — последние ML-данные.
// Сами события (events) хранят полную историю всех аномалий.
type Incident struct {
	ID            string         `json:"id"`
	Rule          string         `json:"rule"`
	Service      string         `json:"service"`
	Metric       string         `json:"metric"`
	Status       IncidentStatus `json:"status"`
	Severity     string         `json:"severity"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty"`
	Resolution   string         `json:"resolution,omitempty"`
	Value        float64        `json:"value,omitempty"`
	Forecast     float64        `json:"forecast,omitempty"`
	ExpectedValue float64       `json:"expected_value,omitempty"`
	LowerCI      float64        `json:"lower_ci,omitempty"`
	UpperCI      float64        `json:"upper_ci,omitempty"`
	Message      string         `json:"message,omitempty"`
}

// IncidentEvent — одно событие аномалии, привязанное к инциденту.
// Payload хранит JSON с полными данными аномалии (включая ML-информацию).
type IncidentEvent struct {
	ID         string          `json:"id"`
	IncidentID string          `json:"incident_id"`
	Payload    json.RawMessage `json:"payload"`
	Timestamp  time.Time       `json:"timestamp"` // время из самой метрики (когда она была)
	CreatedAt  time.Time       `json:"created_at"` // когда событие реально создалось в БД
}

// IncidentComment — комментарий к инциденту.
type IncidentComment struct {
	ID         string    `json:"id"`
	IncidentID string    `json:"incident_id"`
	Text       string    `json:"text"`
	CreatedAt  time.Time `json:"created_at"`
}

// AnomalyPayload — входящее событие от analyzer.
// Используем custom UnmarshalJSON чтобы корректно парсить timestamp из разных форматов
// (RFC3339 строка или Unix timestamp).
type AnomalyPayload struct {
	Rule          string  `json:"rule"`
	Service       string  `json:"service"`
	Metric        string  `json:"metric"`
	MetricID      int64   `json:"metric_id"`
	Value         float64 `json:"value"`
	Severity      string  `json:"severity"`
	Message       string  `json:"message"`
	Timestamp     int64   `json:"timestamp"` // Unix timestamp в секундах
	AgentID       string  `json:"agent_id,omitempty"`
	Condition     string  `json:"condition,omitempty"`
	ID            int64   `json:"id,omitempty"`
	Forecast      float64 `json:"forecast,omitempty"`
	ExpectedValue float64 `json:"expected_value,omitempty"`
	LowerCI       float64 `json:"lower_ci,omitempty"`
	UpperCI       float64 `json:"upper_ci,omitempty"`
}

// SetTimestamp парсит timestamp из разных форматов.
// Приоритет: RFC3339 (2026-05-07T18:04:16Z) → Unix timestamp строка (1746643456).
// Это исправляет баг, когда строка "2026" парсилась как Unix timestamp 2026 секунд
// вместо того чтобы попробовать RFC3339 формат.
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

// UnmarshalJSON распарсивает JSON от analyzer.
// Поддерживает flexible types для всех полей (string/float/int) и
// корректно обрабатывает timestamp в любом формате (RFC3339, Unix, float).
// После парсинга вызывает SetTimestamp для统一 обработки timestamp.
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

// DedupKey возвращает ключ дедупликации для инцидента.
// Инциденты дедуплицируются по rule+service+metric — это позволяет
// не создавать новый инцидент если по тому же правилу уже есть открытый.
func (a *AnomalyPayload) DedupKey() string {
	return a.Rule + "|" + a.Service + "|" + a.Metric
}
