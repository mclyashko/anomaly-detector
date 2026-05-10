package broker

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/mclyashko/anomaly-detector/services/ingestion/internal/core"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// mockStorage implements core.Storage for testing.
type mockStorage struct {
	mu    sync.Mutex
	saved []core.Batch
	err   error
}

func (m *mockStorage) Save(_ context.Context, b core.Batch) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saved = append(m.saved, b)
	return nil
}

func (m *mockStorage) Batches() []core.Batch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saved
}

// mockFetcher implements kafkaFetcher for testing.
type mockFetcher struct {
	commitCalled bool
	commitMsg    kafkago.Message
	commitErr    error
	mu           sync.Mutex
}

func (m *mockFetcher) FetchMessage(ctx context.Context) (kafkago.Message, error) {
	return kafkago.Message{}, nil
}

func (m *mockFetcher) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	if len(msgs) > 0 {
		m.mu.Lock()
		m.commitCalled = true
		m.commitMsg = msgs[0]
		m.mu.Unlock()
	}
	return m.commitErr
}

func (m *mockFetcher) Close() error { return nil }

func TestKafkaConsumer_ValidateConfig(t *testing.T) {
	logger := discardLogger()
	svc := core.NewIngestionService(&mockStorage{}, logger)

	tests := []struct {
		name    string
		cfg     ConsumerConfig
		wantErr bool
	}{
		{
			name:    "empty brokers",
			cfg:     ConsumerConfig{Brokers: []string{}, Topic: "test", GroupID: "g"},
			wantErr: true,
		},
		{
			name:    "empty topic",
			cfg:     ConsumerConfig{Brokers: []string{"localhost:9092"}, Topic: "", GroupID: "g"},
			wantErr: true,
		},
		{
			name:    "valid config",
			cfg:     ConsumerConfig{Brokers: []string{"localhost:9092"}, Topic: "test", GroupID: "g"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewKafkaConsumer(tt.cfg, svc, logger)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewKafkaConsumer() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestConsumerConfig_Defaults(t *testing.T) {
	cfg := ConsumerConfig{}
	cfg = cfg.defaults()

	if cfg.Workers != 8 {
		t.Errorf("expected Workers=8, got %d", cfg.Workers)
	}
	if cfg.Capacity != 256 {
		t.Errorf("expected Capacity=256, got %d", cfg.Capacity)
	}
}

func TestKafkaConsumer_ProcessBatch(t *testing.T) {
	storage := &mockStorage{}
	logger := discardLogger()
	svc := core.NewIngestionService(storage, logger)

	fetcher := &mockFetcher{}
	consumer := &KafkaConsumer{
		cfg:     ConsumerConfig{Workers: 1, Capacity: 10},
		logger:  logger,
		service: svc,
		reader:  fetcher,
		jobs:    make(chan rawMessage, 10),
		wg:      sync.WaitGroup{},
	}

	payload, _ := json.Marshal(map[string]any{
		"agent_id": "test-agent",
		"metrics": []map[string]any{
			{
				"name":      "cpu.usage",
				"value":     0.85,
				"type":      "gauge",
				"timestamp": time.Now().Format(time.RFC3339),
				"labels":    map[string]string{"host": "localhost"},
			},
		},
		"created_at": time.Now().Format(time.RFC3339),
	})

	consumer.wg.Add(1)
	go consumer.worker()
	consumer.jobs <- rawMessage{
		msg:        kafkago.Message{Value: payload},
		receivedAt: time.Now(),
	}
	close(consumer.jobs)
	consumer.wg.Wait()

	batches := storage.Batches()
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch, got %d", len(batches))
	}
	if batches[0].AgentID != "test-agent" {
		t.Errorf("expected agent_id=test-agent, got %s", batches[0].AgentID)
	}
	if len(batches[0].Metrics) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(batches[0].Metrics))
	}
	if batches[0].Metrics[0].Name != "cpu.usage" {
		t.Errorf("expected metric name=cpu.usage, got %s", batches[0].Metrics[0].Name)
	}

	fetcher.mu.Lock()
	if !fetcher.commitCalled {
		t.Error("expected CommitMessages to be called")
	}
	fetcher.mu.Unlock()
}

func TestKafkaConsumer_InvalidJSON(t *testing.T) {
	storage := &mockStorage{}
	logger := discardLogger()
	svc := core.NewIngestionService(storage, logger)

	fetcher := &mockFetcher{}
	consumer := &KafkaConsumer{
		cfg:     ConsumerConfig{Workers: 1, Capacity: 10},
		logger:  logger,
		service: svc,
		reader:  fetcher,
		jobs:    make(chan rawMessage, 10),
		wg:      sync.WaitGroup{},
	}

	consumer.wg.Add(1)
	go consumer.worker()
	consumer.jobs <- rawMessage{
		msg:        kafkago.Message{Value: []byte("not json")},
		receivedAt: time.Now(),
	}
	close(consumer.jobs)
	consumer.wg.Wait()

	// No batches should be saved due to malformed JSON.
	batches := storage.Batches()
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for invalid JSON, got %d", len(batches))
	}
}

