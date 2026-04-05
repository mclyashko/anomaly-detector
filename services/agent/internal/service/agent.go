package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
	"github.com/mclyashko/anomaly-detector/services/agent/internal/port"
)

type AgentService struct {
	agentID    string
	interval   time.Duration
	collectors []port.Collector
	sender     port.Sender
	logger     *slog.Logger
}

func NewAgentService(
	agentID string,
	interval time.Duration,
	collectors []port.Collector,
	sender port.Sender,
	logger *slog.Logger,
) *AgentService {
	return &AgentService{
		agentID:    agentID,
		interval:   interval,
		collectors: collectors,
		sender:     sender,
		logger:     logger,
	}
}

// Start runs the collect-and-send loop until ctx is cancelled.
func (a *AgentService) Start(ctx context.Context) {
	a.logger.Info("agent started",
		"agent_id", a.agentID,
		"interval", a.interval,
		"collectors", len(a.collectors),
	)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logger.Info("agent stopping")
			return
		case <-ticker.C:
			metrics := a.collect(ctx)
			if len(metrics) == 0 {
				continue
			}
			batch := domain.Batch{
				AgentID:   a.agentID,
				Metrics:   metrics,
				CreatedAt: time.Now().UTC(),
			}
			if err := a.sender.Send(ctx, batch); err != nil {
				a.logger.Error("failed to send batch", "err", err, "metrics", len(metrics))
			} else {
				a.logger.Info("batch sent", "metrics", len(metrics))
			}
		}
	}
}

func (a *AgentService) collect(ctx context.Context) []domain.Metric {
	var all []domain.Metric
	for _, c := range a.collectors {
		metrics, err := c.Collect(ctx)
		if err != nil {
			a.logger.Warn("collector error", "collector", c.Name(), "err", err)
			continue
		}
		a.logger.Debug("collected", "collector", c.Name(), "count", len(metrics))
		all = append(all, metrics...)
	}
	return all
}
