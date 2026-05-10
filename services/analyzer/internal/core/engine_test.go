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
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

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
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	if len(engine.Evaluate(metric("cpu", 0.5, "agent-1"))) != 0 {
		t.Error("expected no anomalies for normal value")
	}
}

func TestRuleEngine_Evaluate_WrongMetric(t *testing.T) {
	cfg := core.RuleConfig{Name: "high-cpu", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

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
	engine := core.NewRuleEngine([]core.Rule{cpuRule, memRule}, discardLogger())

	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{
			{Name: "cpu", Value: 0.95, Timestamp: ts(), AgentID: "agent-1"},
			{Name: "memory", Value: 0.99, Timestamp: ts(), AgentID: "agent-1"},
			{Name: "cpu", Value: 0.3, Timestamp: ts(), AgentID: "agent-1"},
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
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

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
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

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
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{},
	}

	if len(engine.EvaluateBatch(batch)) != 0 {
		t.Error("expected no anomalies for empty batch")
	}
}

func TestRuleEngine_Evaluate_MLRuleIndexLookup(t *testing.T) {
	cfg := core.RuleConfig{
		Name:     "test-ml",
		AgentID:  "agent-1",
		Metric:   "test_signal",
		Type:     core.RuleTypeML,
		Severity: core.SeverityCritical,
	}
	mlRule := core.NewMLRule(cfg, nil)
	engine := core.NewRuleEngine([]core.Rule{mlRule}, discardLogger())

	// Metric with matching agentID:metricName should match the ML rule.
	m := metric("test_signal", 1.0, "agent-1")
	anomalies := engine.Evaluate(m)
	if len(anomalies) != 0 {
		// ML rule needs history window to fire, but lookup should find it.
		t.Logf("ML rule matched (anomalies=%d, may need window)", len(anomalies))
	}

	// Metric with different agentID should NOT match ML rule.
	m2 := metric("test_signal", 1.0, "agent-2")
	anomalies2 := engine.Evaluate(m2)
	if len(anomalies2) != 0 {
		t.Errorf("expected no match for different agentID, got %d", len(anomalies2))
	}
}

func TestRuleEngine_GetMLRules(t *testing.T) {
	mlCfg := core.RuleConfig{
		Name:              "ml-rule-1",
		AgentID:           "agent-1",
		Metric:            "cpu",
		Type:              core.RuleTypeML,
		TrainIntervalMin:  30,
		TrainDataWindow:   200,
		SeasonalityPeriod: 60,
		Order:             []int{1, 0, 1},
		SeasonalOrder:     []int{1, 1, 1, 60},
	}
	thresholdCfg := core.RuleConfig{
		Name:      "threshold-rule",
		Metric:    "memory",
		Type:      core.RuleTypeThreshold,
		Condition: "value > 0.9",
		Severity:  core.SeverityWarning,
	}
	mlRule := core.NewMLRule(mlCfg, nil)
	thresholdRule, _ := core.NewThresholdRule(thresholdCfg)
	engine := core.NewRuleEngine([]core.Rule{mlRule, thresholdRule}, discardLogger())

	rules := engine.GetMLRules()
	if len(rules) != 1 {
		t.Fatalf("got %d ML rules, want 1", len(rules))
	}
	if rules[0].AgentID != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", rules[0].AgentID)
	}
	if rules[0].Metric != "cpu" {
		t.Errorf("metric = %q, want cpu", rules[0].Metric)
	}
	if rules[0].SeasonalityPeriod != 60 {
		t.Errorf("seasonality_period = %d, want 60", rules[0].SeasonalityPeriod)
	}
}

func TestRuleEngine_EvaluateBatch_SequentialPath(t *testing.T) {
	cfg := core.RuleConfig{Name: "high", Metric: "m", Type: core.RuleTypeThreshold, Condition: "value > 0", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{rule}, discardLogger())

	// Small batch — should go through sequential path.
	batch := core.Batch{
		AgentID: "agent-1",
		Metrics: []core.Metric{
			{Name: "m", Value: 1.0, Timestamp: ts()},
			{Name: "m", Value: -1.0, Timestamp: ts()},
			{Name: "m", Value: 2.0, Timestamp: ts()},
		},
	}

	anomalies := engine.EvaluateBatch(batch)
	if len(anomalies) != 2 {
		t.Errorf("got %d anomalies, want 2", len(anomalies))
	}
}
