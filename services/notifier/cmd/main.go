package main

import (
	"context"
	stdlibhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapters/broker"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapters/channels/log"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapters/http"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapters/storage/postgres"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/adapters/ui"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/config"
	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
	"github.com/mclyashko/anomaly-detector/shared/pkg/buildlog"
	"log/slog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger := buildlog.New(cfg.LogLevel)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Connect to PostgreSQL.
	db, err := postgres.New(ctx, cfg.DBDSN, logger)
	if err != nil {
		logger.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	// Create repository and service.
	repo := postgres.NewRepository(db.Pool())
	logChannel := log.New(logger)
	svc := core.NewNotifierService(repo, logChannel, logger)

	// Create Kafka consumer for anomaly events.
	brokers := strings.Split(cfg.KafkaBrokers, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	consumer := broker.NewConsumer(broker.ConsumerConfig{Brokers: brokers}, svc, logger)

	// Create HTTP handlers.
	uiHandler := ui.New(svc, logger)
	handler := http.New(svc, uiHandler, logger)

	// Start HTTP server.
	srv := &stdlibhttp.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      handler.Routes(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		logger.Info("notifier listening", "port", cfg.HTTPPort)
		if err := srv.ListenAndServe(); err != nil && err != stdlibhttp.ErrServerClosed {
			logger.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Start Kafka consumer in background.
	go func() {
		if err := consumer.Consume(context.Background()); err != nil {
			logger.Error("kafka consumer error", "err", err)
		}
	}()

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, os.Interrupt, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down")
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()

	consumer.Stop()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
	logger.Info("notifier stopped")
}
