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

	// index maps metric name → rule indices that apply to that metric.
	// Built once at construction; provides O(1) rule lookup per metric
	// instead of O(n) linear scan through all rules.
	index map[string][]int
}

// NewRuleEngine creates an engine with the given rules and builds the metric index.
func NewRuleEngine(rules []Rule, logger *slog.Logger) *RuleEngine {
	e := &RuleEngine{rules: rules, logger: logger}

	// Build metric-name → rule-indices index.
	// This is the key optimization: for each metric we only evaluate rules
	// that target its name, not all rules.
	e.index = make(map[string][]int)
	for i, rule := range rules {
		m := rule.Metric()
		e.index[m] = append(e.index[m], i)
	}

	return e
}

// Evaluate evaluates all rules that match the given metric's name.
// It is lock-free and allocation-free in the fast path (no-mismatch case).
// Returns all anomalies that fired.
func (e *RuleEngine) Evaluate(m Metric) []*Anomaly {
	// O(1) map lookup: only check rules that apply to this metric name.
	indices, ok := e.index[m.Name]
	if !ok {
		return nil
	}

	var anomalies []*Anomaly
	for _, idx := range indices {
		a := e.rules[idx].Evaluate(m)
		if a == nil {
			continue
		}
		// Set the AgentID from the metric.
		a.AgentID = m.AgentID
		anomalies = append(anomalies, a)
	}
	return anomalies
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
