package core_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

// --- test doubles ---

type mockStorage struct {
	err   error
	saved []core.Batch
}

func (m *mockStorage) Save(_ context.Context, b core.Batch) error {
	if m.err != nil {
		return m.err
	}
	m.saved = append(m.saved, b)
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func validBatch() core.Batch {
	return core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{
			{Name: "cpu_usage", Value: 0.75, Timestamp: time.Now().UTC()},
		},
	}
}

// --- HandleBatch ---

func TestHandleBatch_ValidBatch_Stored(t *testing.T) {
	storage := &mockStorage{}
	svc := core.NewIngestionService(storage, discardLogger())

	if err := svc.HandleBatch(context.Background(), validBatch()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(storage.saved) != 1 {
		t.Fatalf("expected 1 saved batch, got %d", len(storage.saved))
	}
	if storage.saved[0].ReceivedAt.IsZero() {
		t.Error("service must set ReceivedAt on the batch")
	}
}

func TestHandleBatch_StorageFailure_ReturnsError(t *testing.T) {
	svc := core.NewIngestionService(
		&mockStorage{err: errors.New("db unavailable")}, discardLogger(),
	)

	if err := svc.HandleBatch(context.Background(), validBatch()); err == nil {
		t.Fatal("expected error on storage failure")
	}
}

// --- validation table tests ---

func TestHandleBatch_Validation(t *testing.T) {
	svc := core.NewIngestionService(&mockStorage{}, discardLogger())

	cases := []struct {
		name  string
		batch core.Batch
	}{
		{
			name:  "empty agent_id",
			batch: core.Batch{Metrics: []core.Metric{{Name: "x", Value: 1, Timestamp: time.Now()}}},
		},
		{
			name:  "no metrics",
			batch: core.Batch{AgentID: "a"},
		},
		{
			name: "metric missing name",
			batch: core.Batch{
				AgentID: "a",
				Metrics: []core.Metric{{Value: 1, Timestamp: time.Now()}},
			},
		},
		{
			name: "metric zero timestamp",
			batch: core.Batch{
				AgentID: "a",
				Metrics: []core.Metric{{Name: "x", Value: 1}},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := svc.HandleBatch(context.Background(), c.batch)
			var ve *core.ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("expected ValidationError, got %T: %v", err, err)
			}
		})
	}
}
