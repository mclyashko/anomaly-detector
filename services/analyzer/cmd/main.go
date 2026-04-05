package main

import (
	"context"
	stdlibhttp "net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mclyashko/anomaly-detector/services/analyzer/config"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/lua"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/adapter/notifierclient"
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

	// Load and compile rules from YAML.
	rules, err := yaml.CompileRules(cfg.RulesFile, luaExecutor)
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

	// Create notifier client.
	notifier := notifierclient.NewSender(
		cfg.NotifierURL,
		logger,
	)

	// Create the analyzer service with polling.
	svc := core.NewAnalyzerService(
		reader,
		core.NewRuleEngine(rules, logger),
		notifier,
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

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown error", "err", err)
	}
	logger.Info("analyzer stopped")
}
