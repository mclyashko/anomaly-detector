package ml_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/ml"
)

func TestClient_Health_OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","service":"ml-training"}`))
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	if err := client.Health(ctx); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestClient_Health_Non200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	if err := client.Health(ctx); err == nil {
		t.Error("expected non-nil error for non-200 status")
	}
}

func TestClient_Health_ConnectionError(t *testing.T) {
	client := ml.NewClient(ml.Config{
		URL:     "http://localhost:99999",
		Timeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	if err := client.Health(ctx); err == nil {
		t.Error("expected error for connection failure")
	}
}

func TestClient_Evaluate_Success(t *testing.T) {
	var receivedReq ml.EvaluateRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/evaluate" {
			t.Errorf("path = %q, want /api/v1/evaluate", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}

		if err := json.NewDecoder(r.Body).Decode(&receivedReq); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ml.EvaluateResponse{
			ModelID:  "agent-1__cpu",
			Anomaly:  true,
			Forecast: 75.5,
			LowerCI:  70.0,
			UpperCI:  81.0,
			Value:    85.0,
			Message:  "value=85.00 forecast=75.50 ci=[70.00, 81.00]",
		})
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	result, err := client.Evaluate(ctx, "agent-1__cpu", []float64{70, 72, 74, 76, 78}, 85.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Anomaly {
		t.Errorf("anomaly = false, want true")
	}
	if result.Forecast != 75.5 {
		t.Errorf("forecast = %v, want 75.5", result.Forecast)
	}
	if result.LowerCI != 70.0 {
		t.Errorf("lower_ci = %v, want 70.0", result.LowerCI)
	}
	if result.UpperCI != 81.0 {
		t.Errorf("upper_ci = %v, want 81.0", result.UpperCI)
	}
	if result.Message == "" {
		t.Error("message should be non-empty")
	}

	if receivedReq.ModelID != "agent-1__cpu" {
		t.Errorf("model_id = %q, want agent-1__cpu", receivedReq.ModelID)
	}
	if len(receivedReq.History) != 5 {
		t.Errorf("history len = %d, want 5", len(receivedReq.History))
	}
	if receivedReq.Value != 85.0 {
		t.Errorf("value = %v, want 85.0", receivedReq.Value)
	}
}

func TestClient_Evaluate_NoAnomaly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ml.EvaluateRequest
		json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(ml.EvaluateResponse{
			ModelID:  req.ModelID,
			Anomaly:  false,
			Forecast: 75.0,
			LowerCI:  70.0,
			UpperCI:  80.0,
			Value:    75.0,
			Message:  "value=75.00 forecast=75.00 ci=[70.00, 80.00]",
		})
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	result, err := client.Evaluate(ctx, "agent-1__cpu", []float64{70, 72, 74, 76, 78}, 75.0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Anomaly {
		t.Errorf("anomaly = true, want false")
	}
}

func TestClient_Evaluate_Non200(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	_, err := client.Evaluate(ctx, "model", []float64{1, 2, 3}, 2.5)
	if err == nil {
		t.Error("expected error for non-200 response")
	}
}

func TestClient_Evaluate_ConnectionError(t *testing.T) {
	client := ml.NewClient(ml.Config{
		URL:     "http://localhost:99998",
		Timeout: 100 * time.Millisecond,
	})

	ctx := context.Background()
	_, err := client.Evaluate(ctx, "model", []float64{1, 2, 3}, 2.5)
	if err == nil {
		t.Error("expected error for connection failure")
	}
}

func TestClient_Evaluate_InvalidJSONResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 5 * time.Second,
	})

	ctx := context.Background()
	_, err := client.Evaluate(ctx, "model", []float64{1, 2, 3}, 2.5)
	if err == nil {
		t.Error("expected error for invalid JSON response")
	}
}

func TestClient_Evaluate_CanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := ml.NewClient(ml.Config{
		URL:     server.URL,
		Timeout: 10 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := client.Evaluate(ctx, "model", []float64{1, 2, 3}, 2.5)
	if err == nil {
		t.Error("expected error for canceled context")
	}
}
