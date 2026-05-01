package core_test

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ts() time.Time { return time.Now().UTC() }

func metric(name string, value float64, agentID string) core.Metric {
	return core.Metric{Name: name, Value: value, Timestamp: ts(), AgentID: agentID}
}

// --- RuleEngine.Evaluate ---

func TestRuleEngine_Evaluate_FiresAnomaly(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	anomalies := engine.Evaluate(metric("cpu", 0.95, "agent-1"))
	if len(anomalies) != 1 {
		t.Fatalf("got %d anomalies, want 1", len(anomalies))
	}
	a := anomalies[0]
	if a.Rule != "high-cpu" {
		t.Errorf("rule = %q, want high-cpu", a.Rule)
	}
	if a.Value != 0.95 {
		t.Errorf("value = %v, want 0.95", a.Value)
	}
	if a.AgentID != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", a.AgentID)
	}
}

func TestRuleEngine_Evaluate_NoMatch(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	if len(engine.Evaluate(metric("cpu", 0.5, "agent-1"))) != 0 {
		t.Error("expected no anomalies for normal value")
	}
}

func TestRuleEngine_Evaluate_WrongMetric(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	if len(engine.Evaluate(metric("memory", 0.99, "agent-1"))) != 0 {
		t.Error("expected no anomalies for non-matching metric name")
	}
}

// --- RuleEngine.EvaluateBatch ---

func TestRuleEngine_EvaluateBatch_MultipleAnomalies(t *testing.T) {
	cpuCfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	memCfg := core.RuleConfig{Name: "high-mem", Metric: "memory", Type: core.RuleTypeThreshold, Condition: "value > 0.9", Severity: core.SeverityCritical}
	cpuRule, _ := core.NewThresholdRule(cpuCfg)
	memRule, _ := core.NewThresholdRule(memCfg)
	engine := core.NewRuleEngine([]core.Rule{cpuRule, memRule}, discardLogger(), nil)

	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{
			{Name: "cpu", Value: 0.95, Timestamp: ts()},
			{Name: "memory", Value: 0.99, Timestamp: ts()},
			{Name: "cpu", Value: 0.3, Timestamp: ts()},
		},
	}

	anomalies := engine.EvaluateBatch(batch)
	if len(anomalies) != 2 {
		t.Errorf("got %d anomalies, want 2", len(anomalies))
	}
}

func TestRuleEngine_EvaluateBatch_NoAnomalies(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{
			{Name: "cpu", Value: 0.5, Timestamp: ts()},
			{Name: "cpu", Value: 0.3, Timestamp: ts()},
		},
	}

	if len(engine.EvaluateBatch(batch)) != 0 {
		t.Error("expected no anomalies")
	}
}

func TestRuleEngine_EvaluateBatch_ParallelPath(t *testing.T) {
	cfg := core.RuleConfig{Name: "high", Metric: "m", Type: core.RuleTypeThreshold, Condition: "value > 0", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	// Create a batch large enough to trigger parallel evaluation (>= 500 metrics).
	// batchParallelThreshold = 500, chunkSize = 200
	metrics := make([]core.Metric, 600)
	for i := range metrics {
		metrics[i] = core.Metric{Name: "m", Value: 1.0, Timestamp: ts(), AgentID: "agent-parallel"} // all fire
	}

	batch := core.Batch{
		AgentID: "agent-parallel",
		Metrics: metrics,
	}

	anomalies := engine.EvaluateBatch(batch)
	if len(anomalies) != 600 {
		t.Errorf("got %d anomalies, want 600", len(anomalies))
	}

	// Verify all have correct agentID
	for i, a := range anomalies {
		if a.AgentID != "agent-parallel" {
			t.Errorf("anomaly[%d] agent_id = %q, want agent-parallel", i, a.AgentID)
		}
	}
}

func TestRuleEngine_EvaluateBatch_EmptyBatch(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger(), nil)

	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{},
	}

	if len(engine.EvaluateBatch(batch)) != 0 {
		t.Error("expected no anomalies for empty batch")
	}
}
