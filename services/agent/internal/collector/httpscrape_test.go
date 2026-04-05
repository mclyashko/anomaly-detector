package collector_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/collector"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

func fakeMetricsServer(payload any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(payload) //nolint:errcheck
	}))
}

func TestHTTPScrapeCollector_ParsesResponse(t *testing.T) {
	srv := fakeMetricsServer(map[string]any{
		"http_requests_total": 100,
		"http_errors_total":   5,
		"http_latency_avg_ms": 12.5,
		"worker_ops_total":    999,
	})
	defer srv.Close()

	c := collector.NewHTTPScrapeCollector("svc", srv.URL)
	metrics, err := c.Collect(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(metrics) != 4 {
		t.Fatalf("got %d metrics, want 4", len(metrics))
	}

	byName := make(map[string]domain.Metric, len(metrics))
	for _, m := range metrics {
		byName[m.Name] = m
	}

	checks := map[string]float64{
		"http.requests_total": 100,
		"http.errors_total":   5,
		"http.latency_avg_ms": 12.5,
		"worker.ops_total":    999,
	}
	for name, want := range checks {
		m, ok := byName[name]
		if !ok {
			t.Errorf("metric %q not found", name)
			continue
		}
		if m.Value != want {
			t.Errorf("%q = %v, want %v", name, m.Value, want)
		}
		if m.Labels["service"] != "svc" {
			t.Errorf("%q label service = %q, want svc", name, m.Labels["service"])
		}
	}
}

func TestHTTPScrapeCollector_MetricTypes(t *testing.T) {
	srv := fakeMetricsServer(map[string]any{
		"http_requests_total": 1,
		"http_errors_total":   0,
		"http_latency_avg_ms": 1.0,
		"worker_ops_total":    1,
	})
	defer srv.Close()

	c := collector.NewHTTPScrapeCollector("svc", srv.URL)
	metrics, _ := c.Collect(context.Background())

	for _, m := range metrics {
		switch m.Name {
		case "http.latency_avg_ms":
			if m.Type != domain.MetricTypeGauge {
				t.Errorf("%q: want gauge, got %v", m.Name, m.Type)
			}
		default:
			if m.Type != domain.MetricTypeCounter {
				t.Errorf("%q: want counter, got %v", m.Name, m.Type)
			}
		}
	}
}

func TestHTTPScrapeCollector_NonOKStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := collector.NewHTTPScrapeCollector("svc", srv.URL)
	_, err := c.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error for non-200 response")
	}
}

func TestHTTPScrapeCollector_ServerDown(t *testing.T) {
	c := collector.NewHTTPScrapeCollector("svc", "http://127.0.0.1:19999")
	_, err := c.Collect(context.Background())
	if err == nil {
		t.Fatal("expected error when server is unreachable")
	}
}

func TestHTTPScrapeCollector_Name(t *testing.T) {
	c := collector.NewHTTPScrapeCollector("my-svc", "http://localhost")
	if c.Name() != "http_scrape:my-svc" {
		t.Errorf("Name() = %q, want http_scrape:my-svc", c.Name())
	}
}
