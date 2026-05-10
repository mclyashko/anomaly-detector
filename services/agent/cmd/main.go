package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mclyashko/anomaly-detector/services/agent/config"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/collector"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/port"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/sender"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/service"
	"github.com/mclyashko/anomaly-detector/shared/pkg/buildlog"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config error", "err", err)
		os.Exit(1)
	}

	logger := buildlog.New(cfg.LogLevel)

	collectors := []port.Collector{
		collector.NewSystemCollector(),
		collector.NewHTTPScrapeCollector("fake-service", cfg.FakeServiceMetricsURL),
	}

	var sndr port.Sender
	brokers := strings.Split(cfg.KafkaBrokers, ",")
	for i := range brokers {
		brokers[i] = strings.TrimSpace(brokers[i])
	}
	sndr = sender.NewKafkaSender(brokers, cfg.KafkaTopic, logger)
	logger.Info("using kafka sender", "brokers", brokers, "topic", cfg.KafkaTopic)

	agent := service.NewAgentService(cfg.AgentID, cfg.CollectInterval, collectors, sndr, logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	agent.Start(ctx)
	logger.Info("agent exited cleanly")
}
