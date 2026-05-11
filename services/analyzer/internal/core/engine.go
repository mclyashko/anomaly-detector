package core

import (
	"log/slog"
	"sync"
)

const (
	// batchParallelThreshold is the minimum batch size before parallel evaluation kicks in.
	// Below this, sequential evaluation has lower overhead.
	batchParallelThreshold = 500
	// chunkSize is how many metrics each parallel worker processes.
	chunkSize = 200
)

// RuleEngine evaluates metrics against registered rules.
// It is safe for concurrent use.
type RuleEngine struct {
	rules  []Rule
	logger *slog.Logger

	// index maps "agentID:metricName" → rule indices.
	// agentID may be "*" for rules with empty AgentID (wildcard — matches any agent).
	index map[string][]int
}

// NewRuleEngine creates an engine with the given rules and builds the metric index.
// All rules are indexed by "agentID:metricName" for consistent per-agent matching.
// An empty AgentID in the config is stored as "*" (wildcard — matches any agent).
func NewRuleEngine(rules []Rule, logger *slog.Logger) *RuleEngine {
	e := &RuleEngine{rules: rules, logger: logger}

	e.index = make(map[string][]int)
	for i, rule := range rules {
		cfg := extractRuleConfig(rule)
		if cfg == nil {
			// Fallback: index by metric name for unknown rule types.
			e.index[rule.Metric()] = append(e.index[rule.Metric()], i)
			continue
		}
		// Normalize empty AgentID to "*" for consistent indexing.
		agentID := cfg.AgentID
		if agentID == "" {
			agentID = "*"
		}
		key := agentID + ":" + cfg.Metric
		e.index[key] = append(e.index[key], i)
	}

	return e
}

// extractRuleConfig extracts RuleConfig from a rule if it is a known rule type.
func extractRuleConfig(rule Rule) *RuleConfig {
	switch r := rule.(type) {
	case *ThresholdRule:
		return &r.cfg
	case *LuaRule:
		return &r.cfg
	case *MLRule:
		return &r.cfg
	case *KSRule:
		return &r.cfg
	default:
		return nil
	}
}

// Evaluate evaluates all rules that match the given metric.
// Rules are matched by agentID:metricName. If no exact match exists,
// a wildcard rule (AgentID="*") is tried as fallback.
func (e *RuleEngine) Evaluate(m Metric) []*Anomaly {
	var anomalies []*Anomaly

	agentID := m.AgentID
	if agentID == "" {
		agentID = "*"
	}

	// Try exact agentID:metricName match first.
	if indices, ok := e.index[agentID+":"+m.Name]; ok {
		for _, idx := range indices {
			a := e.rules[idx].Evaluate(m)
			if a == nil {
				continue
			}
			a.AgentID = m.AgentID
			anomalies = append(anomalies, a)
		}
	}

	// Fallback: wildcard "*" rules match any agent (skip if already used).
	if agentID != "*" {
		if indices, ok := e.index["*:"+m.Name]; ok {
			for _, idx := range indices {
				a := e.rules[idx].Evaluate(m)
				if a == nil {
					continue
				}
				a.AgentID = m.AgentID
				anomalies = append(anomalies, a)
			}
		}
	}

	return anomalies
}

// GetMLRules returns training configuration for all ML rules.
func (e *RuleEngine) GetMLRules() []MLRuleInfo {
	var result []MLRuleInfo
	for _, rule := range e.rules {
		cfg := extractRuleConfig(rule)
		if cfg == nil || cfg.Type != RuleTypeML {
			continue
		}
		result = append(result, MLRuleInfo{
			AgentID:           cfg.AgentID,
			Metric:            cfg.Metric,
			TrainIntervalMin:  cfg.TrainIntervalMin,
			TrainDataWindow:   cfg.TrainDataWindow,
			SeasonalityPeriod: cfg.SeasonalityPeriod,
			Order:             cfg.Order,
			SeasonalOrder:     cfg.SeasonalOrder,
		})
	}
	return result
}

// RuleSummary is a human-readable summary of a rule for the UI.
type RuleSummary struct {
	Name     string   `json:"name"`
	Type     RuleType `json:"type"`
	AgentID  string   `json:"agent_id"`
	Metric   string   `json:"metric"`
	Enabled  bool     `json:"enabled"`
	Severity Severity `json:"severity"`
}

// GetAllRules returns summaries of all registered rules.
func (e *RuleEngine) GetAllRules() []RuleSummary {
	result := make([]RuleSummary, 0, len(e.rules))
	for _, rule := range e.rules {
		cfg := extractRuleConfig(rule)
		if cfg == nil {
			continue
		}
		result = append(result, RuleSummary{
			Name:     cfg.Name,
			Type:     cfg.Type,
			AgentID:  cfg.AgentID,
			Metric:   cfg.Metric,
			Enabled:  cfg.Enabled,
			Severity: cfg.Severity,
		})
	}
	return result
}

// EvaluateBatch evaluates all rules against all metrics in the batch.
// For large batches (>= batchParallelThreshold metrics) it parallelizes
// evaluation across multiple goroutines for throughput at scale.
func (e *RuleEngine) EvaluateBatch(batch Batch) []*Anomaly {
	n := len(batch.Metrics)
	if n == 0 {
		return nil
	}

	// Pre-allocate result slice. Capacity hint: assume at most 2 anomalies per metric.
	result := make([]*Anomaly, 0, min(n*2, 1024))

	if n < batchParallelThreshold {
		// Sequential — lower overhead for small batches.
		for _, m := range batch.Metrics {
			fired := e.Evaluate(m)
			result = append(result, fired...)
		}
	} else {
		// Parallel — amortize evaluation across goroutines for high throughput.
		// Each goroutine computes anomalies for its chunk and stores results in a local slice.
		// mutex.Lock/unlock is only needed when merging the local result into the shared result —
		// this is the single point where goroutine results enter the final result.
		var wg sync.WaitGroup
		mu := sync.Mutex{}

		for i := 0; i < n; i += chunkSize {
			j := min(i+chunkSize, n)
			wg.Add(1)
			go func(slice []Metric) {
				defer wg.Done()
				local := make([]*Anomaly, 0, len(slice)*2)
				for _, m := range slice {
					local = append(local, e.Evaluate(m)...)
				}
				mu.Lock()
				result = append(result, local...)
				mu.Unlock()
			}(batch.Metrics[i:j])
		}
		wg.Wait()
	}

	return result
}
