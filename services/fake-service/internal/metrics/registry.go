package metrics

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
)

// Registry holds all service counters using lock-free atomics.
type Registry struct {
	requestsTotal  atomic.Int64
	errorsTotal    atomic.Int64
	latencyTotalNs atomic.Int64 // nanoseconds; converted to ms on snapshot
	workerOpsTotal atomic.Int64
}

func NewRegistry() *Registry { return &Registry{} }

// RecordRequest is called by HTTP middleware after each request completes.
func (r *Registry) RecordRequest(d time.Duration, isError bool) {
	r.requestsTotal.Add(1)
	r.latencyTotalNs.Add(d.Nanoseconds())
	if isError {
		r.errorsTotal.Add(1)
	}
}

// AddWorkerOps is called by workers to report completed operations.
func (r *Registry) AddWorkerOps(n int64) {
	r.workerOpsTotal.Add(n)
}

// Snapshot is the JSON-serialisable view exposed on /metrics.
type Snapshot struct {
	HTTPRequestsTotal int64   `json:"http_requests_total"`
	HTTPErrorsTotal   int64   `json:"http_errors_total"`
	HTTPLatencyAvgMs  float64 `json:"http_latency_avg_ms"`
	WorkerOpsTotal    int64   `json:"worker_ops_total"`
}

func (r *Registry) Snapshot() Snapshot {
	reqs := r.requestsTotal.Load()
	var avgMs float64
	if reqs > 0 {
		avgMs = float64(r.latencyTotalNs.Load()) / float64(reqs) / 1e6
	}
	return Snapshot{
		HTTPRequestsTotal: reqs,
		HTTPErrorsTotal:   r.errorsTotal.Load(),
		HTTPLatencyAvgMs:  avgMs,
		WorkerOpsTotal:    r.workerOpsTotal.Load(),
	}
}

// Handler serves GET /metrics as JSON.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r.Snapshot()) //nolint:errcheck
	})
}
