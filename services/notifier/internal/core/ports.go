package core

import (
	"context"
)

// IncidentRepository defines the storage interface for incidents.
type IncidentRepository interface {
	// Create inserts a new incident.
	Create(ctx context.Context, incident *Incident) error

	// Update modifies an existing incident.
	Update(ctx context.Context, incident *Incident) error

	// FindByID retrieves an incident by its ID.
	FindByID(ctx context.Context, id string) (*Incident, error)

	// FindByDedupKey retrieves an open incident matching the dedup key.
	FindByDedupKey(ctx context.Context, rule, service, metric string) (*Incident, error)

	// List returns all incidents ordered by updated_at descending.
	List(ctx context.Context) ([]Incident, error)

	// AddEvent appends an event to an incident.
	AddEvent(ctx context.Context, incidentID string, event *IncidentEvent) error

	// AddComment appends a comment to an incident.
	AddComment(ctx context.Context, incidentID string, comment *IncidentComment) error

	// GetEvents retrieves all events for an incident.
	GetEvents(ctx context.Context, incidentID string) ([]IncidentEvent, error)

	// GetComments retrieves all comments for an incident.
	GetComments(ctx context.Context, incidentID string) ([]IncidentComment, error)
}

// NotifierChannel defines the notification delivery interface.
type NotifierChannel interface {
	// NotifyIncidentCreated is called when a new incident is created.
	NotifyIncidentCreated(ctx context.Context, incident *Incident, event *AnomalyPayload)

	// NotifyIncidentEscalated is called when an incident is escalated.
	NotifyIncidentEscalated(ctx context.Context, incident *Incident)
}
