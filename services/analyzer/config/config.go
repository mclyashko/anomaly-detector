package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	HTTPPort       string
	RulesFile      string
	LogLevel       string
	// Storage
	DBDSN string
	// Kafka
	KafkaBrokers       string
	KafkaAnomaliesTopic string
	// Analyzer settings
	AnalyzerID   string
	PollInterval time.Duration
	BatchSize    int
	// ML Service URL (e.g. "http://training:8085")
	MLServiceURL string
}

// Load reads environment variables and returns the analyzer service configuration.
// Sets up polling interval, batch size, TimescaleDB connection, and ML service URL.
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:            envconfig.Get("HTTP_PORT", "8081"),
		RulesFile:           envconfig.Get("RULES_FILE", "rules.yaml"),
		LogLevel:            envconfig.Get("LOG_LEVEL", "info"),
		DBDSN:               envconfig.Get("DB_DSN", ""),
		KafkaBrokers:        envconfig.Get("KAFKA_BROKERS", "kafka:9092"),
		KafkaAnomaliesTopic: envconfig.Get("KAFKA_ANOMALIES_TOPIC", "anomalies"),
		AnalyzerID:          envconfig.Get("ANALYZER_ID", "analyzer-1"),
		PollInterval:        envconfig.GetDurationSec("ANALYZER_POLL_INTERVAL_SEC", 10),
		BatchSize:           envconfig.GetInt("ANALYZER_BATCH_SIZE", 100),
		MLServiceURL:        envconfig.Get("ML_SERVICE_URL", ""),
	}

	if cfg.RulesFile == "" {
		return Config{}, fmt.Errorf("RULES_FILE must not be empty")
	}
	if cfg.AnalyzerID == "" {
		return Config{}, fmt.Errorf("ANALYZER_ID must not be empty")
	}
	return cfg, nil
}
