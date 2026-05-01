package broker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

const (
	anomaliesTopic    = "anomalies"
	consumerGroup     = "notifier-group"
	workers           = 8
	capacity          = 256
	shutdownTimeout   = 10 * time.Second
)

// Consumer implements core.EventConsumer by reading anomaly events from Kafka
// and forwarding them to the incident service. Idempotency is handled by the
// incident service's deduplication logic (unique constraint on rule|service|metric).
type Consumer struct {
	cfg    ConsumerConfig
	logger *slog.Logger
	svc    *core.NotifierService
	reader kafkaReader

	jobs    chan rawMessage
	wg      sync.WaitGroup
	stopMu  sync.Mutex
	stopped bool
}

type ConsumerConfig struct {
	Brokers []string
}

type rawMessage struct {
	msg kafkago.Message
}

func NewConsumer(cfg ConsumerConfig, svc *core.NotifierService, logger *slog.Logger) *Consumer {
	return &Consumer{
		cfg:    cfg,
		logger: logger,
		svc:    svc,
		jobs:   make(chan rawMessage, capacity),
	}
}

// Consume connects to Kafka and starts worker goroutines.
// It blocks until ctx is cancelled.
func (c *Consumer) Consume(ctx context.Context) error {
	if c.reader == nil {
		c.reader = kafkago.NewReader(kafkago.ReaderConfig{
			Brokers:  c.cfg.Brokers,
			Topic:    anomaliesTopic,
			GroupID:  consumerGroup,
			MinBytes: 1024,
			MaxBytes: 1048576,
		})
	}

	c.logger.Info("kafka consumer starting",
		"brokers", c.cfg.Brokers,
		"topic",   anomaliesTopic,
		"group",   consumerGroup,
		"workers", workers,
	)

	for range workers {
		c.wg.Add(1)
		go c.worker()
	}

	for {
		msg, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			c.logger.Warn("fetch error, retrying", "err", err)
			continue
		}

		select {
		case c.jobs <- rawMessage{msg: msg}:
		default:
			c.logger.Warn("consumer queue full, dropping message")
			_ = c.reader.CommitMessages(ctx, msg)
		}
	}
}

func (c *Consumer) Stop() {
	c.stopMu.Lock()
	if c.stopped {
		c.stopMu.Unlock()
		return
	}
	c.stopped = true
	c.stopMu.Unlock()

	c.logger.Info("kafka consumer stopping")
	close(c.jobs)

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		c.logger.Info("kafka consumer workers drained")
	case <-time.After(shutdownTimeout):
		c.logger.Warn("kafka consumer workers did not drain within timeout")
	}
}

func (c *Consumer) worker() {
	defer c.wg.Done()
	for job := range c.jobs {
		c.process(job)
	}
}

func (c *Consumer) process(job rawMessage) {
	var payload core.AnomalyPayload
	if err := json.Unmarshal(job.msg.Value, &payload); err != nil {
		c.logger.Warn("malformed anomaly message, skipping",
			"partition", job.msg.Partition,
			"offset", job.msg.Offset,
			"err", err,
		)
		// Commit to avoid re-processing bad message.
		_ = c.reader.CommitMessages(context.Background(), job.msg)
		return
	}

	_, err := c.svc.HandleAnomaly(context.Background(), &payload)
	if err != nil {
		c.logger.Error("failed to handle anomaly",
			"rule", payload.Rule,
			"service", payload.Service,
			"metric", payload.Metric,
			"err", err,
		)
		// Do not commit — will be redelivered.
		return
	}

	if err := c.reader.CommitMessages(context.Background(), job.msg); err != nil {
		c.logger.Warn("failed to commit offset",
			"offset", job.msg.Offset,
			"err", err,
		)
	}
}

type kafkaReader interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
}

var _ core.EventConsumer = (*Consumer)(nil)
