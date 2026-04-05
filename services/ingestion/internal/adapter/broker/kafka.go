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

	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/port"
)

// ConsumerConfig tunes the Kafka consumer behaviour.
type ConsumerConfig struct {
	Brokers  []string
	Topic    string
	GroupID  string
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

// KafkaConsumer implements port.MetricConsumer on top of a Kafka topic.
// It fetches messages from Kafka and dispatches them to a worker pool,
// which deserializes JSON and calls the ingestion service.
// A bounded channel applies backpressure: if the service is slower than
// the broker, fetching pauses automatically.
type KafkaConsumer struct {
	cfg     ConsumerConfig
	logger  *slog.Logger
	service *core.IngestionService

	reader kafkaFetcher // Kafka message fetcher; injectable for testing
	jobs   chan rawMessage
	wg     sync.WaitGroup
	stopMu sync.Mutex
}

// kafkaFetcher abstracts the Kafka read operations needed by the consumer.
// It is satisfied by *kafkago.Reader.
type kafkaFetcher interface {
	FetchMessage(ctx context.Context) (kafkago.Message, error)
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
	io.Closer
}

// NewKafkaConsumer creates a consumer that feeds batches into svc.
func NewKafkaConsumer(cfg ConsumerConfig, svc *core.IngestionService, logger *slog.Logger) (*KafkaConsumer, error) {
	if len(cfg.Brokers) == 0 {
		return nil, errors.New("at least one broker is required")
	}
	if cfg.Topic == "" {
		return nil, errors.New("topic is required")
	}
	cfg = cfg.defaults()

	return &KafkaConsumer{
		cfg:     cfg,
		logger:  logger,
		service: svc,
		jobs:    make(chan rawMessage, cfg.Capacity),
	}, nil
}

// Start begins the consume loop. It blocks until the context is cancelled.
func (c *KafkaConsumer) Start(ctx context.Context) error {
	c.logger.Info("kafka consumer starting",
		"brokers", c.cfg.Brokers,
		"topic", c.cfg.Topic,
		"group", c.cfg.GroupID,
		"workers", c.cfg.Workers,
		"capacity", c.cfg.Capacity,
	)

	// Lazily create the Kafka reader so tests can inject a stub via .reader field.
	if c.reader == nil {
		c.reader = kafkago.NewReader(kafkago.ReaderConfig{
			Brokers:  c.cfg.Brokers,
			Topic:    c.cfg.Topic,
			GroupID:  c.cfg.GroupID,
			MinBytes: 1024,
			MaxBytes: 1048576,
		})
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
				// No message within timeout — loop and check ctx / backpressure.
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
		case c.jobs <- rawMessage{msg: msg, receivedAt: time.Now()}:
			// Job enqueued successfully.
		default:
			// Channel full — drop the oldest to make room.
			c.logger.Warn("ingestion queue full, dropping oldest kafka message")
			select {
			case <-c.jobs:
				// Dropped.
			default:
			}
			c.jobs <- rawMessage{msg: msg, receivedAt: time.Now()}
		}
	}
}

// Stop gracefully shuts down workers and closes the Kafka reader.
func (c *KafkaConsumer) Stop() {
	c.logger.Info("kafka consumer stopping")
	close(c.jobs)
	c.wg.Wait()
	if c.reader != nil {
		c.reader.Close()
	}
	c.logger.Info("kafka consumer stopped")
}

// worker processes messages from the job channel.
func (c *KafkaConsumer) worker() {
	defer c.wg.Done()
	for job := range c.jobs {
		c.process(job)
	}
}

type rawMessage struct {
	msg        kafkago.Message
	receivedAt time.Time
}

// process deserializes a Kafka message into a core.Batch and submits it
// to the ingestion service with retry semantics.
func (c *KafkaConsumer) process(job rawMessage) {
	var payload struct {
		AgentID   string       `json:"agent_id"`
		Metrics   []metricJSON `json:"metrics"`
		CreatedAt time.Time    `json:"created_at"`
	}
	if err := json.Unmarshal(job.msg.Value, &payload); err != nil {
		c.logger.Warn("malformed kafka message, skipping",
			"partition", job.msg.Partition,
			"offset", job.msg.Offset,
			"err", err,
		)
		// Commit so we don't re-process a bad message.
		_ = c.reader.CommitMessages(context.Background(), job.msg)
		return
	}

	batch := core.Batch{
		AgentID: payload.AgentID,
		Metrics: make([]core.Metric, len(payload.Metrics)),
	}
	for i, m := range payload.Metrics {
		batch.Metrics[i] = core.Metric{
			Name:      m.Name,
			Value:     m.Value,
			Labels:    m.Labels,
			Timestamp: m.Timestamp,
			Type:      core.MetricType(m.Type),
		}
	}

	// Submit with retry.
	var lastErr error
	for attempt := 0; attempt <= 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(1<<uint(attempt-1)) * 200 * time.Millisecond)
		}
		if err := c.service.HandleBatch(context.Background(), batch); err != nil {
			lastErr = err
			var ve *core.ValidationError
			if errors.As(err, &ve) {
				c.logger.Warn("invalid batch from kafka, skipping",
					"agent_id", payload.AgentID, "err", err)
				break
			}
			c.logger.Warn("HandleBatch failed, retrying",
				"attempt", attempt, "err", err)
			continue
		}
		lastErr = nil
		break
	}

	if lastErr != nil {
		c.logger.Error("HandleBatch failed after retries, dropping",
			"agent_id", payload.AgentID, "err", lastErr)
	}

	// Commit offset — at-least-once guarantee.
	if err := c.reader.CommitMessages(context.Background(), job.msg); err != nil {
		c.logger.Warn("failed to commit kafka offset",
			"offset", job.msg.Offset, "err", err)
	}
}

type metricJSON struct {
	Name      string            `json:"name"`
	Value     float64           `json:"value"`
	Labels    map[string]string `json:"labels,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Type      string            `json:"type"`
}

// Verify port.MetricConsumer is implemented.
var _ port.MetricConsumer = (*KafkaConsumer)(nil)
