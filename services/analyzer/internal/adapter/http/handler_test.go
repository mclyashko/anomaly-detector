package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	httphandler "github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/http"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// --- test double ---

type mockAnalyzer struct {
	batches []core.Batch
}

func (m *mockAnalyzer) ProcessBatch(_ context.Context, batch core.Batch) {
	m.batches = append(m.batches, batch)
}

type mockMLRulesProvider struct {
	rules []core.MLRuleInfo
}

func (p *mockMLRulesProvider) GetMLRules() []core.MLRuleInfo {
	return p.rules
}

type mockRulesProvider struct {
	rules []core.RuleSummary
}

func (p *mockRulesProvider) GetAllRules() []core.RuleSummary {
	return p.rules
}

// --- helpers ---

func newHandler() (*httphandler.Handler, *mockAnalyzer) {
	analyzer := &mockAnalyzer{}
	mlRules := &mockMLRulesProvider{}
	rulesProvider := &mockRulesProvider{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httphandler.New(analyzer, mlRules, rulesProvider, logger), analyzer
}

func postJSON(handler http.Handler, path string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func payload(agentID string, metrics []map[string]any) []byte {
	b, _ := json.Marshal(map[string]any{
		"agent_id": agentID,
		"metrics":  metrics,
	})
	return b
}

// --- tests ---

func TestHandleAnalyze_ValidBatch_Returns202(t *testing.T) {
	h, _ := newHandler()
	w := postJSON(h.Routes(), "/api/v1/analyze", payload("agent-1", []map[string]any{
		{"name": "cpu", "value": 0.95, "timestamp": time.Now().Format(time.RFC3339), "type": "gauge"},
	}))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
}

func TestHandleAnalyze_ForwardsBatch(t *testing.T) {
	h, analyzer := newHandler()
	ts := time.Now().Format(time.RFC3339)
	postJSON(h.Routes(), "/api/v1/analyze", payload("agent-X", []map[string]any{
		{"name": "cpu", "value": 0.5, "timestamp": ts, "type": "gauge"},
		{"name": "mem", "value": 0.3, "timestamp": ts, "type": "gauge"},
	}))

	if len(analyzer.batches) != 1 {
		t.Fatalf("got %d batches, want 1", len(analyzer.batches))
	}
	b := analyzer.batches[0]
	if b.AgentID != "agent-X" {
		t.Errorf("agent_id = %q, want agent-X", b.AgentID)
	}
	if len(b.Metrics) != 2 {
		t.Errorf("metrics count = %d, want 2", len(b.Metrics))
	}
}

func TestHandleAnalyze_MalformedJSON_Returns400(t *testing.T) {
	h, _ := newHandler()
	w := postJSON(h.Routes(), "/api/v1/analyze", []byte("not-json{"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleAnalyze_EmptyBody_Returns202(t *testing.T) {
	// JSON is valid but missing required fields; the adapter does not validate
	// domain-level requirements — those belong in core. A 202 means
	// the request was accepted for processing.
	h, _ := newHandler()
	w := postJSON(h.Routes(), "/api/v1/analyze", []byte(`{"agent_id":"","metrics":[]}`))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
}

func TestHealthz_Returns200(t *testing.T) {
	h, _ := newHandler()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}

func TestMLRules_ReturnsRules(t *testing.T) {
	mlRules := &mockMLRulesProvider{
		rules: []core.MLRuleInfo{
			{AgentID: "agent-1", Metric: "cpu", TrainIntervalMin: 30, TrainDataWindow: 500, SeasonalityPeriod: 60, Order: []int{1, 0, 1}, SeasonalOrder: []int{1, 1, 1, 60}},
		},
	}
	handler := httphandler.New(&mockAnalyzer{}, mlRules, &mockRulesProvider{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ml-rules", nil)
	w := httptest.NewRecorder()
	handler.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	var rules []core.MLRuleInfo
	if err := json.NewDecoder(w.Body).Decode(&rules); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(rules) != 1 {
		t.Errorf("got %d rules, want 1", len(rules))
	}
	if rules[0].AgentID != "agent-1" {
		t.Errorf("agent_id = %q, want agent-1", rules[0].AgentID)
	}
}
