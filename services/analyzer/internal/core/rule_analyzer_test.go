package core

import (
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/ml"
)

// mockServer is a test helper that simulates the ML training service.
type mockServer struct {
	anomaly  bool
	forecast float64
	lowerCI  float64
	upperCI  float64
	message  string
	called   bool
}

func (m *mockServer) ServeHTTP() {}

func TestMLRule_Evaluate_NilClient(t *testing.T) {
	// Ensure nil client doesn't panic
	rule := &MLRule{
		cfg: RuleConfig{
			Name:              "test_ml",
			Metric:            "test_signal",
			Type:              RuleTypeML,
			SeasonalityPeriod: 60,
		},
		evaluator: nil,
	}

	m := Metric{
		ID:        1,
		Name:      "test_signal",
		AgentID:   "agent-1",
		Value:     5.0,
		Timestamp: time.Now(),
	}

	result := rule.Evaluate(m)
	if result != nil {
		t.Errorf("expected nil for nil client, got %+v", result)
	}
}

func TestMLRule_Evaluate_SlidingWindow(t *testing.T) {
	// Test that the sliding window accumulates values correctly
	rule := &MLRule{
		cfg: RuleConfig{
			Name:              "test_ml",
			Metric:            "test_signal",
			Type:              RuleTypeML,
			SeasonalityPeriod: 5, // small window for testing
		},
		modelID:   "agent-1__test_signal",
		windowBuf: []float64{1.0, 2.0, 3.0, 4.0, 5.0},
	}

	m := Metric{
		ID:        1,
		Name:      "test_signal",
		AgentID:   "agent-1",
		Value:     6.0,
		Timestamp: time.Now(),
	}

	// Evaluate should add value to window
	rule.Evaluate(m)

	// Window should now have 6 values (5 old + 1 new), max size is seasonality+1=6
	if len(rule.windowBuf) != 6 {
		t.Errorf("windowBuf len = %d, want 6", len(rule.windowBuf))
	}

	// Add more values beyond window size
	for i := 7; i <= 12; i++ {
		m.Value = float64(i)
		rule.Evaluate(m)
	}

	// Window should still be max 6 (seasonality+1)
	if len(rule.windowBuf) != 6 {
		t.Errorf("windowBuf len after overflow = %d, want 6", len(rule.windowBuf))
	}
}

func TestMLRule_Evaluate_MetricMismatch(t *testing.T) {
	rule := &MLRule{
		cfg: RuleConfig{
			Name:   "test_ml",
			Metric: "test_signal",
			Type:   RuleTypeML,
		},
		modelID: "agent-1__test_signal",
	}

	m := Metric{
		ID:        1,
		Name:      "other_signal", // Different metric name
		AgentID:   "agent-1",
		Value:     5.0,
		Timestamp: time.Now(),
	}

	result := rule.Evaluate(m)
	if result != nil {
		t.Errorf("expected nil for metric mismatch, got %+v", result)
	}
}

func TestMLRule_Evaluate_AgentIDMismatch(t *testing.T) {
	rule := &MLRule{
		cfg: RuleConfig{
			Name:    "test_ml",
			Metric:  "test_signal",
			AgentID: "agent-1",
			Type:    RuleTypeML,
		},
		modelID: "agent-1__test_signal",
	}

	m := Metric{
		ID:        1,
		Name:      "test_signal",
		AgentID:   "agent-2", // Different agent
		Value:     5.0,
		Timestamp: time.Now(),
	}

	result := rule.Evaluate(m)
	if result != nil {
		t.Errorf("expected nil for agent mismatch, got %+v", result)
	}
}

func TestMLRule_NameAndMetric(t *testing.T) {
	rule := &MLRule{
		cfg: RuleConfig{
			Name:   "my_ml_rule",
			Metric: "my_metric",
			Type:   RuleTypeML,
		},
	}

	if rule.Name() != "my_ml_rule" {
		t.Errorf("Name() = %s, want my_ml_rule", rule.Name())
	}
	if rule.Metric() != "my_metric" {
		t.Errorf("Metric() = %s, want my_metric", rule.Metric())
	}
}

