package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// mockRepository implements IncidentRepository for testing.
type mockRepository struct {
	incidents map[string]*Incident
	events    map[string][]IncidentEvent
	comments  map[string][]IncidentComment
	createErr error
	updateErr error
	nextID    int
}

func newMockRepo() *mockRepository {
	return &mockRepository{
		incidents: make(map[string]*Incident),
		events:    make(map[string][]IncidentEvent),
		comments:  make(map[string][]IncidentComment),
	}
}

func (m *mockRepository) nextIncidentID() string {
	m.nextID++
	return fmt.Sprintf("test-id-%d", m.nextID)
}

func (m *mockRepository) Create(ctx context.Context, incident *Incident) error {
	if m.createErr != nil {
		return m.createErr
	}
	incident.ID = m.nextIncidentID()
	incident.CreatedAt = time.Now().UTC()
	incident.UpdatedAt = incident.CreatedAt
	m.incidents[incident.ID] = incident
	return nil
}

func (m *mockRepository) Update(ctx context.Context, incident *Incident) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	m.incidents[incident.ID] = incident
	return nil
}

func (m *mockRepository) FindByID(ctx context.Context, id string) (*Incident, error) {
	if inc, ok := m.incidents[id]; ok {
		return inc, nil
	}
	return nil, ErrIncidentNotFound
}

func (m *mockRepository) FindByDedupKey(ctx context.Context, rule, service, metric string) (*Incident, error) {
	for _, inc := range m.incidents {
		if inc.Rule == rule && inc.Service == service && inc.Metric == metric {
			return inc, nil
		}
	}
	return nil, ErrIncidentNotFound
}

func (m *mockRepository) List(ctx context.Context) ([]Incident, error) {
	var result []Incident
	for _, inc := range m.incidents {
		result = append(result, *inc)
	}
	return result, nil
}

