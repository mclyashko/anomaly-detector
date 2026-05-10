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

	// index maps metric name → rule indices for threshold and lua rules.
	index map[string][]int

	// mlIndex maps "agentID:metricName" → rule indices for ML rules.
	// ML rules are indexed separately because they require agentID scoping.
	mlIndex map[string][]int
}

// NewRuleEngine creates an engine with the given rules and builds the metric index.
// ML rules are indexed by "agentID:metricName" for proper scoping.
func NewRuleEngine(rules []Rule, logger *slog.Logger) *RuleEngine {
	e := &RuleEngine{rules: rules, logger: logger}

	e.index = make(map[string][]int)
	e.mlIndex = make(map[string][]int)
	for i, rule := range rules {
		cfg := extractRuleConfig(rule)
		if cfg == nil {
			// Fallback: index by metric name for unknown rule types.
			e.index[rule.Metric()] = append(e.index[rule.Metric()], i)
			continue
		}
		switch cfg.Type {
		case RuleTypeML:
			// ML rules are indexed by agentID:metricName.
			key := cfg.AgentID + ":" + cfg.Metric
			e.mlIndex[key] = append(e.mlIndex[key], i)
		default:
			// Threshold and Lua rules indexed by metric name.
			e.index[rule.Metric()] = append(e.index[rule.Metric()], i)
		}
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
	default:
		return nil
	}
}

// Evaluate evaluates all rules that match the given metric.
// Threshold and Lua rules are matched by metric name.
// ML rules are matched by agentID:metricName.
func (e *RuleEngine) Evaluate(m Metric) []*Anomaly {
	var anomalies []*Anomaly

	// Threshold/Lua rules: O(1) lookup by metric name.
	if indices, ok := e.index[m.Name]; ok {
		for _, idx := range indices {
			a := e.rules[idx].Evaluate(m)
			if a == nil {
				continue
			}
			a.AgentID = m.AgentID
			anomalies = append(anomalies, a)
		}
	}

	// ML rules: lookup by agentID:metricName.
	mlKey := m.AgentID + ":" + m.Name
	if indices, ok := e.mlIndex[mlKey]; ok {
		for _, idx := range indices {
			a := e.rules[idx].Evaluate(m)
			if a == nil {
				continue
			}
			a.AgentID = m.AgentID
			anomalies = append(anomalies, a)
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
