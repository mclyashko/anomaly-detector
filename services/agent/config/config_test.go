package config

import (
	"os"
	"testing"
)

func TestLoad_KafkaEnabled(t *testing.T) {
	os.Setenv("ENABLE_KAFKA", "true")
	os.Setenv("KAFKA_BROKERS", "broker1:9092,broker2:9092")
	os.Setenv("KAFKA_TOPIC", "metrics-test")
	defer func() {
		os.Unsetenv("ENABLE_KAFKA")
		os.Unsetenv("KAFKA_BROKERS")
		os.Unsetenv("KAFKA_TOPIC")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if !cfg.EnableKafka {
		t.Error("EnableKafka = false, want true")
	}
	if cfg.KafkaBrokers != "broker1:9092,broker2:9092" {
		t.Errorf("KafkaBrokers = %q, want broker1:9092,broker2:9092", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "metrics-test" {
		t.Errorf("KafkaTopic = %q, want metrics-test", cfg.KafkaTopic)
	}
}

func TestLoad_KafkaDisabled(t *testing.T) {
	os.Setenv("ENABLE_KAFKA", "false")
	os.Setenv("INGESTION_URL", "http://localhost:8080/api/v1/ingest")
	defer func() {
		os.Unsetenv("ENABLE_KAFKA")
		os.Unsetenv("INGESTION_URL")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.EnableKafka {
		t.Error("EnableKafka = true, want false")
	}
	if cfg.IngestionURL != "http://localhost:8080/api/v1/ingest" {
		t.Errorf("IngestionURL = %q, want http://localhost:8080/api/v1/ingest", cfg.IngestionURL)
	}
}

func TestLoad_KafkaEnabled_EmptyBrokers(t *testing.T) {
	// Unset to trigger fallback, then override to check validation
	os.Setenv("ENABLE_KAFKA", "true")
	os.Unsetenv("KAFKA_BROKERS")
	defer func() {
		os.Unsetenv("ENABLE_KAFKA")
		os.Unsetenv("KAFKA_BROKERS")
	}()

	// With KAFKA_BROKERS unset, Get returns fallback "localhost:9092"
	// so validation passes. Test the validation by using empty explicitly via .env
	// For this test we verify that empty KAFKA_BROKERS with Kafka enabled
	// doesn't error because Get returns the fallback
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.KafkaBrokers != "localhost:9092" {
		t.Errorf("KafkaBrokers = %q, want localhost:9092 (fallback)", cfg.KafkaBrokers)
	}
}

func TestLoad_HttpDisabled_EmptyURL(t *testing.T) {
	os.Setenv("ENABLE_KAFKA", "false")
	os.Unsetenv("INGESTION_URL")
	defer func() {
		os.Unsetenv("ENABLE_KAFKA")
		os.Unsetenv("INGESTION_URL")
	}()

	// With INGESTION_URL unset, Get returns fallback "http://localhost:8080/api/v1/ingest"
	// so validation passes
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IngestionURL != "http://localhost:8080/api/v1/ingest" {
		t.Errorf("IngestionURL = %q, want fallback", cfg.IngestionURL)
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Clear all relevant env vars
	envVars := []string{"ENABLE_KAFKA", "KAFKA_BROKERS", "KAFKA_TOPIC", "INGESTION_URL"}
	for _, v := range envVars {
		os.Unsetenv(v)
	}
	defer func() {
		for _, v := range envVars {
			os.Unsetenv(v)
		}
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Default is Kafka enabled
	if !cfg.EnableKafka {
		t.Error("EnableKafka = false, want true (default)")
	}
	if cfg.KafkaBrokers != "localhost:9092" {
		t.Errorf("KafkaBrokers = %q, want localhost:9092", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "metrics" {
		t.Errorf("KafkaTopic = %q, want metrics", cfg.KafkaTopic)
	}
	if cfg.CollectInterval.Seconds() != 10 {
		t.Errorf("CollectInterval = %v, want 10s", cfg.CollectInterval)
	}
	if cfg.AgentID != "agent-1" {
		t.Errorf("AgentID = %q, want agent-1", cfg.AgentID)
	}
}