func (m *mockRepository) ListFiltered(ctx context.Context, filters IncidentFilters, page, pageSize int) ([]Incident, int, error) {
	var result []Incident
	for _, inc := range m.incidents {
		if len(filters.Status) > 0 {
			found := false
			for _, s := range filters.Status {
				if inc.Status == s {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(filters.Severity) > 0 {
			found := false
			for _, sev := range filters.Severity {
				if inc.Severity == sev {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, *inc)
	}
	// Apply pagination.
	total := len(result)
	start := (page - 1) * pageSize
	if start >= total {
		return []Incident{}, total, nil
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (m *mockRepository) AddEvent(ctx context.Context, incidentID string, event *IncidentEvent) error {
	event.ID = "event-1"
	event.CreatedAt = time.Now().UTC()
	m.events[incidentID] = append(m.events[incidentID], *event)
	return nil
}

func (m *mockRepository) AddComment(ctx context.Context, incidentID string, comment *IncidentComment) error {
	comment.ID = "comment-1"
	comment.CreatedAt = time.Now().UTC()
	m.comments[incidentID] = append(m.comments[incidentID], *comment)
	return nil
}

func (m *mockRepository) GetEvents(ctx context.Context, incidentID string) ([]IncidentEvent, error) {
	return m.events[incidentID], nil
}

func (m *mockRepository) GetComments(ctx context.Context, incidentID string) ([]IncidentComment, error) {
	return m.comments[incidentID], nil
}

// txMock is a minimal transaction mock that implements pgx.Tx for testing.
// It only implements the methods that the test code actually calls:
// Begin, Commit, Rollback, Exec.
type txMock struct{}

func (txMock) Begin(ctx context.Context) (txMock, error) { return txMock{}, nil }
func (txMock) Commit(ctx context.Context) error          { return nil }
func (txMock) Rollback(ctx context.Context) error        { return nil }
func (txMock) Exec(ctx context.Context, sql string, arguments ...any) (interface{ RowsAffected() int64 }, error) {
	return nil, nil
}

func (m *mockRepository) Delete(ctx context.Context, id string) error {
	delete(m.incidents, id)
	return nil
}

func (m *mockRepository) BeginTx(ctx context.Context, fn func(tx interface{}) error) error {
	return fn(txMock{})
}

func (m *mockRepository) CreateInTx(ctx context.Context, tx interface{}, incident *Incident) error {
	return m.Create(ctx, incident)
}

func (m *mockRepository) AddEventInTx(ctx context.Context, tx interface{}, incidentID string, event *IncidentEvent) error {
	return m.AddEvent(ctx, incidentID, event)
}

// mockChannel implements NotifierChannel for testing.
type mockChannel struct {
	created   int
	escalated int
}

func (m *mockChannel) NotifyIncidentCreated(ctx context.Context, incident *Incident, payload *AnomalyPayload) {
	m.created++
}

func (m *mockChannel) NotifyIncidentEscalated(ctx context.Context, incident *Incident) {
	m.escalated++
}

func TestNotifierService_HandleAnomaly_CreatesNewIncident(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Message:   "CPU too high",
		Timestamp: time.Now().Unix(),
	}

	incident, err := svc.HandleAnomaly(context.Background(), payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if incident.ID == "" {
		t.Error("expected incident ID to be set")
	}
	if incident.Rule != "high_cpu" {
		t.Errorf("rule = %q, want high_cpu", incident.Rule)
	}
	if incident.Status != StatusOpen {
		t.Errorf("status = %v, want OPEN", incident.Status)
	}
	if ch.created != 1 {
		t.Errorf("channel created count = %d, want 1", ch.created)
	}
}

func TestNotifierService_HandleAnomaly_UpdatesExistingIncident(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create first anomaly
	payload1 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Message:   "CPU too high",
		Timestamp: time.Now().Unix(),
	}

	incident1, err := svc.HandleAnomaly(context.Background(), payload1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Send second anomaly - should update existing incident
	payload2 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.95,
		Severity:  "critical",
		Message:   "CPU even higher",
		Timestamp: time.Now().Unix(),
	}

	incident2, err := svc.HandleAnomaly(context.Background(), payload2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if incident1.ID != incident2.ID {
		t.Errorf("expected same incident ID, got %s and %s", incident1.ID, incident2.ID)
	}
	if incident2.Status != StatusUpdated {
		t.Errorf("status = %v, want UPDATED", incident2.Status)
	}
	if ch.created != 1 {
		t.Errorf("channel created count = %d, want 1 (no new incident)", ch.created)
	}
}

func TestNotifierService_Escalate(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create incident
	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)

	// Escalate
	escalated, err := svc.Escalate(context.Background(), incident.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if escalated.Status != StatusEscalated {
		t.Errorf("status = %v, want ESCALATED", escalated.Status)
	}
	if ch.escalated != 1 {
		t.Errorf("channel escalated count = %d, want 1", ch.escalated)
	}
}

func TestNotifierService_Resolve(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create incident
	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)

	// Resolve
	resolved, err := svc.Resolve(context.Background(), incident.ID, "Restarted the service")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resolved.Status != StatusResolved {
		t.Errorf("status = %v, want RESOLVED", resolved.Status)
	}
	if resolved.Resolution != "Restarted the service" {
		t.Errorf("resolution = %q, want 'Restarted the service'", resolved.Resolution)
	}
}

func TestNotifierService_Resolve_AlreadyResolved(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create and resolve
	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)
	svc.Resolve(context.Background(), incident.ID, "Fixed")

	// Try to resolve again
	_, err := svc.Resolve(context.Background(), incident.ID, "Another resolution")
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Errorf("expected ErrInvalidStatusTransition, got %v", err)
	}
}

func TestNotifierService_AddComment(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create incident
	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)

	// Add comment
	comment, err := svc.AddComment(context.Background(), incident.ID, "Investigating this issue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if comment.Text != "Investigating this issue" {
		t.Errorf("text = %q, want 'Investigating this issue'", comment.Text)
	}
}

func TestNotifierService_AddComment_IncidentNotFound(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	_, err := svc.AddComment(context.Background(), "nonexistent-id", "comment")
	if !errors.Is(err, ErrIncidentNotFound) {
		t.Errorf("expected ErrIncidentNotFound, got %v", err)
	}
}

func TestNotifierService_GetIncident(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create incident with event
	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)

	// Add comment
	svc.AddComment(context.Background(), incident.ID, "Test comment")

	// Get incident with details
	details, err := svc.GetIncident(context.Background(), incident.ID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(details.Events) != 1 {
		t.Errorf("events count = %d, want 1", len(details.Events))
	}
	if len(details.Comments) != 1 {
		t.Errorf("comments count = %d, want 1", len(details.Comments))
	}
}

func TestNotifierService_ListIncidents(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	// Create two incidents
	payload1 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	payload2 := &AnomalyPayload{
		Rule:      "high_memory",
		Service:   "api-service",
		Metric:    "memory_usage",
		Severity:  "warning",
		Timestamp: time.Now().Unix(),
	}
	svc.HandleAnomaly(context.Background(), payload1)
	svc.HandleAnomaly(context.Background(), payload2)

	incidents, err := svc.ListIncidents(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(incidents) != 2 {
		t.Errorf("incidents count = %d, want 2", len(incidents))
	}
}

func TestNotifierService_HandleAnomaly_EscalatedIncident_NotDowngraded(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)
	svc.Escalate(context.Background(), incident.ID)

	// Send another anomaly — ESCALATED must stay ESCALATED.
	newPayload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.95,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	result, err := svc.HandleAnomaly(context.Background(), newPayload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != StatusEscalated {
		t.Errorf("status = %v, want ESCALATED (must not be downgraded)", result.Status)
	}
	if ch.escalated != 1 {
		t.Errorf("escalated notifications = %d, want 1 (no new notification for ESCALATED)", ch.escalated)
	}
}

func TestNotifierService_HandleAnomaly_ResolvedIncident_Reopened(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)
	svc.Resolve(context.Background(), incident.ID, "Fixed")

	// New anomaly for same dedup key — RESOLVED must become OPEN.
	newPayload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.99,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	result, err := svc.HandleAnomaly(context.Background(), newPayload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Status != StatusOpen {
		t.Errorf("status = %v, want OPEN (resolved incident must reopen)", result.Status)
	}
	if result.ID != incident.ID {
		t.Errorf("incident ID changed: got %s, want %s", result.ID, incident.ID)
	}
}

func TestNotifierService_HandleAnomaly_OpenIncident_Updated(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload1 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.92,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload1)

	// Second anomaly on OPEN incident must become UPDATED.
	payload2 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Value:     0.95,
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	result, err := svc.HandleAnomaly(context.Background(), payload2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ID != incident.ID {
		t.Errorf("incident ID changed: got %s, want %s", result.ID, incident.ID)
	}
	if result.Status != StatusUpdated {
		t.Errorf("status = %v, want UPDATED", result.Status)
	}
}

func TestNotifierService_Escalate_AlreadyResolved(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)
	svc.Resolve(context.Background(), incident.ID, "Fixed")

	// Cannot escalate a RESOLVED incident.
	_, err := svc.Escalate(context.Background(), incident.ID)
	if !errors.Is(err, ErrInvalidStatusTransition) {
		t.Errorf("expected ErrInvalidStatusTransition, got %v", err)
	}
}

func TestNotifierService_Resolve_FromEscalated(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident, _ := svc.HandleAnomaly(context.Background(), payload)
	svc.Escalate(context.Background(), incident.ID)

	// Can resolve from ESCALATED.
	resolved, err := svc.Resolve(context.Background(), incident.ID, "Manually resolved")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Status != StatusResolved {
		t.Errorf("status = %v, want RESOLVED", resolved.Status)
	}
}

func TestNotifierService_HandleAnomaly_DifferentMetric_NewIncident(t *testing.T) {
	repo := newMockRepo()
	ch := &mockChannel{}
	logger := slog.Default()
	svc := NewNotifierService(repo, ch, logger)

	payload1 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "cpu_usage",
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident1, _ := svc.HandleAnomaly(context.Background(), payload1)

	// Same rule+service, different metric — must create separate incident.
	payload2 := &AnomalyPayload{
		Rule:      "high_cpu",
		Service:   "auth-service",
		Metric:    "memory_usage",
		Severity:  "critical",
		Timestamp: time.Now().Unix(),
	}
	incident2, err := svc.HandleAnomaly(context.Background(), payload2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if incident1.ID == incident2.ID {
		t.Errorf("expected different incident IDs for different metrics, got same: %s", incident1.ID)
	}
	if ch.created != 2 {
		t.Errorf("channel created count = %d, want 2", ch.created)
	}
}
