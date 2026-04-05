package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mclyashko/anomaly-detector/services/fake-service/config"
	"github.com/mclyashko/anomaly-detector/services/fake-service/internal/metrics"
	"github.com/mclyashko/anomaly-detector/services/fake-service/internal/worker"
	"github.com/mclyashko/anomaly-detector/shared/pkg/buildlog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger := buildlog.New(cfg.LogLevel)
	reg := metrics.NewRegistry()

	mux := http.NewServeMux()
	mux.Handle("/metrics", reg.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap the whole mux so every request (including /metrics scrapes) is measured.
	handler := trackingMiddleware(reg, mux)

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool := worker.NewPool(cfg.WorkerCount, reg, logger)
	pool.Start(ctx)

	go func() {
		logger.Info("fake service listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
		}
	}()

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown error", "err", err)
	}
	logger.Info("fake service stopped")
}

// trackingMiddleware records latency and request count for every HTTP request.
func trackingMiddleware(reg *metrics.Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		reg.RecordRequest(time.Since(start), false)
	})
}
