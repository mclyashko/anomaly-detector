package metrics_test

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/fake-service/internal/metrics"
)

func TestRegistry_RecordRequest_Counts(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordRequest(5*time.Millisecond, false)
	reg.RecordRequest(3*time.Millisecond, false)

	s := reg.Snapshot()
	if s.HTTPRequestsTotal != 2 {
		t.Errorf("requests = %d, want 2", s.HTTPRequestsTotal)
	}
	if s.HTTPErrorsTotal != 0 {
		t.Errorf("errors = %d, want 0", s.HTTPErrorsTotal)
	}
	// avg = (5 + 3) / 2 = 4 ms
	if math.Abs(s.HTTPLatencyAvgMs-4.0) > 0.001 {
		t.Errorf("avg latency = %v, want 4.0", s.HTTPLatencyAvgMs)
	}
}

func TestRegistry_RecordRequest_ErrorFlag(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.RecordRequest(time.Millisecond, true)
	reg.RecordRequest(time.Millisecond, false)

	s := reg.Snapshot()
	if s.HTTPRequestsTotal != 2 {
		t.Errorf("requests = %d, want 2", s.HTTPRequestsTotal)
	}
	if s.HTTPErrorsTotal != 1 {
		t.Errorf("errors = %d, want 1", s.HTTPErrorsTotal)
	}
}

func TestRegistry_WorkerOps_Accumulates(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.AddWorkerOps(100)
	reg.AddWorkerOps(250)

	if got := reg.Snapshot().WorkerOpsTotal; got != 350 {
		t.Errorf("worker_ops = %d, want 350", got)
	}
}

func TestRegistry_Snapshot_ZeroLatency_WhenEmpty(t *testing.T) {
	reg := metrics.NewRegistry()
	if s := reg.Snapshot(); s.HTTPLatencyAvgMs != 0 {
		t.Errorf("avg latency = %v, want 0 on empty registry", s.HTTPLatencyAvgMs)
	}
}

func TestRegistry_Handler_ServesJSON(t *testing.T) {
	reg := metrics.NewRegistry()
	reg.AddWorkerOps(42)
	reg.RecordRequest(10*time.Millisecond, false)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w := httptest.NewRecorder()
	reg.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var s metrics.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if s.WorkerOpsTotal != 42 {
		t.Errorf("worker_ops_total = %d, want 42", s.WorkerOpsTotal)
	}
	if s.HTTPRequestsTotal != 1 {
		t.Errorf("http_requests_total = %d, want 1", s.HTTPRequestsTotal)
	}
}

func TestRegistry_Concurrent_NoDataRace(t *testing.T) {
	reg := metrics.NewRegistry()
	const n = 200
	var wg sync.WaitGroup

	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.RecordRequest(time.Millisecond, false)
		}()
	}
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reg.AddWorkerOps(1)
		}()
	}
	wg.Wait()

	s := reg.Snapshot()
	if s.HTTPRequestsTotal != n {
		t.Errorf("requests = %d, want %d", s.HTTPRequestsTotal, n)
	}
	if s.WorkerOpsTotal != n {
		t.Errorf("worker_ops = %d, want %d", s.WorkerOpsTotal, n)
	}
}
