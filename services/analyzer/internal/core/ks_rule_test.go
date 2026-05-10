package core_test

import (
	"log/slog"
	"testing"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// ─────────────────────────────────────────────
// KsTest function unit tests
// ─────────────────────────────────────────────

func TestKsTest_IdenticalDistributions(t *testing.T) {
	// Two identical sequences should produce a high p-value (not anomalous).
	ref := make([]float64, 50)
	cur := make([]float64, 50)
	for i := range ref {
		ref[i] = float64(i)
		cur[i] = float64(i) // identical
	}
	p := core.KsTest(ref, cur)
	if p < 0.05 {
		t.Errorf("identical distributions: p=%.4f, want >= 0.05", p)
	}
}

func TestKsTest_ShiftedDistributions(t *testing.T) {
	// Two clearly different distributions should produce a low p-value (anomalous).
	ref := make([]float64, 50)
	cur := make([]float64, 50)
	for i := range ref {
		ref[i] = float64(i)
		cur[i] = float64(i * i % 20) // non-uniform vs uniform
	}
	p := core.KsTest(ref, cur)
	if p >= 0.05 {
		t.Errorf("shifted distributions: p=%.4f, want < 0.05", p)
	}
}

func TestKsTest_InsufficientDataBothLessThanTwo(t *testing.T) {
	// Both slices < 2 elements → p should be 1.0 (no anomaly).
	ref := []float64{1.0}
	cur := []float64{1.0}
	p := core.KsTest(ref, cur)
	if p != 1.0 {
		t.Errorf("insufficient data: p=%.4f, want 1.0", p)
	}
}

func TestKsTest_InsufficientDataRefLessThanTwo(t *testing.T) {
	ref := []float64{1.0}
	cur := []float64{1.0, 2.0, 3.0}
	p := core.KsTest(ref, cur)
	if p != 1.0 {
		t.Errorf("ref < 2 elements: p=%.4f, want 1.0", p)
	}
}

func TestKsTest_InsufficientDataCurLessThanTwo(t *testing.T) {
	ref := []float64{1.0, 2.0, 3.0}
	cur := []float64{1.0}
	p := core.KsTest(ref, cur)
	if p != 1.0 {
		t.Errorf("cur < 2 elements: p=%.4f, want 1.0", p)
	}
}

func TestKsTest_EqualValuesBothHigh(t *testing.T) {
	// Same constant value in both windows — distributions are identical, p should be high.
	ref := make([]float64, 30)
	cur := make([]float64, 30)
	for i := range ref {
		ref[i] = 42.0
		cur[i] = 42.0
	}
	p := core.KsTest(ref, cur)
	if p < 0.05 {
		t.Errorf("equal constants: p=%.4f, want >= 0.05", p)
	}
}

func TestKsTest_NormalVsUniform(t *testing.T) {
	// Normal distribution vs uniform — should be detected as different.
	ref := make([]float64, 50)
	cur := make([]float64, 50)
	for i := range ref {
		ref[i] = float64(i)          // uniform
		cur[i] = float64(i * i % 20) // non-uniform
	}
	p := core.KsTest(ref, cur)
	if p >= 0.05 {
		t.Errorf("different distributions: p=%.4f, want < 0.05", p)
	}
}

// ─────────────────────────────────────────────
// KSRule.Evaluate unit tests
// ─────────────────────────────────────────────

func TestKSRule_Evaluate_DetectsDistributionShift(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "test_ks",
		Metric:            "test_signal",
		AgentID:           "agent-1",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   20,
		MinWindowSize:     10,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityCritical,
	}
	rule := core.NewKSRule(cfg)
	metrics := core.Metric{Name: "test_signal", AgentID: "agent-1"}

	// Fill buffer with baseline values (small range).
	for i := 0; i < 60; i++ {
		m := metrics
		m.Value = float64(i%10) + 50.0 // stable baseline around 50-59
		rule.Evaluate(m)
	}

	// Inject a clear shift in the current window.
	anomalyCount := 0
	for i := 0; i < 10; i++ {
		m := metrics
		m.Value = float64(i) + 200.0 // high values — shifted distribution
		result := rule.Evaluate(m)
		if result != nil {
			anomalyCount++
		}
	}

	if anomalyCount == 0 {
		t.Error("expected at least one anomaly after distribution shift")
	}
}

