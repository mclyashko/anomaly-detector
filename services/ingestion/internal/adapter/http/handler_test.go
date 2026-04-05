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

	httphandler "github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/http"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

// --- test double ---

type mockService struct{ err error }

func (m *mockService) HandleBatch(_ context.Context, _ core.Batch) error { return m.err }

// --- helpers ---

func newHandler(svc *mockService) http.Handler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return httphandler.New(svc, logger).Routes()
}

func validBody(t *testing.T) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"agent_id": "agent-1",
		"metrics": []map[string]any{
			{"name": "cpu_usage", "value": 0.75, "timestamp": time.Now()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(handler http.Handler, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func assertErrorBody(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v — body: %s", err, w.Body)
	}
	if resp["error"] == "" {
		t.Errorf("expected non-empty 'error' field in JSON response")
	}
}

// --- tests ---

func TestHandleIngest_ValidRequest_Returns202(t *testing.T) {
	w := post(newHandler(&mockService{}), validBody(t))
	if w.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", w.Code)
	}
}

func TestHandleIngest_InvalidJSON_Returns400(t *testing.T) {
	w := post(newHandler(&mockService{}), []byte("{bad json"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	assertErrorBody(t, w)
}

func TestHandleIngest_ValidationError_Returns422(t *testing.T) {
	svc := &mockService{err: core.NewValidationError("agent_id is required")}
	w := post(newHandler(svc), validBody(t))
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
	assertErrorBody(t, w)
}

func TestHandleIngest_InternalError_Returns500(t *testing.T) {
	svc := &mockService{err: context.DeadlineExceeded}
	w := post(newHandler(svc), validBody(t))
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
	assertErrorBody(t, w)
}

func TestHandleIngest_ErrorResponseContentType(t *testing.T) {
	svc := &mockService{err: core.NewValidationError("bad")}
	w := post(newHandler(svc), validBody(t))
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestHealthz_Returns200(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	newHandler(&mockService{}).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
