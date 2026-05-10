package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	// Broker (Kafka) consumer
	EnableBroker   bool
	BrokerBrokers  string // comma-separated broker addresses
	BrokerTopic    string
	BrokerGroupID  string
	BrokerWorkers  int
	BrokerCapacity int
	// Storage
	DBDSN       string
	StorageMode string // "memory" or "db"
	LogLevel    string
}

// Load reads environment variables and returns the ingestion service configuration.
// Supports two modes: memory (development) and postgres (production).
// Also configures the Kafka consumer if ENABLE_BROKER=true.
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		EnableBroker:   envconfig.Get("ENABLE_BROKER", "false") == "true",
		BrokerBrokers:  envconfig.Get("BROKER_BROKERS", "localhost:9092"), // comma-separated
		BrokerTopic:    envconfig.Get("BROKER_TOPIC", "metrics"),
		BrokerGroupID:  envconfig.Get("BROKER_GROUP_ID", "ingestion-group"),
		BrokerWorkers:  envconfig.GetInt("BROKER_WORKERS", 8),
		BrokerCapacity: envconfig.GetInt("BROKER_CAPACITY", 256),
		DBDSN:          envconfig.Get("DB_DSN", ""),
		StorageMode:    envconfig.Get("STORAGE_MODE", "db"),
		LogLevel:       envconfig.Get("LOG_LEVEL", "info"),
	}

	if cfg.StorageMode != "memory" && cfg.StorageMode != "db" {
		return Config{}, fmt.Errorf("STORAGE_MODE must be 'memory' or 'db', got %q", cfg.StorageMode)
	}
	if cfg.StorageMode == "db" && cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("DB_DSN must not be empty when STORAGE_MODE=db")
	}
	return cfg, nil
}
