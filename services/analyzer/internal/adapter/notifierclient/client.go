package notifierclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/port"
)

// Sender implements port.NotifierSender by POSTing anomaly events to the notifier service.
type Sender struct {
	url    string
	client *http.Client
	logger *slog.Logger
}

func NewSender(url string, logger *slog.Logger) *Sender {
	return &Sender{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: logger,
	}
}

func (s *Sender) Notify(ctx context.Context, anomalies []*core.Anomaly) error {
	body, err := json.Marshal(map[string]any{
		"anomalies": anomalies,
		"count":     len(anomalies),
	})
	if err != nil {
		return fmt.Errorf("marshal anomalies: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post to notifier: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("notifier returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// Verify Sender satisfies port.NotifierSender.
var _ port.NotifierSender = (*Sender)(nil)
