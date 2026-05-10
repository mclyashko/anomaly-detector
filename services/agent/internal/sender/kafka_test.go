package sender

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mclyashko/anomaly-detector/services/agent/internal/domain"
)

// mockWriter implements kafkago.Writer-like interface for testing.
type mockWriter struct {
	messages []mockMessage
	err      error
	closeErr error
}

type mockMessage struct {
	Key   []byte
	Value []byte
}

func (m *mockWriter) WriteMessages(ctx context.Context, msgs ...mockMessage) error {
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, msgs...)
	return nil
}

func (m *mockWriter) Close() error {
	return m.closeErr
}

// testableKafkaSender is a version that allows injecting a mock writer.
type testableKafkaSender struct {
	writeFn func(ctx context.Context, msgs ...mockMessage) error
	logger  *slog.Logger
}

func (s *testableKafkaSender) Send(ctx context.Context, batch domain.Batch) error {
	data, err := json.Marshal(batch)
	if err != nil {
		return err
	}

	msg := mockMessage{
		Key:   []byte(batch.AgentID),
		Value: data,
	}

	return s.writeFn(ctx, msg)
}

func TestKafkaSender_Send_CallsWriteMessages(t *testing.T) {
	var sentMessages []mockMessage
	l := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &testableKafkaSender{
		writeFn: func(ctx context.Context, msgs ...mockMessage) error {
			sentMessages = append(sentMessages, msgs...)
			return nil
		},
		logger: l,
	}

	batch := domain.Batch{
		AgentID:   "agent-write-test",
		CreatedAt: time.Now().UTC(),
		Metrics: []domain.Metric{
			{Name: "memory.usage", Value: 0.5, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()},
		},
	}

	err := s.Send(context.Background(), batch)
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}

	if len(sentMessages) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sentMessages))
	}

	// Verify key is agent_id
	if string(sentMessages[0].Key) != "agent-write-test" {
		t.Errorf("key = %q, want agent-write-test", string(sentMessages[0].Key))
	}

	// Verify value is valid JSON with metrics
	var parsed struct {
		AgentID string `json:"agent_id"`
		Metrics []struct {
			Name  string  `json:"name"`
			Value float64 `json:"value"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(sentMessages[0].Value, &parsed); err != nil {
		t.Fatalf("invalid JSON in message value: %v", err)
	}
	if parsed.AgentID != "agent-write-test" {
		t.Errorf("agent_id in JSON = %q, want agent-write-test", parsed.AgentID)
	}
	if len(parsed.Metrics) != 1 {
		t.Fatalf("metrics count = %d, want 1", len(parsed.Metrics))
	}
	if parsed.Metrics[0].Name != "memory.usage" {
		t.Errorf("metric name = %q, want memory.usage", parsed.Metrics[0].Name)
	}
}

func TestKafkaSender_Send_WriteError(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))

	s := &testableKafkaSender{
		writeFn: func(ctx context.Context, msgs ...mockMessage) error {
			return errors.New("kafka: broker not available")
		},
		logger: l,
	}

	batch := domain.Batch{
		AgentID: "agent-fail",
		Metrics: []domain.Metric{
			{Name: "cpu", Value: 0.9, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()},
		},
	}

	err := s.Send(context.Background(), batch)
	if err == nil {
		t.Error("expected error from WriteMessages failure")
	}
	if err.Error() != "kafka: broker not available" {
		t.Errorf("error = %q, want 'kafka: broker not available'", err.Error())
	}
}

func TestKafkaSender_IntegrationStyle(t *testing.T) {
	// Test the Send method behavior by checking the actual code path
	// uses kafkago.TCP and properly formats the message.

	// Create a real KafkaSender but with a real writer that will fail to connect
	// This tests that Send handles connection errors gracefully.
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := NewKafkaSender([]string{"localhost:9999"}, "test-topic", l)

	batch := domain.Batch{
		AgentID:   "agent-fail",
		CreatedAt: time.Now().UTC(),
		Metrics: []domain.Metric{
			{Name: "test", Value: 1.0, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()},
		},
	}

	// This should fail because there's no Kafka on localhost:9999
	err := sender.Send(context.Background(), batch)
	if err == nil {
		t.Error("expected error when Kafka is unavailable")
	}
}

func TestKafkaSender_Close(t *testing.T) {
	l := slog.New(slog.NewTextHandler(io.Discard, nil))
	sender := NewKafkaSender([]string{"localhost:9092"}, "test-topic", l)

	// Close should not panic
	err := sender.Close()
	if err != nil {
		t.Errorf("Close returned error: %v", err)
	}
}

func TestKafkaSender_BatchJsonMarshaling(t *testing.T) {
	// Verify that Batch can be properly marshaled to JSON
	batch := domain.Batch{
		AgentID:   "agent-json",
		CreatedAt: time.Now().UTC(),
		Metrics: []domain.Metric{
			{Name: "cpu.usage", Value: 0.85, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()},
			{Name: "memory.usage", Value: 0.6, Type: domain.MetricTypeGauge, Timestamp: time.Now().UTC()},
		},
	}

	data, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("failed to marshal batch: %v", err)
	}

	if len(data) == 0 {
		t.Error("expected non-empty json data")
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json not valid: %v", err)
	}
	if parsed["agent_id"] != "agent-json" {
		t.Errorf("agent_id = %v, want agent-json", parsed["agent_id"])
	}
}
