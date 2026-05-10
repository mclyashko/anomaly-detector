package metrics

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"sync/atomic"
	"time"
)

// SignalType represents which signal is currently active.
type SignalType int

const (
	SignalA SignalType = iota
	SignalB
)

func (s SignalType) String() string {
	if s == SignalA {
		return "A"
	}
	return "B"
}

// Registry holds all service counters using lock-free atomics.
type Registry struct {
	requestsTotal  atomic.Int64
	errorsTotal    atomic.Int64
	latencyTotalNs atomic.Int64 // nanoseconds; converted to ms on snapshot
	workerOpsTotal atomic.Int64

	// Signal generation: two sinusoidal signals with different seasonality
	// Use int64 bit representation for float64 atomic operations
	signalA      atomic.Int64
	signalB      atomic.Int64
	activeSignal atomic.Int64 // 0 = A, 1 = B

	// Ticker for signal updates
	tickCount atomic.Int64
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
	TestSignal        float64 `json:"test_signal"` // current active signal value
	ActiveSignal      string  `json:"active_signal"` // "A" or "B"
}

func (r *Registry) Snapshot() Snapshot {
	reqs := r.requestsTotal.Load()
	var avgMs float64
	if reqs > 0 {
		avgMs = float64(r.latencyTotalNs.Load()) / float64(reqs) / 1e6
	}

	active := r.activeSignal.Load()
	var signalVal float64
	if active == 0 {
		signalVal = math.Float64frombits(uint64(r.signalA.Load()))
	} else {
		signalVal = math.Float64frombits(uint64(r.signalB.Load()))
	}

	return Snapshot{
		HTTPRequestsTotal: reqs,
		HTTPErrorsTotal:   r.errorsTotal.Load(),
		HTTPLatencyAvgMs:  avgMs,
		WorkerOpsTotal:    r.workerOpsTotal.Load(),
		TestSignal:       signalVal,
		ActiveSignal:      map[bool]string{true: "B", false: "A"}[active == 1],
	}
}

// GetActiveSignal returns the currently active signal type.
func (r *Registry) GetActiveSignal() SignalType {
	if r.activeSignal.Load() == 0 {
		return SignalA
	}
	return SignalB
}

// SwitchSignal toggles the active signal between A and B.
func (r *Registry) SwitchSignal() SignalType {
	old := r.activeSignal.Load()
	var newVal int64
	if old == 0 {
		newVal = 1
	} else {
		newVal = 0
	}
	r.activeSignal.Store(newVal)
	if newVal == 0 {
		return SignalA
	}
	return SignalB
}

// StartSignalGenerator starts a goroutine that updates both signals every interval.
// Must be called once, typically in a goroutine.
func (r *Registry) StartSignalGenerator(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.tickSignals()
		}
	}
}

// tickSignals updates both signals with sinusoidal patterns.
// SignalA: period=60, amplitude 4.0, minimal noise ±0.021
// SignalB: period=30, amplitude 4.0, same noise (twice as fast — distinguishable by SARIMA).
func (r *Registry) tickSignals() {
	t := float64(r.tickCount.Add(1))

	const (
		signalAmplitude = 4.0
		noiseScale      = 0.007 // ±0.021 noise — gives residual_std ≈ 0.02-0.04
	)

	// Small deterministic "random": take t mod 7 → 0-6, subtract 3 → range [-3, +3],
	// multiply by noiseScale → ±0.021.
	// Not true random, but gives enough spread for realism.
	noise := (float64(int64(t)%7) - 3) * noiseScale

	signalA := math.Sin(2*math.Pi*t/60.0)*signalAmplitude + noise
	signalB := math.Sin(2*math.Pi*t/30.0)*signalAmplitude + noise

	r.signalA.Store(int64(math.Float64bits(signalA)))
	r.signalB.Store(int64(math.Float64bits(signalB)))
}

// Handler serves GET /metrics as JSON.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(r.Snapshot()) //nolint:errcheck
	})
}

// SignalStateHandler returns current signal state as JSON.
func (r *Registry) SignalStateHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"active_signal": r.GetActiveSignal().String(),
			"signal_a":       math.Float64frombits(uint64(r.signalA.Load())),
			"signal_b":       math.Float64frombits(uint64(r.signalB.Load())),
		}) //nolint:errcheck
	}
}

// SignalSwitchHandler toggles the active signal.
func (r *Registry) SignalSwitchHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		newSignal := r.SwitchSignal()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"active_signal": newSignal.String(),
		}) //nolint:errcheck
	}
}