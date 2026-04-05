package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/port"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/service"
)

// --- test doubles ---

type mockCollector struct {
	name    string
	metrics []domain.Metric
	err     error
}

func (m *mockCollector) Name() string { return m.name }
func (m *mockCollector) Collect(_ context.Context) ([]domain.Metric, error) {
	return m.metrics, m.err
}

type mockSender struct {
	batches chan domain.Batch
}

func (m *mockSender) Send(_ context.Context, b domain.Batch) error {
	select {
	case m.batches <- b:
	default:
	}
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func oneMetric(name string) domain.Metric {
	return domain.Metric{Name: name, Value: 1.0, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()}
}

// --- tests ---

func TestAgentService_SendsBatch(t *testing.T) {
	col := &mockCollector{name: "col", metrics: []domain.Metric{oneMetric("test.gauge")}}
	snd := &mockSender{batches: make(chan domain.Batch, 1)}

	svc := service.NewAgentService("agent-1", 30*time.Millisecond, []port.Collector{col}, snd, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	select {
	case batch := <-snd.batches:
		if batch.AgentID != "agent-1" {
			t.Errorf("agent_id = %q, want agent-1", batch.AgentID)
		}
		if len(batch.Metrics) != 1 || batch.Metrics[0].Name != "test.gauge" {
			t.Errorf("unexpected metrics: %v", batch.Metrics)
		}
		if batch.CreatedAt.IsZero() {
			t.Error("CreatedAt must not be zero")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for batch")
	}
}

func TestAgentService_FailingCollector_OtherStillRuns(t *testing.T) {
	bad := &mockCollector{name: "bad", err: errors.New("collector down")}
	good := &mockCollector{name: "good", metrics: []domain.Metric{oneMetric("good.metric")}}
	snd := &mockSender{batches: make(chan domain.Batch, 1)}

	svc := service.NewAgentService("agent-1", 30*time.Millisecond, []port.Collector{bad, good}, snd, discardLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Start(ctx)

	select {
	case batch := <-snd.batches:
		if len(batch.Metrics) != 1 || batch.Metrics[0].Name != "good.metric" {
			t.Errorf("unexpected metrics: %v", batch.Metrics)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for batch")
	}
}

func TestAgentService_NoSend_WhenAllCollectorsReturnEmpty(t *testing.T) {
	col := &mockCollector{name: "empty", metrics: nil}
	snd := &mockSender{batches: make(chan domain.Batch, 1)}

	// Run synchronously: ctx timeout drives Start to return.
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	service.NewAgentService("agent-1", 30*time.Millisecond, []port.Collector{col}, snd, discardLogger()).Start(ctx)

	select {
	case <-snd.batches:
		t.Fatal("sender must not be called when no metrics are collected")
	default:
	}
}
