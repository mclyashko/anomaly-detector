package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/ml"
)

// LuaExecutor executes Lua scripts for rule evaluation.
// Implementation is in internal/adapter/lua.
type LuaExecutor interface {
	Execute(scriptPath string, metric Metric) (triggered bool, message string, err error)
}

// RuleType defines the kind of rule execution.
type RuleType string

const (
	RuleTypeThreshold RuleType = "threshold"
	RuleTypeLua       RuleType = "lua"
	RuleTypeML        RuleType = "ml"
)

// RuleConfig is the parsed YAML representation of a single rule.
type RuleConfig struct {
	Name      string   `yaml:"name"`
	Metric    string   `yaml:"metric"`
	AgentID   string   `yaml:"agent_id"` // Scope rule to specific agent; empty = all agents
	Type      RuleType `yaml:"type"`
	Enabled   bool     `yaml:"enabled"`   // required — true/false, no omitempty
	Condition string   `yaml:"condition"` // e.g. "value > 0.8" for threshold
	Script    string   `yaml:"script"`    // e.g. "./rules/cpu_rule.lua" for lua
	Severity  Severity `yaml:"severity"`
	// ML training params (required for ml type rules)
	TrainIntervalMin  int   `yaml:"train_interval_min"` // minutes between retraining
	TrainDataWindow   int   `yaml:"train_data_window"`  // how many points to fetch for training
	SeasonalityPeriod int   `yaml:"seasonality_period"` // data points per seasonal cycle
	Order             []int `yaml:"order"`              // ARIMA order [p,d,q]
	SeasonalOrder     []int `yaml:"seasonal_order"`     // SARIMA seasonal order [P,D,Q,S]
}

// Rule is the core interface that every rule type implements.
// Evaluate inspects a metric and returns an Anomaly if the rule fires,
// or nil if the metric is normal.
type Rule interface {
	Name() string
	Metric() string
	Evaluate(m Metric) *Anomaly
}

// MLEvaluator evaluates metric values against a trained ML model.
type MLEvaluator interface {
	Evaluate(ctx context.Context, modelID string, history []float64, value float64) (*ml.EvaluateResponse, error)
}

// ThresholdRule implements Rule for simple threshold expressions like "value > 0.8".
type ThresholdRule struct {
	cfg   RuleConfig
	check func(float64) bool
}

func NewThresholdRule(cfg RuleConfig) (*ThresholdRule, error) {
	check, err := ParseCondition(cfg.Condition)
	if err != nil {
		return nil, err
	}
	return &ThresholdRule{cfg: cfg, check: check}, nil
}

func (r *ThresholdRule) Name() string   { return r.cfg.Name }
func (r *ThresholdRule) Metric() string { return r.cfg.Metric }

// Evaluate checks the metric against the threshold condition.
// Returns Anomaly if value satisfies the condition (e.g. value > 0.8).
// Returns nil if the metric name or AgentID does not match.
func (r *ThresholdRule) Evaluate(m Metric) *Anomaly {
	if m.Name != r.cfg.Metric {
		return nil
	}
	if r.cfg.AgentID != "" && m.AgentID != r.cfg.AgentID {
		return nil
	}
	if r.check(m.Value) {
		return &Anomaly{
			ID:        m.ID,
			Rule:      r.cfg.Name,
			Metric:    m.Name,
			MetricID:  m.ID,
			Value:     m.Value,
			Condition: r.cfg.Condition,
			Severity:  r.cfg.Severity,
			Timestamp: m.Timestamp,
			Message:   r.cfg.Condition,
			AgentID:   m.AgentID,
			Service:   m.AgentID,
		}
	}
	return nil
}

// LuaRule evaluates metrics using a Lua script via a LuaExecutor.
type LuaRule struct {
	cfg      RuleConfig
	executor LuaExecutor // port.LuaExecutor interface
}

func NewLuaRule(cfg RuleConfig, executor LuaExecutor) (*LuaRule, error) {
	if cfg.Script == "" {
		return nil, errors.New("lua rule requires a script path")
	}
	return &LuaRule{cfg: cfg, executor: executor}, nil
}

func (r *LuaRule) Name() string   { return r.cfg.Name }
func (r *LuaRule) Metric() string { return r.cfg.Metric }

// Evaluate runs the Lua script to check the metric.
// The script receives a table with metric data and returns triggered=true/false
// and a text message. Returns nil if AgentID does not match.
func (r *LuaRule) Evaluate(m Metric) *Anomaly {
	if r.cfg.AgentID != "" && m.AgentID != r.cfg.AgentID {
		return nil
	}
	triggered, msg, err := r.executor.Execute(r.cfg.Script, m)
	if err != nil {
		// Error is logged by executor; skip this rule silently.
		return nil
	}
	if !triggered {
		return nil
	}
	return &Anomaly{
		ID:        m.ID,
		Rule:      r.cfg.Name,
		Metric:    m.Name,
		MetricID:  m.ID,
		Value:     m.Value,
		Condition: r.cfg.Script,
		Severity:  r.cfg.Severity,
		Timestamp: m.Timestamp,
		Message:   msg,
		Service:   m.AgentID,
	}
}

// MLRule evaluates metrics by calling the Python ML Training Service via HTTP.
type MLRule struct {
	cfg       RuleConfig
	evaluator MLEvaluator
	modelID   string
	windowBuf []float64
}

// NewMLRule creates a new ML rule that calls the ML service HTTP API.
func NewMLRule(cfg RuleConfig, evaluator MLEvaluator) *MLRule {
	// Build model_id from agent_id and metric_name (matches training service convention)
	agentID := cfg.AgentID
	metricName := cfg.Metric
	if agentID == "" {
		agentID = "*"
	}
	modelID := agentID + "__" + strings.ReplaceAll(strings.ReplaceAll(metricName, ".", "_"), "/", "_")

	return &MLRule{
		cfg:       cfg,
		evaluator: evaluator,
		modelID:   modelID,
	}
}

