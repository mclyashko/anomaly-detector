package main

import (
	"context"
	stdlibhttp "net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/config"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/broker"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/lua"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/storage"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/storage/postgres"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/yaml"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
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

	// Create Lua executor for lua-type rules.
	luaExecutor := lua.New(logger)
	defer luaExecutor.Close()

	// --- Model Registry Setup ---
	var registry *core.ModelRegistry
	modelCfgs, err := yaml.LoadModelsConfig(cfg.ModelsConfigFile)
	if err != nil {
		logger.Error("failed to load models config", "path", cfg.ModelsConfigFile, "err", err)
		os.Exit(1)
	}
	if len(modelCfgs) > 0 {
		minioStorage, err := storage.NewMinIOStorage(
			cfg.MinIOEndpoint,
			cfg.MinIOAccessKey,
			cfg.MinIOSecretKey,
			cfg.MinIOBucket,
			cfg.MinIOUseSSL,
		)
		if err != nil {
			logger.Error("failed to create MinIO storage", "err", err)
			os.Exit(1)
		}
		registry = core.NewModelRegistry(minioStorage, modelCfgs, logger)
		// Initial load.
		if err := registry.LoadAll(context.Background()); err != nil {
			logger.Warn("failed to load initial models", "err", err)
		} else {
			logger.Info("models loaded", "count", registry.ModelCount())
		}
		// Start background refresher.
		go registry.StartCron(context.Background(), cfg.ModelRefreshInterval)
	} else {
		logger.Info("no models configured — ML rules disabled")
	}

	// Load and compile rules from YAML.
	rules, err := yaml.CompileRules(cfg.RulesFile, luaExecutor, registry)
	if err != nil {
		logger.Error("failed to load rules", "rules_file", cfg.RulesFile, "err", err)
		os.Exit(1)
	}
	if len(rules) == 0 {
		logger.Warn("no rules loaded — analyzer is a no-op")
	}

	logger.Info("rules loaded", "count", len(rules))

	// Connect to TimescaleDB for reading metrics.
	reader, err := postgres.NewReader(context.Background(), cfg.DBDSN, logger)
	if err != nil {
		logger.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	defer reader.Close()

	// Create Kafka producer for anomaly events.
	brokers := strings.Split(cfg.KafkaBrokers, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	producer := broker.NewProducer(brokers, logger)
	defer producer.Close()

	// Create the analyzer service with polling.
	svc := core.NewAnalyzerService(
		reader,
		core.NewRuleEngine(rules, logger, registry),
		producer,
		cfg.AnalyzerID,
		cfg.BatchSize,
		logger,
	)

	// Simple HTTP server for healthz.
	mux := stdlibhttp.NewServeMux()
	mux.HandleFunc("/healthz", func(w stdlibhttp.ResponseWriter, _ *stdlibhttp.Request) {
		w.WriteHeader(stdlibhttp.StatusOK)
	})
	httpServer := &stdlibhttp.Server{
		Addr:         ":" + cfg.HTTPPort,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start HTTP server in background.
	go func() {
		logger.Info("analyzer http listening", "port", cfg.HTTPPort)
		if err := httpServer.ListenAndServe(); err != nil && err != stdlibhttp.ErrServerClosed {
			logger.Error("http server error", "err", err)
		}
	}()

	// Start the polling loop.
	go svc.Run(ctx, cfg.PollInterval)

	logger.Info("analyzer started", "id", cfg.AnalyzerID, "poll_interval", cfg.PollInterval, "batch_size", cfg.BatchSize)

	<-ctx.Done()
	logger.Info("shutting down analyzer")

	shutdownCtx, shutdownStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownStop()

	if registry != nil {
		registry.Close()
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
	logger.Info("analyzer stopped")
}
