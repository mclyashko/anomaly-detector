package core_test

import (
	"testing"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// --- ParseCondition ---

func TestParseCondition_ValidOperators(t *testing.T) {
	cases := []struct {
		expr string
		val  float64
		want bool
	}{
		{"value > 0.8", 0.9, true},
		{"value > 0.8", 0.8, false},
		{"value >= 0.8", 0.8, true},
		{"value < 0.8", 0.5, true},
		{"value <= 0.8", 0.8, true},
		{"value == 1.0", 1.0, true},
		{"value == 1.0", 1.0000001, false},
		{"value != 0.0", 0.0, false},
		{"value != 0.0", 0.001, true},
	}

	for _, c := range cases {
		t.Run(c.expr, func(t *testing.T) {
			check, err := core.ParseCondition(c.expr)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := check(c.val); got != c.want {
				t.Errorf("check(%v) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

func TestParseCondition_Invalid(t *testing.T) {
	cases := []string{
		"",
		"value",
		"x > 0.5",
		"value > xx",
		"value >",
	}
	for _, expr := range cases {
		t.Run(expr, func(t *testing.T) {
			_, err := core.ParseCondition(expr)
			if err == nil {
				t.Errorf("expected error for %q, got nil", expr)
			}
		})
	}
}

// --- ThresholdRule ---

func TestThresholdRule_Fires(t *testing.T) {
	cfg := core.RuleConfig{Name: "high", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, err := core.NewThresholdRule(cfg)
	if err != nil {
		t.Fatal(err)
	}

	anomaly := rule.Evaluate(metric("cpu", 0.9, "test-agent"))
	if anomaly == nil {
		t.Fatal("expected anomaly, got nil")
	}
	if anomaly.Rule != "high" {
		t.Errorf("rule name = %q, want high", anomaly.Rule)
	}
	if anomaly.Severity != core.SeverityWarning {
		t.Errorf("severity = %v, want warning", anomaly.Severity)
	}
}

func TestThresholdRule_NoMatch(t *testing.T) {
	cfg := core.RuleConfig{Name: "high", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)

	if rule.Evaluate(metric("cpu", 0.5, "test-agent")) != nil {
		t.Error("expected nil for normal value")
	}
}

func TestThresholdRule_WrongMetric(t *testing.T) {
	cfg := core.RuleConfig{Name: "high", Metric: "cpu", Type: core.RuleTypeThreshold, Condition: "value > 0.8", Severity: core.SeverityWarning}
	rule, _ := core.NewThresholdRule(cfg)

	if rule.Evaluate(metric("memory", 0.99, "test-agent")) != nil {
		t.Error("expected nil for non-matching metric name")
	}
}

// --- RuleFactory ---

func TestRuleFactory_Threshold(t *testing.T) {
	cfg := core.RuleConfig{Name: "r", Metric: "m", Type: core.RuleTypeThreshold, Condition: "value > 1"}
	rule, err := core.RuleFactory(cfg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.Name() != "r" {
		t.Errorf("name = %q, want r", rule.Name())
	}
}

func TestRuleFactory_LuaStub(t *testing.T) {
	cfg := core.RuleConfig{Name: "lua-rule", Metric: "m", Type: core.RuleTypeLua, Script: "./rules/test.lua", Severity: core.SeverityInfo}
	_, err := core.RuleFactory(cfg, nil, nil)
	if err == nil {
		// LuaRule requires Script field to be non-empty.
		// If Script is empty, NewLuaRule returns error.
	}
}

func TestRuleFactory_MLStub(t *testing.T) {
	cfg := core.RuleConfig{Name: "ml-rule", Metric: "m", Type: core.RuleTypeML, Condition: "", Severity: core.SeverityInfo}
	_, err := core.RuleFactory(cfg, nil, nil)
	if err == nil {
		t.Error("expected error for ML rule without registry")
	}
}

func TestRuleFactory_UnknownType(t *testing.T) {
	cfg := core.RuleConfig{Name: "bad", Metric: "m", Type: "unknown"}
	_, err := core.RuleFactory(cfg, nil, nil)
	if err == nil {
		t.Error("expected error for unknown type")
	}
}

// --- MLRule ---

func TestMLRule_NoModel(t *testing.T) {
	// Registry has no models loaded — MLRule should return nil silently.
	cfg := core.RuleConfig{
		Name: "ml-rule", Metric: "cpu", Type: core.RuleTypeML,
		AgentID: "agent-1", Severity: core.SeverityWarning,
	}
	ms := &mockStorage{}
	registry := core.NewModelRegistry(ms, []core.ModelConfig{}, discardLogger())
	mlRule := core.NewMLRule(cfg, registry)

	// No model for agent-1:cpu → nil
	anomaly := mlRule.Evaluate(metric("cpu", 100.0, "agent-1"))
	if anomaly != nil {
		t.Error("expected nil when no model is loaded")
	}
}

func TestMLRule_WrongAgent(t *testing.T) {
	cfg := core.RuleConfig{
		Name: "ml-rule", Metric: "cpu", Type: core.RuleTypeML,
		AgentID: "agent-1", Severity: core.SeverityWarning,
	}
	ms := &mockStorage{}
	registry := core.NewModelRegistry(ms, []core.ModelConfig{}, discardLogger())
	mlRule := core.NewMLRule(cfg, registry)

	// Metric for agent-2, rule scoped to agent-1 → nil
	anomaly := mlRule.Evaluate(metric("cpu", 100.0, "agent-2"))
	if anomaly != nil {
		t.Error("expected nil for wrong agent_id")
	}
}

func TestMLRule_WrongMetric(t *testing.T) {
	cfg := core.RuleConfig{
		Name: "ml-rule", Metric: "cpu", Type: core.RuleTypeML,
		AgentID: "agent-1", Severity: core.SeverityWarning,
	}
	ms := &mockStorage{}
	registry := core.NewModelRegistry(ms, []core.ModelConfig{}, discardLogger())
	mlRule := core.NewMLRule(cfg, registry)

	anomaly := mlRule.Evaluate(metric("memory", 100.0, "agent-1"))
	if anomaly != nil {
		t.Error("expected nil for wrong metric")
	}
}