func TestNewMLRule_ModelIDConstruction(t *testing.T) {
	tests := []struct {
		agentID   string
		metric    string
		wantModel string
	}{
		{"agent-1", "test_signal", "agent-1__test_signal"},
		{"agent-1", "http_latency", "agent-1__http_latency"},
		{"agent-1", "cpu/usage", "agent-1__cpu_usage"},
		{"", "test_signal", "*__test_signal"},
	}

	for _, tt := range tests {
		cfg := RuleConfig{
			AgentID: tt.agentID,
			Metric:  tt.metric,
			Type:    RuleTypeML,
		}
		rule := NewMLRule(cfg, nil)
		if rule.modelID != tt.wantModel {
			t.Errorf("modelID = %s, want %s (agent=%s metric=%s)",
				rule.modelID, tt.wantModel, tt.agentID, tt.metric)
		}
	}
}

func TestRuleFactory_MLRule(t *testing.T) {
	cfg := RuleConfig{
		Name:              "test_ml",
		Metric:            "test_signal",
		Type:              RuleTypeML,
		SeasonalityPeriod: 60,
		Order:             []int{1, 0, 1},
		SeasonalOrder:     []int{1, 1, 1, 24},
	}

	// Create a minimal client - we don't call Evaluate, just check rule creation.
	// NewClient with a dummy URL still creates a non-nil client.
	client := ml.NewClient(ml.Config{URL: "http://localhost:9999", Timeout: time.Second})

	rule, err := RuleFactory(cfg, nil, client)
	if err != nil {
		t.Fatalf("RuleFactory() error = %v", err)
	}

	mlRule, ok := rule.(*MLRule)
	if !ok {
		t.Fatalf("expected *MLRule, got %T", rule)
	}

	if mlRule.cfg.Name != "test_ml" {
		t.Errorf("MLRule.cfg.Name = %s, want test_ml", mlRule.cfg.Name)
	}
}

func TestRuleFactory_MLRule_NoClient(t *testing.T) {
	cfg := RuleConfig{
		Name:              "test_ml",
		Metric:            "test_signal",
		Type:              RuleTypeML,
		SeasonalityPeriod: 60,
		Order:             []int{1, 0, 1},
		SeasonalOrder:     []int{1, 1, 1, 24},
	}

	// ML rules require a non-nil client — RuleFactory should return an error.
	_, err := RuleFactory(cfg, nil, nil)
	if err == nil {
		t.Fatalf("expected error for nil ML client, got nil")
	}
}

func TestMLRule_Evaluate_InsufficientHistory(t *testing.T) {
	// Test the logic path directly via rule evaluation with insufficient history
	rule := &MLRule{
		cfg: RuleConfig{
			Name:              "test_ml",
			Metric:            "test_signal",
			AgentID:           "agent-1",
			Type:              RuleTypeML,
			SeasonalityPeriod: 60,
			Severity:          SeverityCritical,
		},
		modelID:   "agent-1__test_signal",
		windowBuf: []float64{1.0}, // Only 1 value, not enough for windowSize=61
	}

	m := Metric{
		Name:      "test_signal",
		AgentID:   "agent-1",
		Value:     10.0,
		Timestamp: time.Now(),
	}

	// Should return nil because len(windowBuf) < windowSize (61)
	// windowSize = seasonalityPeriod + 1 = 60 + 1 = 61
	result := rule.Evaluate(m)
	if result != nil {
		t.Errorf("expected nil with insufficient history, got %+v", result)
	}
}

func TestParseCondition(t *testing.T) {
	tests := []struct {
		cond  string
		val   float64
		want  bool
	}{
		{"value > 0.8", 0.9, true},
		{"value > 0.8", 0.7, false},
		{"value < 0.5", 0.3, true},
		{"value < 0.5", 0.6, false},
		{"value >= 1.0", 1.0, true},
		{"value >= 1.0", 0.9, false},
		{"value <= 2.0", 2.0, true},
		{"value <= 2.0", 2.1, false},
		{"value == 3.0", 3.0, true},
		{"value == 3.0", 3.1, false},
		{"value != 0.0", 1.0, true},
	}

	for _, tt := range tests {
		t.Run(tt.cond, func(t *testing.T) {
			fn, err := ParseCondition(tt.cond)
			if err != nil {
				t.Fatalf("ParseCondition() error = %v", err)
			}
			if fn(tt.val) != tt.want {
				t.Errorf("ParseCondition(%s)(%v) = %v, want %v", tt.cond, tt.val, fn(tt.val), tt.want)
			}
		})
	}
}

func TestParseCondition_Invalid(t *testing.T) {
	_, err := ParseCondition("x > 0.5")
	if err == nil {
		t.Errorf("expected error for invalid LHS")
	}

	_, err = ParseCondition("value > abc")
	if err == nil {
		t.Errorf("expected error for invalid threshold")
	}

	_, err = ParseCondition("value == ")
	if err == nil {
		t.Errorf("expected error for empty threshold")
	}
}

