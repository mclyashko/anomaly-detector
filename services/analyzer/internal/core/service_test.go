package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// --- mock producer ---

type mockProducer struct {
	mu        sync.Mutex
	received  []*core.Anomaly
	produceErr error
	called    int
	wg        sync.WaitGroup
}

func (m *mockProducer) Produce(_ context.Context, anomalies []*core.Anomaly) error {
	m.mu.Lock()
	m.received = append(m.received, anomalies...)
	m.called++
	m.mu.Unlock()
	if m.produceErr != nil {
		return m.produceErr
	}
	m.wg.Done()
	return nil
}

// --- mock metric fetcher ---

type mockMetricFetcher struct {
	mu          sync.Mutex
	fetched     []core.Metric
	fetchErr    error
	markErr     error
	markRows    int64
	markCalled  bool
	analyzerID  string
}

func (m *mockMetricFetcher) FetchUnanalyzed(ctx context.Context, analyzerID string, limit int) ([]core.Metric, error) {
	m.mu.Lock()
	m.analyzerID = analyzerID
	m.mu.Unlock()
	if m.fetchErr != nil {
		return nil, m.fetchErr
	}
	return m.fetched, nil
}

func (m *mockMetricFetcher) MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) (int64, error) {
	if m.markErr != nil {
		return 0, m.markErr
	}
	m.mu.Lock()
	m.markCalled = true
	// Default: mark all provided IDs as success.
	if m.markRows == 0 {
		m.markRows = int64(len(metricIDs))
	}
	affected := m.markRows
	m.mu.Unlock()
	return affected, nil
}

func (m *mockMetricFetcher) IsMarkAnalyzedCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.markCalled
}

// --- AnalyzerService tests ---

func TestAnalyzerService_NoMetrics_DoesNotProduce(t *testing.T) {
	engine := core.NewRuleEngine([]core.Rule{}, discardLogger(), nil)
	fetcher := &mockMetricFetcher{}
	producer := &mockProducer{}
	svc := core.NewAnalyzerService(fetcher, engine, producer, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	go svc.Run(ctx, 10*time.Millisecond)
	<-ctx.Done()

	if producer.called != 0 {
		t.Errorf("producer called %d times, want 0", producer.called)
	}
}

func TestAnalyzerService_WithAnomalies_Produces(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.95, Timestamp: ts()},
		},
	}
	producer := &mockProducer{}
	svc := core.NewAnalyzerService(fetcher, engine, producer, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	producer.wg.Add(1)
	go svc.Run(ctx, 10*time.Millisecond)
	producer.wg.Wait()

	if producer.called != 1 {
		t.Fatalf("producer called %d times, want 1", producer.called)
	}

	producer.mu.Lock()
	got := make([]*core.Anomaly, len(producer.received))
	copy(got, producer.received)
	producer.mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("got %d anomalies, want 1", len(got))
	}
	if got[0].Rule != "high-cpu" {
		t.Errorf("rule = %q, want high-cpu", got[0].Rule)
	}
}

func TestAnalyzerService_ProducerFailure_Continues(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.95, Timestamp: ts()},
		},
	}
	producer := &mockProducer{produceErr: errors.New("connection refused")}
	svc := core.NewAnalyzerService(fetcher, engine, producer, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Must not panic even if producer fails.
	go svc.Run(ctx, 10*time.Millisecond)
	<-ctx.Done()
}

func TestAnalyzerService_MarksMetricsAsAnalyzed(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.5, Timestamp: ts()}, // value=0.5 < 0.8 → normal
		},
	}
	producer := &mockProducer{}
	svc := core.NewAnalyzerService(fetcher, engine, producer, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go svc.Run(ctx, 10*time.Millisecond)
	<-ctx.Done()

	if !fetcher.IsMarkAnalyzedCalled() {
		t.Error("expected MarkAnalyzed to be called")
	}
}