func TestKSRule_Evaluate_WrongAgentID(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "ks",
		Metric:            "m",
		AgentID:           "agent-1",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   10,
		MinWindowSize:     5,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityCritical,
	}
	rule := core.NewKSRule(cfg)
	m := core.Metric{Name: "m", AgentID: "agent-2", Value: 1.0}
	if rule.Evaluate(m) != nil {
		t.Error("expected nil for agent mismatch")
	}
}

func TestKSRule_Evaluate_WrongMetric(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "ks",
		Metric:            "cpu",
		AgentID:           "",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   10,
		MinWindowSize:     5,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityWarning,
	}
	rule := core.NewKSRule(cfg)
	m := core.Metric{Name: "memory", Value: 1.0}
	if rule.Evaluate(m) != nil {
		t.Error("expected nil for metric mismatch")
	}
}

func TestKSRule_Evaluate_InsufficientHistory(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "ks",
		Metric:            "m",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   20,
		MinWindowSize:     10,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityCritical,
	}
	rule := core.NewKSRule(cfg)

	// Feed fewer than maxSize=30 values.
	for i := 0; i < 15; i++ {
		m := core.Metric{Name: "m", Value: float64(i)}
		if rule.Evaluate(m) != nil {
			t.Error("expected nil with insufficient history")
		}
	}
}

func TestKSRule_Evaluate_NameAndMetric(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "my_ks_rule",
		Metric:            "my_metric",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   5,
		MinWindowSize:     3,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityWarning,
	}
	rule := core.NewKSRule(cfg)
	if rule.Name() != "my_ks_rule" {
		t.Errorf("Name() = %q, want %q", rule.Name(), "my_ks_rule")
	}
	if rule.Metric() != "my_metric" {
		t.Errorf("Metric() = %q, want %q", rule.Metric(), "my_metric")
	}
}

func TestKSRule_Evaluate_NoAnomalyWhenPValueAboveThreshold(t *testing.T) {
	cfg := core.RuleConfig{
		Name:              "ks",
		Metric:            "m",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   20,
		MinWindowSize:     10,
		SignificanceLevel: 0.01, // very strict — almost any shift will fire
		Severity:          core.SeverityCritical,
	}
	rule := core.NewKSRule(cfg)
	metrics := core.Metric{Name: "m"}

	// Fill with completely uniform data — p should be 1.0 → no anomaly.
	for i := 0; i < 60; i++ {
		m := metrics
		m.Value = 50.0 // constant — identical windows
		result := rule.Evaluate(m)
		if result != nil && i >= 30 { // after buffer is full
			t.Errorf("expected no anomaly for constant data, got p-based anomaly at i=%d", i)
		}
	}
}

// ─────────────────────────────────────────────
// Engine integration: KS rule indexing
// ─────────────────────────────────────────────

func TestRuleEngine_Evaluate_KSRuleIndexLookup(t *testing.T) {
	logger := slog.Default()
	metrics := core.Metric{Name: "test_signal", AgentID: "agent-1"}

	cfg := core.RuleConfig{
		Name:              "test_ks",
		AgentID:           "agent-1",
		Metric:            "test_signal",
		Type:              core.RuleTypeKS,
		ReferenceWindow:   10,
		MinWindowSize:     5,
		SignificanceLevel: 0.05,
		Severity:          core.SeverityCritical,
	}
	ksRule := core.NewKSRule(cfg)
	engine := core.NewRuleEngine([]core.Rule{ksRule}, logger)

	// Different agentID — should not match.
	mismatchAgent := metrics
	mismatchAgent.AgentID = "agent-99"
	if len(engine.Evaluate(mismatchAgent)) != 0 {
		t.Error("expected no match for different agentID")
	}

	// Correct agentID — evaluate should not panic.
	m := metrics
	m.Value = 100.0
	engine.Evaluate(m) // should not panic
}
