package broker

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/core"
	"github.com/mclyashko/anomaly-detector/services/analyzer/internal/port"
)

const (
	anomaliesTopic = "anomalies"
	batchSize      = 100
	batchTimeout   = 5 * time.Millisecond
)

// Producer implements port.EventProducer by writing anomaly events to Kafka.
// Each anomaly is written as a separate message for maximum consumer parallelism.
// At-least-once delivery is guaranteed by Kafka'sacks setting.
type Producer struct {
	writer *kafkago.Writer
	logger *slog.Logger
}

func NewProducer(brokers []string, logger *slog.Logger) *Producer {
	w := &kafkago.Writer{
		Addr:         kafkago.TCP(brokers...),
		Topic:        anomaliesTopic,
		Balancer:     &kafkago.LeastBytes{},
		BatchSize:    batchSize,
		BatchTimeout: batchTimeout,
		RequiredAcks: kafkago.RequireAll,
		Async:        false,
	}
	return &Producer{writer: w, logger: logger}
}

// Produce writes each anomaly as a separate Kafka message.
func (p *Producer) Produce(ctx context.Context, anomalies []*core.Anomaly) error {
	if len(anomalies) == 0 {
		return nil
	}

	msgs := make([]kafkago.Message, 0, len(anomalies))
	for _, a := range anomalies {
		payload, err := json.Marshal(a)
		if err != nil {
			p.logger.Warn("skipping anomaly: marshal failed", "rule", a.Rule, "err", err)
			return err
		}
		msgs = append(msgs, kafkago.Message{
			Key:   []byte(a.Metric), // partition by metric name for ordering
			Value: payload,
		})
	}

	if len(msgs) == 0 {
		return nil
	}

	if err := p.writer.WriteMessages(ctx, msgs...); err != nil {
		return err
	}

	p.logger.Debug("anomalies produced to kafka", "count", len(msgs))
	return nil
}

// Close flushes and closes the writer.
func (p *Producer) Close() error {
	return p.writer.Close()
}

var _ port.EventProducer = (*Producer)(nil)
