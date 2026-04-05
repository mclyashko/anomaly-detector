package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// --- mock notifier ---

type mockNotifier struct {
	mu       sync.Mutex
	received []*core.Anomaly
	failErr  error
	called   int
	wg       sync.WaitGroup
}

func (m *mockNotifier) Notify(_ context.Context, anomalies []*core.Anomaly) error {
	m.mu.Lock()
	m.received = append(m.received, anomalies...)
	m.called++
	m.mu.Unlock()
	if m.failErr != nil {
		return m.failErr
	}
	m.wg.Done()
	return nil
}

// --- mock metric fetcher ---

type mockMetricFetcher struct {
	mu         sync.Mutex
	fetched    []core.Metric
	fetchErr   error
	markErr    error
	markCalled bool
	analyzerID string
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

func (m *mockMetricFetcher) MarkAnalyzed(ctx context.Context, analyzerID string, metricIDs []int64) error {
	if m.markErr != nil {
		return m.markErr
	}
	m.mu.Lock()
	m.markCalled = true
	m.mu.Unlock()
	return nil
}

func (m *mockMetricFetcher) IsMarkAnalyzedCalled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.markCalled
}

// --- AnalyzerService tests ---

func TestAnalyzerService_NoMetrics_DoesNotNotify(t *testing.T) {
	engine := core.NewRuleEngine([]core.Rule{}, discardLogger())
	fetcher := &mockMetricFetcher{}
	notifier := &mockNotifier{}
	svc := core.NewAnalyzerService(fetcher, engine, notifier, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	go svc.Run(ctx, 10*time.Millisecond)
	<-ctx.Done()

	if notifier.called != 0 {
		t.Errorf("notifier called %d times, want 0", notifier.called)
	}
}

func TestAnalyzerService_WithAnomalies_Notifies(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.95, Timestamp: ts()},
		},
	}
	notifier := &mockNotifier{}
	svc := core.NewAnalyzerService(fetcher, engine, notifier, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	notifier.wg.Add(1)
	go svc.Run(ctx, 10*time.Millisecond)
	notifier.wg.Wait()

	if notifier.called != 1 {
		t.Fatalf("notifier called %d times, want 1", notifier.called)
	}

	notifier.mu.Lock()
	got := make([]*core.Anomaly, len(notifier.received))
	copy(got, notifier.received)
	notifier.mu.Unlock()

	if len(got) != 1 {
		t.Fatalf("got %d anomalies, want 1", len(got))
	}
	if got[0].Rule != "high-cpu" {
		t.Errorf("rule = %q, want high-cpu", got[0].Rule)
	}
}

func TestAnalyzerService_NotifierFailure_Continues(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.95, Timestamp: ts()},
		},
	}
	notifier := &mockNotifier{failErr: errors.New("connection refused")}
	svc := core.NewAnalyzerService(fetcher, engine, notifier, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Must not panic even if notifier fails.
	go svc.Run(ctx, 10*time.Millisecond)
	<-ctx.Done()
}

func TestAnalyzerService_MarksMetricsAsAnalyzed(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	fetcher := &mockMetricFetcher{
		fetched: []core.Metric{
			{ID: 1, AgentID: "agent-1", Name: "cpu", Value: 0.95, Timestamp: ts()},
		},
	}
	notifier := &mockNotifier{}
	svc := core.NewAnalyzerService(fetcher, engine, notifier, "test-analyzer", 100, discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	notifier.wg.Add(1)
	go svc.Run(ctx, 10*time.Millisecond)
	notifier.wg.Wait()

	if !fetcher.IsMarkAnalyzedCalled() {
		t.Error("expected MarkAnalyzed to be called")
	}
}
