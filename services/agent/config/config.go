package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	AgentID               string
	KafkaBrokers          string // comma-separated broker addresses
	KafkaTopic            string
	FakeServiceMetricsURL string
	CollectInterval       time.Duration
	MaxRetries            int
	LogLevel              string
}

// Load reads environment variables and returns the agent service configuration.
// Agent can send metrics either via Kafka (EnableKafka=true) or directly
// to the ingestion service over HTTP (EnableKafka=false).
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		AgentID:               envconfig.Get("AGENT_ID", "agent-1"),
		KafkaBrokers:          envconfig.Get("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:            envconfig.Get("KAFKA_TOPIC", "metrics"),
		FakeServiceMetricsURL: envconfig.Get("FAKE_SERVICE_METRICS_URL", "http://localhost:8090/metrics"),
		CollectInterval:       envconfig.GetDurationSec("COLLECT_INTERVAL_SEC", 10),
		MaxRetries:            envconfig.GetInt("MAX_RETRIES", 3),
		LogLevel:              envconfig.Get("LOG_LEVEL", "info"),
	}

	if cfg.KafkaBrokers == "" {
		return Config{}, fmt.Errorf("KAFKA_BROKERS must not be empty")
	}
	if cfg.CollectInterval <= 0 {
		return Config{}, fmt.Errorf("COLLECT_INTERVAL_SEC must be > 0")
	}
	return cfg, nil
}