func TestThresholdRule_Evaluate(t *testing.T) {
	cfg := RuleConfig{
		Name:      "high_cpu",
		Metric:    "cpu_usage",
		Type:      RuleTypeThreshold,
		Condition: "value > 0.8",
		Severity:  SeverityCritical,
	}

	rule, err := NewThresholdRule(cfg)
	if err != nil {
		t.Fatalf("NewThresholdRule() error = %v", err)
	}

	tests := []struct {
		name  string
		metric Metric
		want   bool
	}{
		{
			name:   "triggers on high value",
			metric: Metric{Name: "cpu_usage", Value: 0.9},
			want:   true,
		},
		{
			name:   "does not trigger on low value",
			metric: Metric{Name: "cpu_usage", Value: 0.5},
			want:   false,
		},
		{
			name:   "wrong metric returns nil",
			metric: Metric{Name: "memory_usage", Value: 0.9},
			want:   false,
		},
		{
			name:   "agent ID set but empty string means no filtering",
			metric: Metric{Name: "cpu_usage", AgentID: "other-agent", Value: 0.9},
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := rule.Evaluate(tt.metric)
			if (result != nil) != tt.want {
				t.Errorf("Evaluate() = %v, want %v", result != nil, tt.want)
			}
		})
	}
}

func TestThresholdRule_AgentIDFiltering(t *testing.T) {
	cfg := RuleConfig{
		Name:      "high_cpu_agent1",
		Metric:    "cpu_usage",
		AgentID:   "agent-1",
		Type:      RuleTypeThreshold,
		Condition: "value > 0.8",
		Severity:  SeverityCritical,
	}

	rule, err := NewThresholdRule(cfg)
	if err != nil {
		t.Fatalf("NewThresholdRule() error = %v", err)
	}

	// Matching agent should trigger
	m1 := Metric{Name: "cpu_usage", AgentID: "agent-1", Value: 0.9}
	if rule.Evaluate(m1) == nil {
		t.Errorf("expected trigger for agent-1")
	}

	// Non-matching agent should not trigger
	m2 := Metric{Name: "cpu_usage", AgentID: "agent-2", Value: 0.9}
	if rule.Evaluate(m2) != nil {
		t.Errorf("expected nil for agent-2")
	}
}

func TestRuleEngine_EvaluateBatch(t *testing.T) {
	thresholdCfg := RuleConfig{
		Name:      "high_value",
		Metric:    "test_metric",
		Type:      RuleTypeThreshold,
		Condition: "value > 100",
		Severity:  SeverityWarning,
	}

	rule, err := NewThresholdRule(thresholdCfg)
	if err != nil {
		t.Fatalf("NewThresholdRule() error = %v", err)
	}

	engine := NewRuleEngine([]Rule{rule}, nil)

	batch := Batch{
		Metrics: []Metric{
			{ID: 1, Name: "test_metric", Value: 50},
			{ID: 2, Name: "test_metric", Value: 150},
			{ID: 3, Name: "other_metric", Value: 200},
		},
	}

	anomalies := engine.EvaluateBatch(batch)
	if len(anomalies) != 1 {
		t.Fatalf("expected 1 anomaly, got %d", len(anomalies))
	}
	if anomalies[0].MetricID != 2 {
		t.Errorf("expected anomaly for metric ID 2, got %d", anomalies[0].MetricID)
	}
}

func TestRuleEngine_EmptyBatch(t *testing.T) {
	engine := NewRuleEngine(nil, nil)
	anomalies := engine.EvaluateBatch(Batch{})
	if anomalies != nil {
		t.Errorf("expected nil for empty batch, got %+v", anomalies)
	}
}

func TestRuleEngine_MLRuleIndexing(t *testing.T) {
	// Test that ML rules are indexed by agentID:metricName
	mlCfg := RuleConfig{
		Name:              "ml_rule",
		Metric:            "test_signal",
		AgentID:           "agent-1",
		Type:              RuleTypeML,
		SeasonalityPeriod: 60,
	}

	// Create MLRule without client (for testing indexing)
	mlRule := NewMLRule(mlCfg, nil)

	engine := NewRuleEngine([]Rule{mlRule}, nil)

	// Should be indexed under "agent-1:test_signal"
	m := Metric{Name: "test_signal", AgentID: "agent-1", Value: 5.0}
	result := engine.Evaluate(m)
	// With nil client this returns nil, but the indexing should work
	if result != nil {
		t.Errorf("expected nil result with nil client, got %+v", result)
	}
}
