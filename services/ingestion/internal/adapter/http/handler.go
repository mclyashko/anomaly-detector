package http

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

// batchHandler is the only core capability the HTTP layer depends on.
type batchHandler interface {
	HandleBatch(ctx context.Context, b core.Batch) error
}

// Handler wires HTTP routes to the ingestion service.
type Handler struct {
	svc    batchHandler
	logger *slog.Logger
}

func New(svc batchHandler, logger *slog.Logger) *Handler {
	return &Handler{svc: svc, logger: logger}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/ingest", h.handleIngest)
	mux.HandleFunc("GET /healthz", h.handleHealth)
	return mux
}

// --- request/response types ---

// ingestRequest mirrors the JSON payload that the agent service sends.
type ingestRequest struct {
	AgentID   string          `json:"agent_id"`
	Metrics   []metricRequest `json:"metrics"`
	CreatedAt time.Time       `json:"created_at"`
}

type metricRequest struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Type      string            `json:"type"`
}

func (r ingestRequest) toDomain() core.Batch {
	metrics := make([]core.Metric, len(r.Metrics))
	for i, m := range r.Metrics {
		metrics[i] = core.Metric{
			Name:      m.Name,
			Value:     m.Value,
			Labels:    m.Labels,
			Timestamp: m.Timestamp,
			Type:      core.MetricType(m.Type),
		}
	}
	return core.Batch{AgentID: r.AgentID, Metrics: metrics}
}

type errorResponse struct {
	Error string `json:"error"`
}

// --- handlers ---

func (h *Handler) handleIngest(w http.ResponseWriter, r *http.Request) {
	var req ingestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logger.Warn("malformed request body", "remote", r.RemoteAddr, "err", err)
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batch := req.toDomain()

	if err := h.svc.HandleBatch(r.Context(), batch); err != nil {
		var ve *core.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusUnprocessableEntity, ve.Error())
			return
		}
		h.logger.Error("HandleBatch failed", "agent_id", batch.AgentID, "err", err)
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(errorResponse{Error: msg}) //nolint:errcheck
}
