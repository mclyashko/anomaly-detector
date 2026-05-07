package core

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// ErrIncidentNotFound возвращается когда инцидент не найден.
var ErrIncidentNotFound = errors.New("incident not found")

// ErrInvalidStatusTransition возвращается при недопустимой смене статуса.
var ErrInvalidStatusTransition = errors.New("invalid status transition")

// NotifierService управляет жизненным циклом инцидентов.
// Инциденты создаются при обнаружении аномалий и обновляются при поступлении новых данных
// с тем же rule+service+metric (deduplication).
type NotifierService struct {
	repo    IncidentRepository // доступ к данным инцидентов
	channel NotifierChannel     // уведомления (логирование и т.д.)
	logger  *slog.Logger
}

// NewNotifierService создаёт новый сервис.
func NewNotifierService(repo IncidentRepository, channel NotifierChannel, logger *slog.Logger) *NotifierService {
	return &NotifierService{
		repo:    repo,
		channel: channel,
		logger:  logger,
	}
}

// HandleAnomaly обрабатывает аномалию от analyzer.
// Логика:
//   - Ищет существующий открытый инцидент с тем же rule+service+metric (dedup key).
//   - Если нашёл — обновляет его (status=UPDATED) и добавляет событие.
//   - Если не нашёл — создаёт новый инцидент (status=OPEN) и событие в одной транзакции.
// Это гарантирует что новые аномалии по тому же rule+service+metric не создают новых инцидентов.
func (s *NotifierService) HandleAnomaly(ctx context.Context, payload *AnomalyPayload) (*Incident, error) {
	// Check for existing open incident with same dedup key.
	existing, err := s.repo.FindByDedupKey(ctx, payload.Rule, payload.Service, payload.Metric)
	if err != nil && !errors.Is(err, ErrIncidentNotFound) {
		return nil, err
	}

	if existing != nil {
		// Update existing incident.
		existing.Status = StatusUpdated
		existing.UpdatedAt = time.Now().UTC()
		existing.Value = payload.Value
		existing.Forecast = payload.Forecast
		existing.ExpectedValue = payload.Forecast
		existing.LowerCI = payload.LowerCI
		existing.UpperCI = payload.UpperCI
		existing.Message = payload.Message
		if err := s.repo.Update(ctx, existing); err != nil {
			return nil, err
		}
		s.logger.Info("incident updated",
			"incident_id", existing.ID,
			"rule", existing.Rule,
			"service", existing.Service,
		)

		// Append the event (outside transaction — incident already exists).
		event := &IncidentEvent{
			IncidentID: existing.ID,
			Payload:    mustMarshal(payload),
			Timestamp:  timeFromUnix(payload.Timestamp),
			CreatedAt:  time.Now().UTC(),
		}
		if err := s.repo.AddEvent(ctx, existing.ID, event); err != nil {
			return nil, err
		}
		return existing, nil
	}

	// Create new incident atomically with the initial event.
	incident := &Incident{
		Rule:         payload.Rule,
		Service:      payload.Service,
		Metric:       payload.Metric,
		Status:       StatusOpen,
		Severity:     payload.Severity,
		CreatedAt:    time.Now().UTC(),
		UpdatedAt:    time.Now().UTC(),
		Value:        payload.Value,
		Forecast:     payload.Forecast,
		ExpectedValue: payload.Forecast,
		LowerCI:      payload.LowerCI,
		UpperCI:      payload.UpperCI,
		Message:      payload.Message,
	}
	event := &IncidentEvent{
		IncidentID: "", // filled after CreateInTx generates the ID
		Payload:    mustMarshal(payload),
		Timestamp:  timeFromUnix(payload.Timestamp),
		CreatedAt:  time.Now().UTC(),
	}

	// Use a transaction so incident+event are atomic — no compensating delete needed.
	err = s.repo.BeginTx(ctx, func(tx interface{}) error {
		if err := s.repo.CreateInTx(ctx, tx, incident); err != nil {
			return err
		}
		event.IncidentID = incident.ID
		if err := s.repo.AddEventInTx(ctx, tx, incident.ID, event); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	s.logger.Info("incident created",
		"incident_id", incident.ID,
		"rule", incident.Rule,
		"service", incident.Service,
	)

	// Notify after both inserts succeed.
	s.channel.NotifyIncidentCreated(ctx, incident, payload)

	return incident, nil
}

// Delete удаляет инцидент (используется для компенсирующих действий).
func (s *NotifierService) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// Escalate меняет статус инцидента на ESCALATED.
// Нельзя эскалировать уже закрытый инцидент (ErrInvalidStatusTransition).
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

// Resolve закрывает инцидент с резолюцией.
// Устанавливает status=RESOLVED, ResolvedAt=текущее время, Resolution=текст резолюции.
// Нельзя закрыть уже закрытый инцидент (ErrInvalidStatusTransition).
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

// AddComment добавляет комментарий к инциденту.
// Также обновляет UpdatedAt инцидента (чтобы изменить порядок в списке).
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

// GetIncident возвращает инцидент со всеми событиями и комментариями.
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

// ListIncidents возвращает все инциденты, отсортированные по updated_at (сначала свежие).
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
