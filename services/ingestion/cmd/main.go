package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mclyashko/anomaly-detector/services/ingestion/config"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/broker"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/db"
	httphandler "github.com/mclyashko/anomaly-detector/services/ingestion/internal/adapter/http"
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
		logger.Info("using in-memory storage")

	case "postgres":
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

	var consumer *broker.KafkaConsumer
	if cfg.EnableBroker {
		brokers := strings.Split(cfg.BrokerBrokers, ",")
		for i := range brokers {
			brokers[i] = strings.TrimSpace(brokers[i])
		}
		consumer, err = broker.NewKafkaConsumer(broker.ConsumerConfig{
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
	}

	handler := httphandler.New(svc, logger)
	srv := &http.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      handler.Routes(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		logger.Info("ingestion listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "err", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}

	if consumer != nil {
		consumer.Stop()
	}

	if storageCloser != nil {
		storageCloser()
	}
	logger.Info("ingestion stopped")
}
