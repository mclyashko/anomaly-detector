package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	HTTPPort            string
	DBDSN               string
	LogLevel            string
	KafkaBrokers        string
	KafkaAnomaliesTopic string
}

// Load reads environment variables and returns the notifier service configuration.
// PostgreSQL DSN for incident storage, Kafka brokers for consuming anomalies.
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:            envconfig.Get("HTTP_PORT", "8082"),
		DBDSN:               envconfig.Get("DB_DSN", ""),
		LogLevel:            envconfig.Get("LOG_LEVEL", "info"),
		KafkaBrokers:        envconfig.Get("KAFKA_BROKERS", "kafka:9092"),
		KafkaAnomaliesTopic: envconfig.Get("KAFKA_ANOMALIES_TOPIC", "anomalies"),
	}

	if cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("DB_DSN must not be empty")
	}

	return cfg, nil
}
