package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	// HTTP server
	HTTPPort string
	// Broker (Kafka) consumer
	EnableBroker   bool
	BrokerBrokers  string // comma-separated broker addresses
	BrokerTopic    string
	BrokerGroupID  string
	BrokerWorkers  int
	BrokerCapacity int
	// Storage
	DBDSN       string
	StorageMode string // "memory" or "postgres"
	LogLevel    string
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:       envconfig.Get("HTTP_PORT", "8080"),
		EnableBroker:   envconfig.Get("ENABLE_BROKER", "false") == "true",
		BrokerBrokers:  envconfig.Get("BROKER_BROKERS", "localhost:9092"), // comma-separated
		BrokerTopic:    envconfig.Get("BROKER_TOPIC", "metrics"),
		BrokerGroupID:  envconfig.Get("BROKER_GROUP_ID", "ingestion-group"),
		BrokerWorkers:  envconfig.GetInt("BROKER_WORKERS", 8),
		BrokerCapacity: envconfig.GetInt("BROKER_CAPACITY", 256),
		DBDSN:          envconfig.Get("DB_DSN", "postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable"),
		StorageMode:    envconfig.Get("STORAGE_MODE", "memory"),
		LogLevel:       envconfig.Get("LOG_LEVEL", "info"),
	}

	if cfg.StorageMode != "memory" && cfg.StorageMode != "postgres" {
		return Config{}, fmt.Errorf("STORAGE_MODE must be 'memory' or 'postgres', got %q", cfg.StorageMode)
	}
	if cfg.StorageMode == "postgres" && cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("DB_DSN must not be empty when STORAGE_MODE=postgres")
	}
	return cfg, nil
}
