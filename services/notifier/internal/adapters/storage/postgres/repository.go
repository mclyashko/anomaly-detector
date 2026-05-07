package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// Repository implements core.IncidentRepository using PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository creates a new PostgreSQL repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// BeginTx starts a new database transaction and passes it to fn.
// If fn returns an error the transaction is rolled back; otherwise it is committed.
func (r *Repository) BeginTx(ctx context.Context, fn func(tx interface{}) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("rollback failed: %v (original error: %w)", rbErr, err)
		}
		return err
	}
	return tx.Commit(ctx)
}

// Create inserts a new incident.
func (r *Repository) Create(ctx context.Context, incident *core.Incident) error {
	query := `
		INSERT INTO incidents (id, rule, service, metric, status, severity, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	incident.ID = newUUID()
	_, err := r.pool.Exec(ctx, query,
		incident.ID,
		incident.Rule,
		incident.Service,
		incident.Metric,
		incident.Status,
		incident.Severity,
		incident.CreatedAt,
		incident.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create incident: %w", err)
	}
	return nil
}

// Update modifies an existing incident.
func (r *Repository) Update(ctx context.Context, incident *core.Incident) error {
	query := `
		UPDATE incidents
		SET status = $2, updated_at = $3, resolved_at = $4, resolution = $5
		WHERE id = $1
	`
	result, err := r.pool.Exec(ctx, query,
		incident.ID,
		incident.Status,
		incident.UpdatedAt,
		incident.ResolvedAt,
		incident.Resolution,
	)
	if err != nil {
		return fmt.Errorf("update incident: %w", err)
	}
	if result.RowsAffected() == 0 {
		return core.ErrIncidentNotFound
	}
	return nil
}

// FindByID retrieves an incident by ID.
func (r *Repository) FindByID(ctx context.Context, id string) (*core.Incident, error) {
	query := `
		SELECT id, rule, service, metric, status, severity, created_at, updated_at, resolved_at, resolution
		FROM incidents
		WHERE id = $1
	`
	row := r.pool.QueryRow(ctx, query, id)
	return r.scanIncident(row)
}

// FindByDedupKey retrieves an open incident by dedup key.
func (r *Repository) FindByDedupKey(ctx context.Context, rule, service, metric string) (*core.Incident, error) {
	query := `
		SELECT id, rule, service, metric, status, severity, created_at, updated_at, resolved_at, resolution
		FROM incidents
		WHERE rule = $1 AND service = $2 AND metric = $3 AND status != 'RESOLVED'
		ORDER BY created_at DESC
		LIMIT 1
	`
	row := r.pool.QueryRow(ctx, query, rule, service, metric)
	return r.scanIncident(row)
}

// List returns all incidents ordered by updated_at descending.
func (r *Repository) List(ctx context.Context) ([]core.Incident, error) {
	query := `
		SELECT id, rule, service, metric, status, severity, created_at, updated_at, resolved_at, resolution
		FROM incidents
		ORDER BY updated_at DESC
	`
	rows, err := r.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list incidents: %w", err)
	}
	defer rows.Close()

	var incidents []core.Incident
	for rows.Next() {
		inc, err := r.scanIncidentRows(rows)
		if err != nil {
			return nil, err
		}
		incidents = append(incidents, *inc)
	}
	return incidents, nil
}

// CreateInTx inserts a new incident using the provided transaction.
func (r *Repository) CreateInTx(ctx context.Context, txType interface{}, incident *core.Incident) error {
	tx := txType.(pgx.Tx)
	query := `
		INSERT INTO incidents (id, rule, service, metric, status, severity, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	incident.ID = newUUID()
	_, err := tx.Exec(ctx, query,
		incident.ID,
		incident.Rule,
		incident.Service,
		incident.Metric,
		incident.Status,
		incident.Severity,
		incident.CreatedAt,
		incident.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create incident: %w", err)
	}
	return nil
}

// AddEvent appends an event to an incident.
func (r *Repository) AddEvent(ctx context.Context, incidentID string, event *core.IncidentEvent) error {
	query := `
		INSERT INTO incident_events (id, incident_id, payload, timestamp, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	event.ID = newUUID()
	_, err := r.pool.Exec(ctx, query,
		event.ID,
		incidentID,
		event.Payload,
		event.Timestamp,
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("add event: %w", err)
	}
	return nil
}

// AddEventInTx appends an event to an incident using the provided transaction.
func (r *Repository) AddEventInTx(ctx context.Context, txType interface{}, incidentID string, event *core.IncidentEvent) error {
	tx := txType.(pgx.Tx)
	query := `
		INSERT INTO incident_events (id, incident_id, payload, timestamp, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`
	event.ID = newUUID()
	_, err := tx.Exec(ctx, query,
		event.ID,
		incidentID,
		event.Payload,
		event.Timestamp,
		event.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("add event: %w", err)
	}
	return nil
}

// AddComment appends a comment to an incident.
func (r *Repository) AddComment(ctx context.Context, incidentID string, comment *core.IncidentComment) error {
	query := `
		INSERT INTO incident_comments (id, incident_id, text, created_at)
		VALUES ($1, $2, $3, $4)
	`
	comment.ID = newUUID()
	_, err := r.pool.Exec(ctx, query,
		comment.ID,
		incidentID,
		comment.Text,
		comment.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("add comment: %w", err)
	}
	return nil
}

// GetEvents retrieves all events for an incident.
func (r *Repository) GetEvents(ctx context.Context, incidentID string) ([]core.IncidentEvent, error) {
	query := `
		SELECT id, incident_id, payload, timestamp, created_at
		FROM incident_events
		WHERE incident_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query, incidentID)
	if err != nil {
		return nil, fmt.Errorf("get events: %w", err)
	}
	defer rows.Close()

	var events []core.IncidentEvent
	for rows.Next() {
		var e core.IncidentEvent
		if err := rows.Scan(&e.ID, &e.IncidentID, &e.Payload, &e.Timestamp, &e.CreatedAt); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, nil
}

// GetComments retrieves all comments for an incident.
func (r *Repository) GetComments(ctx context.Context, incidentID string) ([]core.IncidentComment, error) {
	query := `
		SELECT id, incident_id, text, created_at
		FROM incident_comments
		WHERE incident_id = $1
		ORDER BY created_at ASC
	`
	rows, err := r.pool.Query(ctx, query, incidentID)
	if err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}
	defer rows.Close()

	var comments []core.IncidentComment
	for rows.Next() {
		var c core.IncidentComment
		if err := rows.Scan(&c.ID, &c.IncidentID, &c.Text, &c.CreatedAt); err != nil {
			return nil, err
		}
		comments = append(comments, c)
	}
	return comments, nil
}

// Delete removes an incident and its events/comments by ID (used for compensating actions).
func (r *Repository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM incidents WHERE id = $1`
	_, err := r.pool.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("delete incident: %w", err)
	}
	return nil
}

func (r *Repository) scanIncident(row pgx.Row) (*core.Incident, error) {
	var inc core.Incident
	var resolvedAt *time.Time
	var resolution sql.NullString
	err := row.Scan(
		&inc.ID,
		&inc.Rule,
		&inc.Service,
		&inc.Metric,
		&inc.Status,
		&inc.Severity,
		&inc.CreatedAt,
		&inc.UpdatedAt,
		&resolvedAt,
		&resolution,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, core.ErrIncidentNotFound
		}
		return nil, fmt.Errorf("scan incident: %w", err)
	}
	inc.ResolvedAt = resolvedAt
	if resolution.Valid {
		inc.Resolution = resolution.String
	}
	return &inc, nil
}

func (r *Repository) scanIncidentRows(rows pgx.Rows) (*core.Incident, error) {
	var inc core.Incident
	var resolvedAt *time.Time
	var resolution sql.NullString
	err := rows.Scan(
		&inc.ID,
		&inc.Rule,
		&inc.Service,
		&inc.Metric,
		&inc.Status,
		&inc.Severity,
		&inc.CreatedAt,
		&inc.UpdatedAt,
		&resolvedAt,
		&resolution,
	)
	if err != nil {
		return nil, fmt.Errorf("scan incident: %w", err)
	}
	inc.ResolvedAt = resolvedAt
	if resolution.Valid {
		inc.Resolution = resolution.String
	}
	return &inc, nil
}

// newUUID generates a new UUID string.
func newUUID() string {
	return uuid.New().String()
}
