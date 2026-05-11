package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// handleListIncidents returns all incidents with optional filtering and pagination.
func (h *Handler) handleListIncidents(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Parse query params.
	page := 1
	pageSize := 20
	if p := r.URL.Query().Get("page"); p != "" {
		if parsed, err := parsePositiveInt(p); err == nil && parsed > 0 {
			page = parsed
		}
	}
	if ps := r.URL.Query().Get("page_size"); ps != "" {
		if parsed, err := parsePositiveInt(ps); err == nil && parsed > 0 {
			pageSize = parsed
		}
	}

	statusVals := r.URL.Query()["status"]
	severityVals := r.URL.Query()["severity"]

	// If no filters, use simple List.
	if len(statusVals) == 0 && len(severityVals) == 0 {
		incidents, err := h.svc.ListIncidents(ctx)
		if err != nil {
			h.logger.Error("failed to list incidents", "err", err)
			http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"incidents": incidents, "total": len(incidents), "page": 1, "page_size": len(incidents)})
		return
	}

	// Build filters.
	var filters core.IncidentFilters
	for _, s := range statusVals {
		if st := core.IncidentStatus(s); st == core.StatusOpen || st == core.StatusUpdated || st == core.StatusEscalated || st == core.StatusResolved {
			filters.Status = append(filters.Status, st)
		}
	}
	filters.Severity = severityVals

	incidents, total, err := h.svc.ListIncidentsFiltered(ctx, filters, page, pageSize)
	if err != nil {
		h.logger.Error("failed to list filtered incidents", "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"incidents": incidents, "total": total, "page": page, "page_size": pageSize})
}

func parsePositiveInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// handleGetIncident returns a single incident with events and comments.
func (h *Handler) handleGetIncident(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
		return
	}

	incident, err := h.svc.GetIncident(r.Context(), id)
	if err != nil {
		if errors.Is(err, core.ErrIncidentNotFound) {
			http.Error(w, `{"error":"incident not found"}`, http.StatusNotFound)
			return
		}
		h.logger.Error("failed to get incident", "err", err, "id", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(incident)
}

// handleEscalate escalates an incident.
func (h *Handler) handleEscalate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
		return
	}

	incident, err := h.svc.Escalate(r.Context(), id)
	if err != nil {
		if errors.Is(err, core.ErrIncidentNotFound) {
			http.Error(w, `{"error":"incident not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, core.ErrInvalidStatusTransition) {
			http.Error(w, `{"error":"cannot escalate resolved incident"}`, http.StatusConflict)
			return
		}
		h.logger.Error("failed to escalate incident", "err", err, "id", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(incident)
}

// handleResolve resolves an incident.
func (h *Handler) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Resolution string `json:"resolution"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	incident, err := h.svc.Resolve(r.Context(), id, req.Resolution)
	if err != nil {
		if errors.Is(err, core.ErrIncidentNotFound) {
			http.Error(w, `{"error":"incident not found"}`, http.StatusNotFound)
			return
		}
		if errors.Is(err, core.ErrInvalidStatusTransition) {
			http.Error(w, `{"error":"already resolved"}`, http.StatusConflict)
			return
		}
		h.logger.Error("failed to resolve incident", "err", err, "id", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(incident)
}

// handleComment adds a comment to an incident.
func (h *Handler) handleComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		http.Error(w, `{"error":"missing id"}`, http.StatusBadRequest)
		return
	}

	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.Text) == "" {
		http.Error(w, `{"error":"text is required"}`, http.StatusBadRequest)
		return
	}

	comment, err := h.svc.AddComment(r.Context(), id, req.Text)
	if err != nil {
		if errors.Is(err, core.ErrIncidentNotFound) {
			http.Error(w, `{"error":"incident not found"}`, http.StatusNotFound)
			return
		}
		h.logger.Error("failed to add comment", "err", err, "id", id)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(comment)
}

// handleNotification receives anomaly events from the analyzer.
func (h *Handler) handleNotification(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		h.logger.Warn("failed to read request body", "err", err)
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Try wrapper format: {"anomalies": [...], "count": N}
	var wrapper struct {
		Anomalies []struct {
			Rule      string  `json:"rule"`
			Metric    string  `json:"metric"`
			Value     float64 `json:"value"`
			Condition string  `json:"condition"`
			Severity  string  `json:"severity"`
			Timestamp string  `json:"timestamp"`
			AgentID   string  `json:"agent_id,omitempty"`
			Message   string  `json:"message,omitempty"`
		} `json:"anomalies"`
		Count int `json:"count"`
	}

	if err := json.Unmarshal(body, &wrapper); err == nil && len(wrapper.Anomalies) > 0 {
		// Collect all results first; fail atomically on non-duplicate errors.
		created := 0
		for _, a := range wrapper.Anomalies {
			payload := &core.AnomalyPayload{
				Rule:     a.Rule,
				Service:  a.AgentID,
				AgentID:  a.AgentID,
				Metric:   a.Metric,
				Value:    a.Value,
				Severity: a.Severity,
				Message:  a.Message,
			}
			incident, err := h.svc.HandleAnomaly(ctx, payload)
			if err != nil {
				h.logger.Error("failed to handle anomaly in batch",
					"index", created, "rule", a.Rule, "err", err)
				http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
				return
			}
			if incident != nil {
				created++
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"incident_id": "batch", "count": created})
		return
	}

	// Try direct AnomalyPayload format: {"rule": ..., "service": ..., ...}
	var payload core.AnomalyPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		h.logger.Warn("malformed notification payload", "err", err)
		http.Error(w, `{"error":"invalid JSON"}`, http.StatusBadRequest)
		return
	}

	incident, err := h.svc.HandleAnomaly(ctx, &payload)
	if err != nil {
		h.logger.Error("failed to handle anomaly", "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"incident_id": incident.ID})
}

// handleHealth returns 200 OK for load balancer health checks.
func (h *Handler) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
}

// handleRules fetches all rules from the analyzer service and returns them.
func (h *Handler) handleRules(w http.ResponseWriter, r *http.Request) {
	resp, err := http.Get(h.analyzerURL + "/api/v1/rules")
	if err != nil {
		h.logger.Warn("failed to fetch rules from analyzer", "err", err)
		http.Error(w, `{"error":"analyzer unavailable"}`, http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":"failed to read response"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(body)
}
