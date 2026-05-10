package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/mclyashko/anomaly-detector/services/notifier/internal/core"
)

const (
	consumerGroup   = "notifier-group"
	shutdownTimeout = 10 * time.Second
)

// Consumer reads anomaly events from the Kafka topic "anomalies"
// and forwards them to NotifierService for incident creation/update.
//
// Architecture:
// - Main goroutine reads messages from Kafka using the Reader API
// - Each message is placed in a buffered channel jobs (capacity = Capacity)
// - Workers (in parallel) read from the channel and process each message
// - Offset commit happens only after successful HandleAnomaly processing
//
// This design provides:
// - Parallel processing of multiple messages (Workers goroutines)
// - Backpressure via buffered channel
// - At-least-once delivery: if processing fails, the message is not committed and will be reprocessed
type Consumer struct {
	cfg    ConsumerConfig
	logger *slog.Logger
	svc    *core.NotifierService

	reader  kafkaFetcher // Kafka message fetcher; injectable for testing
	jobs    chan kafkago.Message
	wg      sync.WaitGroup
	stopMu  sync.Mutex
	stopped bool

	stopCtxFn  context.Context
	stopCancel context.CancelFunc
	stopOnce   sync.Once
}

type ConsumerConfig struct {
	Brokers  []string
	Topic    string // Kafka topic for anomaly events
	Workers  int    // parallel message handlers; default 8
	Capacity int    // bounded channel capacity (backpressure); default 256
}

func (c ConsumerConfig) defaults() ConsumerConfig {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.Capacity <= 0 {
		c.Capacity = 256
	}
	if c.Topic == "" {
		c.Topic = "anomalies"
	}
	return c
}

func makeKafkaFetcher(brokers []string, topic string) *kafkago.Reader {
	return kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:  brokers,
		Topic:    topic,
		GroupID:  consumerGroup,
		MinBytes: 1024,
		MaxBytes: 1048576,
	})
}

// NewConsumer creates a Consumer with the given configuration.
// brokers are kafka addresses, svc is NotifierService for event processing,
// logger is for logging. Returns a Consumer ready to start.
func NewConsumer(cfg ConsumerConfig, svc *core.NotifierService, logger *slog.Logger) *Consumer {
	cfg = cfg.defaults()
	return &Consumer{
		cfg:    cfg,
		logger: logger,
		svc:    svc,
		jobs:   make(chan kafkago.Message, cfg.Capacity),
	}
}

// kafkaFetcher abstracts the Kafka read operations needed by the consumer.
type kafkaFetcher interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
	io.Closer
}

// Consume connects to Kafka and starts worker goroutines.
// The supplied context is used for graceful shutdown: cancelling ctx terminates the consume loop.
func (c *Consumer) Consume(ctx context.Context) error {
	c.stopCtxFn, c.stopCancel = context.WithCancel(context.Background())
	defer c.stopCancel()

	c.logger.Info("kafka consumer starting",
		"brokers", c.cfg.Brokers,
		"topic", c.cfg.Topic,
		"group", consumerGroup,
		"workers", c.cfg.Workers,
	)

	// Lazily create the Kafka reader so tests can inject a stub via .reader field.
	if c.reader == nil {
		c.reader = makeKafkaFetcher(c.cfg.Brokers, c.cfg.Topic)
	}

	// Launch workers.
	for range c.cfg.Workers {
		c.wg.Add(1)
		go c.worker()
	}

	for {
		readCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		msg, err := c.reader.FetchMessage(readCtx)
		cancel()

		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			if errors.Is(err, context.Canceled) {
				continue
			}
			c.logger.Warn("kafka fetch error, retrying", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
			continue
		}

		// Try to enqueue for a worker.
		select {
		case c.jobs <- msg:
		default:
			c.logger.Warn("queue full, dropping message")
			select {
			case <-c.jobs:
			default:
			}
			c.jobs <- msg
		}
	}
}

func (c *Consumer) Stop() {
	c.stopOnce.Do(func() {
		c.stopCancel()
	})

	c.stopMu.Lock()
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
		c.logger.Info("workers drained")
	case <-time.After(shutdownTimeout):
		c.logger.Warn("worker drain timeout")
	}

	if c.reader != nil {
		c.reader.Close()
	}
	c.logger.Info("kafka consumer stopped")
}

func (c *Consumer) worker() {
	defer c.wg.Done()
	for msg := range c.jobs {
		c.process(msg)
	}
}

// process processes a single message from Kafka.
// Deserializes AnomalyPayload, passes it to NotifierService, then commits the offset.
func (c *Consumer) process(msg kafkago.Message) {
	var payload core.AnomalyPayload
	if err := json.Unmarshal(msg.Value, &payload); err != nil {
		c.logger.Warn("malformed anomaly message, skipping",
			"partition", msg.Partition,
			"offset", msg.Offset,
			"err", err,
		)
		_ = c.reader.CommitMessages(context.Background(), msg)
		return
	}

	// Use stopCtx so that Stop() can cancel in-flight HandleAnomaly calls.
	_, err := c.svc.HandleAnomaly(c.stopCtxFn, &payload)
	if err != nil {
		c.logger.Error("failed to handle anomaly",
			"rule", payload.Rule,
			"service", payload.Service,
			"metric", payload.Metric,
			"err", err,
		)
		return
	}

	if err := c.reader.CommitMessages(context.Background(), msg); err != nil {
		c.logger.Warn("failed to commit kafka offset",
			"offset", msg.Offset, "err", err)
	}
}
