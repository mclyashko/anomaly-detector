package sender

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// KafkaSender publishes batches to a Kafka topic.
type KafkaSender struct {
	writer *kafkago.Writer
	logger *slog.Logger
}

// NewKafkaSender creates a sender that writes JSON-encoded batches to Kafka.
func NewKafkaSender(brokers []string, topic string, logger *slog.Logger) *KafkaSender {
	w := &kafkago.Writer{
		Addr:         kafkago.TCP(brokers...),
		Topic:        topic,
		Balancer:     &kafkago.LeastBytes{},
		BatchSize:    1, // send immediately
		BatchTimeout: 10 * time.Millisecond,
		RequiredAcks: kafkago.RequireOne,
	}
	return &KafkaSender{writer: w, logger: logger}
}

// Send encodes the batch as JSON and writes to Kafka.
// Partitioning uses agentID:metricName key — first metric in batch determines partition.
// This ensures all metrics from one agent+metric combination go to the same partition.
func (s *KafkaSender) Send(ctx context.Context, batch domain.Batch) error {
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}

	// Use first metric's name for partitioning key — gives per-agent-per-metric ordering.
	partitionKey := batch.AgentID
	if len(batch.Metrics) > 0 {
		partitionKey = batch.AgentID + ":" + batch.Metrics[0].Name
	}

	msg := kafkago.Message{
		Key:   []byte(partitionKey),
		Value: data,
	}

	if err := s.writer.WriteMessages(ctx, msg); err != nil {
		s.logger.Error("kafka write failed", "agent_id", batch.AgentID, "err", err)
		return err
	}

	s.logger.Debug("batch sent to kafka", "agent_id", batch.AgentID, "metrics", len(batch.Metrics))
	return nil
}

// Close closes the Kafka writer.
func (s *KafkaSender) Close() error {
	return s.writer.Close()
}
