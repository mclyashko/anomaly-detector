package http

import (
	"log/slog"
	"net/http"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapter/ui"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

// Handler wires HTTP routes to the notifier service.
type Handler struct {
	svc         *core.NotifierService
	ui          *ui.UI
	logger      *slog.Logger
	analyzerURL string
}

// New creates a new HTTP handler.
func New(svc *core.NotifierService, ui *ui.UI, logger *slog.Logger, analyzerURL string) *Handler {
	return &Handler{svc: svc, ui: ui, logger: logger, analyzerURL: analyzerURL}
}

// Routes returns the HTTP router with all routes.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// Health check.
	mux.HandleFunc("GET /healthz", h.handleHealth)

	// API routes.
	mux.HandleFunc("GET /api/v1/incidents", h.handleListIncidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", h.handleGetIncident)
	mux.HandleFunc("POST /api/v1/incidents/{id}/escalate", h.handleEscalate)
	mux.HandleFunc("POST /api/v1/incidents/{id}/resolve", h.handleResolve)
	mux.HandleFunc("POST /api/v1/incidents/{id}/comment", h.handleComment)
	mux.HandleFunc("POST /api/v1/notifications", h.handleNotification)
	mux.HandleFunc("GET /api/v1/rules", h.handleRules)

	// UI routes.
	mux.HandleFunc("GET /", h.ui.HandleList)
	mux.HandleFunc("GET /incidents/{id}", h.ui.HandleDetail)
	mux.HandleFunc("GET /rules", h.ui.HandleRules)

	return mux
}
