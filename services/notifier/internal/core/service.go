package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// ErrIncidentNotFound is returned when an incident does not exist.
var ErrIncidentNotFound = errors.New("incident not found")

// ErrInvalidStatusTransition is returned when a status transition is not allowed.
var ErrInvalidStatusTransition = errors.New("invalid status transition")

// NotifierService handles incident lifecycle management.
type NotifierService struct {
	repo    IncidentRepository
	channel NotifierChannel
	logger  *slog.Logger
}

// NewNotifierService creates a new notifier service.
func NewNotifierService(repo IncidentRepository, channel NotifierChannel, logger *slog.Logger) *NotifierService {
	return &NotifierService{
		repo:    repo,
		channel: channel,
		logger:  logger,
	}
}

// HandleAnomaly processes an anomaly event, creating or updating an incident.
func (s *NotifierService) HandleAnomaly(ctx context.Context, payload *AnomalyPayload) (*Incident, error) {
	// Check for existing open incident with same dedup key.
	existing, err := s.repo.FindByDedupKey(ctx, payload.Rule, payload.Service, payload.Metric)
	if err != nil && !errors.Is(err, ErrIncidentNotFound) {
		return nil, err
	}

	var incident *Incident
	if existing != nil {
		// Update existing incident.
		incident = existing
		incident.Status = StatusUpdated
		incident.UpdatedAt = time.Now().UTC()

		if err := s.repo.Update(ctx, incident); err != nil {
			return nil, err
		}
		s.logger.Info("incident updated",
			"incident_id", incident.ID,
			"rule", incident.Rule,
			"service", incident.Service,
		)
	} else {
		// Create new incident.
		incident = &Incident{
			Rule:      payload.Rule,
			Service:   payload.Service,
			Metric:    payload.Metric,
			Status:    StatusOpen,
			Severity:  payload.Severity,
			CreatedAt: time.Now().UTC(),
			UpdatedAt: time.Now().UTC(),
		}
		if err := s.repo.Create(ctx, incident); err != nil {
			return nil, err
		}
		s.logger.Info("incident created",
			"incident_id", incident.ID,
			"rule", incident.Rule,
			"service", incident.Service,
		)

		// Notify on creation.
		s.channel.NotifyIncidentCreated(ctx, incident, payload)
	}

	// Append the anomaly event.
	event := &IncidentEvent{
		IncidentID: incident.ID,
		Payload:    mustMarshal(payload),
		Timestamp:  timeFromUnix(payload.Timestamp),
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.repo.AddEvent(ctx, incident.ID, event); err != nil {
		return nil, err
	}

	return incident, nil
}

// Escalate changes the incident status to ESCALATED.
func (s *NotifierService) Escalate(ctx context.Context, id string) (*Incident, error) {
	incident, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if incident.Status == StatusResolved {
		return nil, ErrInvalidStatusTransition
	}

	incident.Status = StatusEscalated
	incident.UpdatedAt = time.Now().UTC()

	if err := s.repo.Update(ctx, incident); err != nil {
		return nil, err
	}

	s.logger.Info("incident escalated",
		"incident_id", incident.ID,
		"rule", incident.Rule,
	)

	// Notify on escalation.
	s.channel.NotifyIncidentEscalated(ctx, incident)

	return incident, nil
}

// Resolve changes the incident status to RESOLVED with a resolution message.
func (s *NotifierService) Resolve(ctx context.Context, id, resolution string) (*Incident, error) {
	incident, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if incident.Status == StatusResolved {
		return nil, ErrInvalidStatusTransition
	}

	now := time.Now().UTC()
	incident.Status = StatusResolved
	incident.ResolvedAt = &now
	incident.Resolution = resolution
	incident.UpdatedAt = now

	if err := s.repo.Update(ctx, incident); err != nil {
		return nil, err
	}

	s.logger.Info("incident resolved",
		"incident_id", incident.ID,
		"resolution", resolution,
	)

	return incident, nil
}

// AddComment adds a comment to an incident.
func (s *NotifierService) AddComment(ctx context.Context, id, text string) (*IncidentComment, error) {
	// Verify incident exists.
	if _, err := s.repo.FindByID(ctx, id); err != nil {
		return nil, err
	}

	comment := &IncidentComment{
		IncidentID: id,
		Text:       text,
		CreatedAt:  time.Now().UTC(),
	}
	if err := s.repo.AddComment(ctx, id, comment); err != nil {
		return nil, err
	}

	// Update incident timestamp.
	incident, err := s.repo.FindByID(ctx, id)
	if err != nil {
		s.logger.Warn("failed to refresh incident timestamp", "err", err, "id", id)
	} else if incident != nil {
		incident.UpdatedAt = time.Now().UTC()
		if err := s.repo.Update(ctx, incident); err != nil {
			s.logger.Warn("failed to update incident timestamp", "err", err, "id", id)
		}
	}

	return comment, nil
}

// GetIncident retrieves a single incident with events and comments.
func (s *NotifierService) GetIncident(ctx context.Context, id string) (*IncidentWithDetails, error) {
	incident, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	events, err := s.repo.GetEvents(ctx, id)
	if err != nil {
		return nil, err
	}

	comments, err := s.repo.GetComments(ctx, id)
	if err != nil {
		return nil, err
	}

	return &IncidentWithDetails{
		Incident: *incident,
		Events:   events,
		Comments: comments,
	}, nil
}

// ListIncidents returns all incidents ordered by updated_at descending.
func (s *NotifierService) ListIncidents(ctx context.Context) ([]Incident, error) {
	return s.repo.List(ctx)
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func timeFromUnix(ts int64) time.Time {
	if ts == 0 {
		return time.Now().UTC()
	}
	return time.Unix(ts, 0).UTC()
}
