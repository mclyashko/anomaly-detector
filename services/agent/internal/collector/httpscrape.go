package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// scrapeResponse mirrors fake-service's metrics.Snapshot JSON.
type scrapeResponse struct {
	HTTPRequestsTotal int64   `json:"http_requests_total"`
	HTTPErrorsTotal   int64   `json:"http_errors_total"`
	HTTPLatencyAvgMs  float64 `json:"http_latency_avg_ms"`
	WorkerOpsTotal    int64   `json:"worker_ops_total"`
	TestSignal        float64 `json:"test_signal"`
}

// HTTPScrapeCollector calls a service's /metrics endpoint and maps the
// response to domain.Metric values. No simulation — these are real numbers.
type HTTPScrapeCollector struct {
	serviceName string
	metricsURL  string
	client      *http.Client
}

// NewHTTPScrapeCollector creates an HTTP scraper for the specified service.
// serviceName is used as a label on metrics, metricsURL is the /metrics endpoint.
func NewHTTPScrapeCollector(serviceName, metricsURL string) *HTTPScrapeCollector {
	return &HTTPScrapeCollector{
		serviceName: serviceName,
		metricsURL:  metricsURL,
		client:      &http.Client{Timeout: 3 * time.Second},
	}
}

// Name returns the collector name in the format "http_scrape:{serviceName}".
func (c *HTTPScrapeCollector) Name() string { return "http_scrape:" + c.serviceName }

func (c *HTTPScrapeCollector) Collect(ctx context.Context) ([]domain.Metric, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.metricsURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("scrape %s: %w", c.metricsURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scrape %s: status %d", c.metricsURL, resp.StatusCode)
	}

	var s scrapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		return nil, fmt.Errorf("decode metrics from %s: %w", c.metricsURL, err)
	}

	labels := map[string]string{"service": c.serviceName}
	now := time.Now().UTC()

	return []domain.Metric{
		{Name: "http_requests_total", Value: float64(s.HTTPRequestsTotal), Labels: labels, Type: domain.MetricTypeCounter, Timestamp: now},
		{Name: "http_errors_total", Value: float64(s.HTTPErrorsTotal), Labels: labels, Type: domain.MetricTypeCounter, Timestamp: now},
		{Name: "http_latency_avg_ms", Value: s.HTTPLatencyAvgMs, Labels: labels, Type: domain.MetricTypeGauge, Timestamp: now},
		{Name: "worker_ops_total", Value: float64(s.WorkerOpsTotal), Labels: labels, Type: domain.MetricTypeCounter, Timestamp: now},
		{Name: "test_signal", Value: s.TestSignal, Labels: labels, Type: domain.MetricTypeGauge, Timestamp: now},
	}, nil
}