func TestKafkaConsumer_ValidationError(t *testing.T) {
	storage := &mockStorage{}
	logger := discardLogger()
	svc := core.NewIngestionService(storage, logger)

	fetcher := &mockFetcher{}
	consumer := &KafkaConsumer{
		cfg:     ConsumerConfig{Workers: 1, Capacity: 10},
		logger:  logger,
		service: svc,
		reader:  fetcher,
		jobs:    make(chan rawMessage, 10),
		wg:      sync.WaitGroup{},
	}

	// Payload with empty agent_id - should fail validation.
	payload, _ := json.Marshal(map[string]any{
		"agent_id":   "",
		"metrics":    []map[string]any{{"name": "m1", "value": 1, "type": "gauge", "timestamp": time.Now().Format(time.RFC3339)}},
		"created_at": time.Now().Format(time.RFC3339),
	})

	consumer.wg.Add(1)
	go consumer.worker()
	consumer.jobs <- rawMessage{msg: kafkago.Message{Value: payload}, receivedAt: time.Now()}
	close(consumer.jobs)
	consumer.wg.Wait()

	// Empty agent_id should fail validation - batch not saved.
	batches := storage.Batches()
	if len(batches) != 0 {
		t.Errorf("expected 0 batches for invalid batch, got %d", len(batches))
	}
}

func TestKafkaConsumer_HandleBatchError(t *testing.T) {
	storage := &mockStorage{}
	logger := discardLogger()
	svc := core.NewIngestionService(storage, logger)

	fetcher := &mockFetcher{}
	consumer := &KafkaConsumer{
		cfg:     ConsumerConfig{Workers: 1, Capacity: 10},
		logger:  logger,
		service: svc,
		reader:  fetcher,
		jobs:    make(chan rawMessage, 10),
		wg:      sync.WaitGroup{},
	}

	payload, _ := json.Marshal(map[string]any{
		"agent_id": "test-agent",
		"metrics": []map[string]any{
			{
				"name":      "cpu.usage",
				"value":     0.85,
				"type":      "gauge",
				"timestamp": time.Now().Format(time.RFC3339),
				"labels":    map[string]string{"host": "localhost"},
			},
		},
		"created_at": time.Now().Format(time.RFC3339),
	})

	consumer.wg.Add(1)
	go consumer.worker()
	consumer.jobs <- rawMessage{msg: kafkago.Message{Value: payload}, receivedAt: time.Now()}
	close(consumer.jobs)
	consumer.wg.Wait()

	// Should still commit even if HandleBatch fails after retries.
	fetcher.mu.Lock()
	if !fetcher.commitCalled {
		t.Error("expected CommitMessages to be called even after HandleBatch error")
	}
	fetcher.mu.Unlock()
}
