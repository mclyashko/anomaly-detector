package http

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
)

// BatchHandler is the interface the HTTP layer depends on.
type BatchHandler interface {
	ProcessBatch(ctx context.Context, batch core.Batch)
}

// MLRulesProvider exposes ML rule training config for the training service.
type MLRulesProvider interface {
	GetMLRules() []core.MLRuleInfo
}

// Handler wires HTTP routes to the analyzer.
type Handler struct {
	svc     BatchHandler
	mlRules MLRulesProvider
	logger  *slog.Logger
}

func New(svc BatchHandler, mlRules MLRulesProvider, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, mlRules: mlRules, logger: logger}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/analyze", h.handleAnalyze)
	mux.HandleFunc("GET /api/v1/ml-rules", h.handleMLRules)
	mux.HandleFunc("GET /healthz", h.handleHealth)
	return mux
}

// ingestRequest mirrors the payload sent by the ingestion service's analyzer sender.
type ingestRequest struct {
	AgentID    string          `json:"agent_id"`
	ReceivedAt string          `json:"received_at"`
	Metrics    []metricRequest `json:"metrics"`
}

type metricRequest struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp string            `json:"timestamp"`
	Type      string            `json:"type"`
}

func (h *Handler) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("malformed request body", "err", err)
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	batch, invalidTS := req.toDomainWithTSCheck()

	if invalidTS > 0 {
		h.logger.Warn("some metrics had invalid timestamps, using current time",
			"count", invalidTS,
			"agent_id", batch.AgentID,
		)
	}

	h.svc.ProcessBatch(r.Context(), batch)
	w.WriteHeader(http.StatusAccepted)
}

func parseTimestampOrNow(s string) (time.Time, bool) {
	if s == "" {
		return time.Now().UTC(), true
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now().UTC(), true
	}
	return t, false
}

func (r ingestRequest) toDomainWithTSCheck() (core.Batch, int) {
	metrics := make([]core.Metric, len(r.Metrics))
	invalidTS := 0
	for i, m := range r.Metrics {
		ts, fellBack := parseTimestampOrNow(m.Timestamp)
		if fellBack {
			invalidTS++
		}
		metrics[i] = core.Metric{
			Name:      m.Name,
			Value:     m.Value,
			Labels:    m.Labels,
			Timestamp: ts,
			Type:      m.Type,
			AgentID:   r.AgentID,
		}
	}
	return core.Batch{
		AgentID: r.AgentID,
		Metrics: metrics,
	}, invalidTS
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleMLRules(w http.ResponseWriter, _ *http.Request) {
	rules := h.mlRules.GetMLRules()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(rules)
}
