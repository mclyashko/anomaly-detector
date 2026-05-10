package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mclyashko/anomaly-detector/services/ingestion/config"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/broker"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/db"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
	"github.com/mclyashko/anomaly-detector/shared/pkg/buildlog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger := buildlog.New(cfg.LogLevel)

	var storage core.Storage
	var storageCloser func()

	switch cfg.StorageMode {
	case "memory":
		storage = db.NewMemoryStorage()
		logger.Warn("using in-memory storage — data will be lost on restart")

	case "db":
		ctx := context.Background()
		pg, pool, err := db.NewPostgresStorage(ctx, cfg.DBDSN, logger)
		if err != nil {
			logger.Error("failed to connect to postgres", "err", err)
			os.Exit(1)
		}
		storage = pg
		storageCloser = pg.Close

		migrator := db.NewMigrator(pool, logger)
		if err := migrator.Up(ctx); err != nil {
			logger.Error("migration failed", "err", err)
			os.Exit(1)
		}

	default:
		logger.Error("unsupported STORAGE_MODE", "mode", cfg.StorageMode)
		os.Exit(1)
	}

	svc := core.NewIngestionService(storage, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if !cfg.EnableBroker {
		logger.Error("broker (Kafka) must be enabled — no HTTP fallback")
		os.Exit(1)
	}

	brokers := strings.Split(cfg.BrokerBrokers, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	consumer, err := broker.NewKafkaConsumer(broker.ConsumerConfig{
		Brokers:  brokers,
		Topic:    cfg.BrokerTopic,
		GroupID:  cfg.BrokerGroupID,
		Workers:  cfg.BrokerWorkers,
		Capacity: cfg.BrokerCapacity,
	}, svc, logger)
	if err != nil {
		logger.Error("failed to create kafka consumer", "err", err)
		os.Exit(1)
	}
	go func() {
		logger.Info("kafka consumer starting")
		if err := consumer.Start(ctx); err != nil {
			logger.Error("kafka consumer error", "err", err)
		}
	}()

	// Minimal HTTP health server for docker healthcheck.
	go func() {
		http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		})
		logger.Info("health server listening on :8080")
		if err := http.ListenAndServe(":8080", nil); err != nil {
			logger.Debug("health server stopped", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	if consumer != nil {
		consumer.Stop()
	}

	if storageCloser != nil {
		storageCloser()
	}
	logger.Info("ingestion stopped")
}
