package log

import (
	"context"
	"log/slog"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// Channel implements core.NotifierChannel using structured logging.
type Channel struct {
	logger *slog.Logger
}

// New creates a new log channel.
func New(logger *slog.Logger) *Channel {
	return &Channel{logger: logger}
}

// NotifyIncidentCreated logs incident creation.
func (c *Channel) NotifyIncidentCreated(ctx context.Context, incident *core.Incident, payload *core.AnomalyPayload) {
	c.logger.Info("INCIDENT CREATED",
		"incident_id", incident.ID,
		"rule", incident.Rule,
		"service", incident.Service,
		"metric", incident.Metric,
		"severity", incident.Severity,
		"message", payload.Message,
		"value", payload.Value,
	)
}

// NotifyIncidentEscalated logs incident escalation.
func (c *Channel) NotifyIncidentEscalated(ctx context.Context, incident *core.Incident) {
	c.logger.Warn("INCIDENT ESCALATED",
		"incident_id", incident.ID,
		"rule", incident.Rule,
		"service", incident.Service,
		"metric", incident.Metric,
		"severity", incident.Severity,
	)
}
