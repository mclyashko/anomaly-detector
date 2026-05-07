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
	anomaliesTopic  = "anomalies"
	consumerGroup   = "notifier-group"
	shutdownTimeout = 10 * time.Second
)

// Consumer читает аномальные события из Kafka топика "anomalies"
// и передаёт их в NotifierService для создания/обновления инцидентов.
//
// Архитектура:
// - Основной горутин читает сообщения из Kafka с помощью Reader API
// - Каждое сообщение кладётся в буферизованный канал jobs (емкость = Capacity)
// - Workers (параллельно) вычитывают из канала и обрабатывают каждое сообщение
// - Коммит offset-а происходит только после успешной обработки HandleAnomaly
//
// Такая схема обеспечивает:
// - Параллельную обработку нескольких сообщений (Workers goroutines)
// - Backpressure через буферизованный канал
// - At-least-once доставку: если обработка упала, сообщение не коммитится и будет переобработано
type Consumer struct {
	cfg    ConsumerConfig
	logger *slog.Logger
	svc    *core.NotifierService

	reader kafkaFetcher // Kafka message fetcher; injectable for testing
	jobs   chan kafkago.Message
	wg     sync.WaitGroup
	stopMu sync.Mutex
	stopped bool
}

type ConsumerConfig struct {
	Brokers  []string
	Workers  int // parallel message handlers; default 8
	Capacity int // bounded channel capacity (backpressure); default 256
}

func (c ConsumerConfig) defaults() ConsumerConfig {
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.Capacity <= 0 {
		c.Capacity = 256
	}
	return c
}

func makeKafkaFetcher(brokers []string) *kafkago.Reader {
	return kafkago.NewReader(kafkago.ReaderConfig{
		Brokers:  brokers,
		Topic:    anomaliesTopic,
		GroupID:  consumerGroup,
		MinBytes: 1024,
		MaxBytes: 1048576,
	})
}

// NewConsumer создаёт Consumer с заданной конфигурацией.
// Broker-и address книги kafka, svc — NotifierService для обработки событий,
// logger — для логирования. Возвращает готовый к запуску Consumer.
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
func (c *Consumer) Consume(ctx context.Context) error {
	c.logger.Info("kafka consumer starting",
		"brokers", c.cfg.Brokers,
		"topic",   anomaliesTopic,
		"group",   consumerGroup,
		"workers", c.cfg.Workers,
	)

	// Lazily create the Kafka reader so tests can inject a stub via .reader field.
	if c.reader == nil {
		c.reader = makeKafkaFetcher(c.cfg.Brokers)
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

// process обрабатывает одно сообщение из Kafka.
// Десериализует AnomalyPayload, передаёт в NotifierService, затем коммитит offset.
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

	_, err := c.svc.HandleAnomaly(context.Background(), &payload)
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

var _ core.EventConsumer = (*Consumer)(nil)
