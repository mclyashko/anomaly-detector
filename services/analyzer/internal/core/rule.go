package core

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	RuleTypeML        RuleType = "ml" // future
)

// RuleConfig is the parsed YAML representation of a single rule.
type RuleConfig struct {
	Name      string   `yaml:"name"`
	Metric    string   `yaml:"metric"`
	AgentID   string   `yaml:"agent_id"` // Scope rule to specific agent; empty = all agents
	Type      RuleType `yaml:"type"`
	Condition string   `yaml:"condition"` // e.g. "value > 0.8" for threshold
	Script    string   `yaml:"script"`    // e.g. "./rules/cpu_rule.lua" for lua
	Severity  Severity `yaml:"severity"`
}

// Rule is the core interface that every rule type implements.
// Evaluate inspects a metric and returns an Anomaly if the rule fires,
// or nil if the metric is normal.
type Rule interface {
	Name() string
	Metric() string
	Evaluate(m Metric) *Anomaly
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

// MLRule evaluates metrics using an ONNX ML model loaded from MinIO.
type MLRule struct {
	cfg      RuleConfig
	registry *ModelRegistry
}

func NewMLRule(cfg RuleConfig, registry *ModelRegistry) *MLRule {
	return &MLRule{cfg: cfg, registry: registry}
}

func (r *MLRule) Name() string   { return r.cfg.Name }
func (r *MLRule) Metric() string { return r.cfg.Metric }

// Evaluate runs ML inference for the given metric.
// It retrieves the recent history window from the model's sliding buffer,
// runs the forecast, and fires an anomaly if the value falls outside the confidence interval.
func (r *MLRule) Evaluate(m Metric) *Anomaly {
	if r.cfg.Metric != "" && m.Name != r.cfg.Metric {
		return nil
	}
	if r.cfg.AgentID != "" && m.AgentID != r.cfg.AgentID {
		return nil
	}
	model, ok := r.registry.Get(m.AgentID, m.Name)
	if !ok {
		return nil
	}

	history := model.GetHistory(m.AgentID, m.Name)
	windowSize := model.Config.WindowSize
	if windowSize == 0 {
		windowSize = DefaultWindowSize
	}
	if len(history) < windowSize {
		return nil
	}

	triggered, msg := model.DetectAnomaly(m.Value, history)
	if !triggered {
		return nil
	}
	return &Anomaly{
		ID:        m.ID,
		Rule:      r.cfg.Name,
		Metric:    m.Name,
		MetricID:  m.ID,
		Value:     m.Value,
		Condition: "ml_anomaly",
		Severity:  r.cfg.Severity,
		Timestamp: m.Timestamp,
		Message:   msg,
		AgentID:   m.AgentID,
		Service:   m.AgentID,
	}
}

// RuleFactory creates a Rule from a RuleConfig.
// The executor is required for Lua rules. The registry is required for ML rules.
// Returns an error for unknown rule types or if ML rule has no registry.
func RuleFactory(cfg RuleConfig, executor LuaExecutor, registry *ModelRegistry) (Rule, error) {
	switch cfg.Type {
	case RuleTypeThreshold:
		return NewThresholdRule(cfg)
	case RuleTypeLua:
		return NewLuaRule(cfg, executor)
	case RuleTypeML:
		if registry == nil {
			return nil, errors.New("ML rule requires a model registry")
		}
		return NewMLRule(cfg, registry), nil
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