func (r *MLRule) Name() string   { return r.cfg.Name }
func (r *MLRule) Metric() string { return r.cfg.Metric }

// Evaluate sends a metric to the Python ML Training Service for anomaly detection.
// Uses a sliding window: accumulates the last seasonality_period+1 values
// and sends them together with the current value to the ML service.
// The ML service returns a forecast and confidence interval — if the value is outside the CI, it's an anomaly.
// Returns nil if the ML service is unavailable or the window hasn't been filled yet.
func (r *MLRule) Evaluate(m Metric) *Anomaly {
	if r.cfg.Metric != "" && m.Name != r.cfg.Metric {
		return nil
	}
	if r.cfg.AgentID != "" && m.AgentID != r.cfg.AgentID {
		return nil
	}

	// Context is not propagated from the caller: the polling loop has no cancellation,
	// and evaluate runs synchronously with a short HTTP timeout on the ML client side.
	ctx := context.Background()

	// windowSize = seasonality_period + 1: to compute seasonal correction
	// we need the value from S periods ago, so we store S+1 most recent values.
	// With seasonality_period=60 we store 61 values.
	windowSize := r.cfg.SeasonalityPeriod + 1

	// Sliding window: append current value, drop oldest if window exceeds size.
	// This accumulates history without growing memory indefinitely.
	r.windowBuf = append(r.windowBuf, m.Value)
	if len(r.windowBuf) > windowSize {
		r.windowBuf = r.windowBuf[len(r.windowBuf)-windowSize:]
	}

	// ML model needs at least windowSize values for a meaningful forecast.
	// If history is insufficient, return nil — the rule stays silent.
	if len(r.windowBuf) < windowSize {
		return nil
	}

	// Build history as a fresh slice (most recent last)
	history := make([]float64, len(r.windowBuf))
	copy(history, r.windowBuf)

	// Log at DEBUG level on every ML evaluation once the window is full.
	slog.Debug("MLRule Evaluate: window ready",
		"metric", m.Name,
		"value", m.Value,
		"history_len", len(history),
		"window_buf_len", len(r.windowBuf),
	)

	if r.evaluator == nil {
		return nil
	}
	result, err := r.evaluator.Evaluate(ctx, r.modelID, history, m.Value)
	if err != nil {
		// ML service unavailable — skip silently
		return nil
	}

	slog.Debug("MLRule Evaluate: result",
		"metric", m.Name,
		"anomaly", result.Anomaly,
		"forecast", result.Forecast,
		"ci_lower", result.LowerCI,
		"ci_upper", result.UpperCI,
	)

	if result == nil || !result.Anomaly {
		return nil
	}

	return &Anomaly{
		ID:            m.ID,
		Rule:          r.cfg.Name,
		Metric:        m.Name,
		MetricID:      m.ID,
		Value:         m.Value,
		Condition:     "ml_anomaly",
		Severity:      r.cfg.Severity,
		Timestamp:     m.Timestamp,
		Message:       result.Message,
		AgentID:       m.AgentID,
		Service:       m.AgentID,
		Forecast:      result.Forecast,
		ExpectedValue: result.Forecast,
		LowerCI:       result.LowerCI,
		UpperCI:       result.UpperCI,
	}
}

// RuleFactory creates a Rule from a RuleConfig.
// The executor is required for Lua rules. The mlEvaluator is required for ML rules.
// Returns an error for unknown rule types or if ML rule has no evaluator.
func RuleFactory(cfg RuleConfig, executor LuaExecutor, mlEvaluator MLEvaluator) (Rule, error) {
	switch cfg.Type {
	case RuleTypeThreshold:
		return NewThresholdRule(cfg)
	case RuleTypeLua:
		return NewLuaRule(cfg, executor)
	case RuleTypeML:
		if mlEvaluator == nil {
			return nil, errors.New("ML rule requires an ML evaluator")
		}
		return NewMLRule(cfg, mlEvaluator), nil
	default:
		return nil, fmt.Errorf("unknown rule type %q for rule %q", cfg.Type, cfg.Name)
	}
}

// ParseCondition parses a simple "value OP float" condition and returns
// a predicate function. Supported operators: > < >= <= == !=
// Returns an error for malformed or unsupported expressions.
func ParseCondition(expr string) (func(float64) bool, error) {
	expr = strings.TrimSpace(expr)

	for _, op := range []string{">=", "<=", "!=", "==", ">", "<"} {
		idx := strings.Index(expr, op)
		if idx == -1 {
			continue
		}
		lhs := strings.TrimSpace(expr[:idx])
		rhs := strings.TrimSpace(expr[idx+len(op):])
		if lhs != "value" {
			return nil, fmt.Errorf("unsupported LHS %q in condition %q", lhs, expr)
		}
		threshold, err := strconv.ParseFloat(rhs, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid threshold %q in condition %q: %w", rhs, expr, err)
		}
		return boolFn(op, threshold), nil
	}
	return nil, fmt.Errorf("could not parse condition %q", expr)
}

func boolFn(op string, threshold float64) func(float64) bool {
	switch op {
	case ">":
		return func(v float64) bool { return v > threshold }
	case "<":
		return func(v float64) bool { return v < threshold }
	case ">=":
		return func(v float64) bool { return v >= threshold }
	case "<=":
		return func(v float64) bool { return v <= threshold }
	case "==":
		return func(v float64) bool { return v == threshold }
	case "!=":
		return func(v float64) bool { return v != threshold }
	default:
		panic("unknown operator: " + op)
	}
}
