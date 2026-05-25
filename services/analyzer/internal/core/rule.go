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
	RuleTypeKS        RuleType = "ks"
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
	// KS-test params (required for ks type rules)
	ReferenceWindow   int     `yaml:"reference_window"`   // size of the reference window (previous N values)
	SignificanceLevel float64 `yaml:"significance_level"` // p-value threshold; anomaly if p < this value
	MinWindowSize     int     `yaml:"min_window_size"`    // minimum current window size to trigger evaluation
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

	// windowSize = train_data_window: we need all training points for the model
	// to be able to do correct Kalman inference. With train_data_window=200
	// we accumulate 200 values before sending the first evaluation request.
	windowSize := r.cfg.TrainDataWindow

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

// KSRule evaluates metrics using a two-sample Kolmogorov-Smirnov test.
// It maintains a sliding buffer of size (reference_window + min_window_size).
// Each Evaluate call:
//   - appends the incoming value to the buffer
//   - trims to max size
//   - if buffer is full: splits into reference (older) and current (newer) windows
//   - runs KS test; if p-value < significance_level → anomaly
type KSRule struct {
	cfg       RuleConfig
	windowBuf []float64
	maxSize   int
}

// NewKSRule creates a KS rule. ReferenceWindow and MinWindowSize must be > 0.
func NewKSRule(cfg RuleConfig) *KSRule {
	maxSize := cfg.ReferenceWindow + cfg.MinWindowSize
	if maxSize <= 0 {
		maxSize = 1 // avoid panic, Evaluate will return nil
	}
	return &KSRule{cfg: cfg, maxSize: maxSize}
}

func (r *KSRule) Name() string   { return r.cfg.Name }
func (r *KSRule) Metric() string { return r.cfg.Metric }

// Evaluate checks whether the current window distribution differs significantly
// from the reference window using a two-sample Kolmogorov-Smirnov test.
func (r *KSRule) Evaluate(m Metric) *Anomaly {
	if r.cfg.Metric != "" && m.Name != r.cfg.Metric {
		return nil
	}
	if r.cfg.AgentID != "" && m.AgentID != r.cfg.AgentID {
		return nil
	}

	// Sliding window: append current value, trim to maxSize.
	r.windowBuf = append(r.windowBuf, m.Value)
	if len(r.windowBuf) > r.maxSize {
		r.windowBuf = r.windowBuf[len(r.windowBuf)-r.maxSize:]
	}

	// Not enough data yet — need maxSize values before first evaluation.
	if len(r.windowBuf) < r.maxSize {
		return nil
	}

	// Split into non-overlapping adjacent windows:
	//   [ oldest ... | reference (last ref_w) | current (last min_w) ]
	refStart := len(r.windowBuf) - r.cfg.ReferenceWindow - r.cfg.MinWindowSize
	ref := r.windowBuf[refStart : refStart+r.cfg.ReferenceWindow]
	cur := r.windowBuf[refStart+r.cfg.ReferenceWindow:]

	// KS test requires at least 2 elements in each window.
	if len(ref) < 2 || len(cur) < 2 {
		return nil
	}

	pValue := KsTest(ref, cur)

	// Significance default: if not set (0), use 0.05.
	sigLevel := r.cfg.SignificanceLevel
	if sigLevel == 0 {
		sigLevel = 0.05
	}

	if pValue >= sigLevel {
		return nil
	}

	return &Anomaly{
		ID:        m.ID,
		Rule:      r.cfg.Name,
		Metric:    m.Name,
		MetricID:  m.ID,
		Value:     m.Value,
		Condition: fmt.Sprintf("ks_test p=%.4f < %.2f", pValue, sigLevel),
		Severity:  r.cfg.Severity,
		Timestamp: m.Timestamp,
		Message:   fmt.Sprintf("KS-test p-value %.4f below significance %.2f", pValue, sigLevel),
		AgentID:   m.AgentID,
		Service:   m.AgentID,
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
	case RuleTypeKS:
		return NewKSRule(cfg), nil
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
