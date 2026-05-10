package config

import (
	"os"
	"testing"
)

func TestLoad_Kafka(t *testing.T) {
	os.Setenv("KAFKA_BROKERS", "broker1:9092,broker2:9092")
	os.Setenv("KAFKA_TOPIC", "metrics-test")
	defer func() {
		os.Unsetenv("KAFKA_BROKERS")
		os.Unsetenv("KAFKA_TOPIC")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.KafkaBrokers != "broker1:9092,broker2:9092" {
		t.Errorf("KafkaBrokers = %q, want broker1:9092,broker2:9092", cfg.KafkaBrokers)
	}
	if cfg.KafkaTopic != "metrics-test" {
		t.Errorf("KafkaTopic = %q, want metrics-test", cfg.KafkaTopic)
	}
}

func TestLoad_Defaults(t *testing.T) {
	envVars := []string{"KAFKA_BROKERS", "KAFKA_TOPIC"}
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
