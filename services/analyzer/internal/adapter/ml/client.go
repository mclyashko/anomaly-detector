package ml

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client calls the Python ML Training Service for inference.
type Client struct {
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

// Config holds ML service connection settings.
type Config struct {
	URL     string        // e.g. "http://ml-service:8085"
	Timeout time.Duration // request timeout
}

// NewClient creates a new ML service client.
func NewClient(cfg Config) *Client {
	return &Client{
		baseURL: cfg.URL,
		timeout: cfg.Timeout,
		httpClient: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// EvaluateRequest is sent to the ML service.
type EvaluateRequest struct {
	ModelID string    `json:"model_id"`
	History []float64 `json:"history"`
	Value   float64   `json:"value"`
}

// EvaluateResponse is received from the ML service.
type EvaluateResponse struct {
	ModelID  string  `json:"model_id"`
	Anomaly  bool    `json:"anomaly"`
	Forecast float64 `json:"forecast"`
	LowerCI  float64 `json:"lower_ci"`
	UpperCI  float64 `json:"upper_ci"`
	Value    float64 `json:"value"`
	Message  string  `json:"message"`
}

// Evaluate sends a single evaluation request to the ML service.
func (c *Client) Evaluate(ctx context.Context, modelID string, history []float64, value float64) (*EvaluateResponse, error) {
	reqBody := EvaluateRequest{
		ModelID: modelID,
		History: history,
		Value:   value,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.baseURL + "/api/v1/evaluate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ml service request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ml service returned status %d", resp.StatusCode)
	}

	var result EvaluateResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &result, nil
}

// Health checks if the ML service is available.
func (c *Client) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health check failed: status %d", resp.StatusCode)
	}
	return nil
}
