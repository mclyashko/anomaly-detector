package sender

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// HTTPSender POSTs metric batches to the ingestion service as JSON.
// Retries with exponential backoff on transient failures.
type HTTPSender struct {
	url        string
	client     *http.Client
	maxRetries int
	logger     *slog.Logger
}

func NewHTTPSender(url string, timeout time.Duration, maxRetries int, logger *slog.Logger) *HTTPSender {
	return &HTTPSender{
		url:        url,
		client:     &http.Client{Timeout: timeout},
		maxRetries: maxRetries,
		logger:     logger,
	}
}

// Send posts the batch, retrying up to maxRetries times with exponential backoff.
// maxRetries=3 means up to 4 total attempts (initial + 3 retries).
func (s *HTTPSender) Send(ctx context.Context, batch domain.Batch) error {
	body, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("marshal batch: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			s.logger.Warn("retrying send", "attempt", attempt, "backoff_ms", backoff.Milliseconds())
		}

		if err := s.post(ctx, body); err != nil {
			lastErr = err
			s.logger.Error("send attempt failed", "attempt", attempt, "err", err)
			continue
		}

		s.logger.Debug("batch delivered", "metrics", len(batch.Metrics))
		return nil
	}

	return fmt.Errorf("all %d send attempts failed: %w", s.maxRetries+1, lastErr)
}

func (s *HTTPSender) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("ingestion returned %d", resp.StatusCode)
	}
	return nil
}
