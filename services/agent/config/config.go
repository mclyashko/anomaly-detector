package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	AgentID               string
	EnableKafka           bool
	KafkaBrokers          string // comma-separated broker addresses
	KafkaTopic            string
	IngestionURL          string // used when EnableKafka=false
	FakeServiceMetricsURL string
	CollectInterval       time.Duration
	SendTimeout           time.Duration
	MaxRetries            int
	LogLevel              string
}

// Load читает переменные окружения и возвращает конфигурацию agent-сервиса.
// Agent может отправлять метрики либо через Kafka (EnableKafka=true), либо напрямую
// в ingestion сервис по HTTP (EnableKafka=false).
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		AgentID:               envconfig.Get("AGENT_ID", "agent-1"),
		EnableKafka:           envconfig.Get("ENABLE_KAFKA", "true") == "true",
		KafkaBrokers:          envconfig.Get("KAFKA_BROKERS", "localhost:9092"),
		KafkaTopic:            envconfig.Get("KAFKA_TOPIC", "metrics"),
		IngestionURL:          envconfig.Get("INGESTION_URL", "http://localhost:8080/api/v1/ingest"),
		FakeServiceMetricsURL: envconfig.Get("FAKE_SERVICE_METRICS_URL", "http://localhost:8090/metrics"),
		CollectInterval:       envconfig.GetDurationSec("COLLECT_INTERVAL_SEC", 10),
		SendTimeout:           envconfig.GetDurationSec("SEND_TIMEOUT_SEC", 5),
		MaxRetries:            envconfig.GetInt("MAX_RETRIES", 3),
		LogLevel:              envconfig.Get("LOG_LEVEL", "info"),
	}

	if !cfg.EnableKafka && cfg.IngestionURL == "" {
		return Config{}, fmt.Errorf("INGESTION_URL must not be empty when ENABLE_KAFKA=false")
	}
	if cfg.EnableKafka && cfg.KafkaBrokers == "" {
		return Config{}, fmt.Errorf("KAFKA_BROKERS must not be empty when ENABLE_KAFKA=true")
	}
	if cfg.CollectInterval <= 0 {
		return Config{}, fmt.Errorf("COLLECT_INTERVAL_SEC must be > 0")
	}
	return cfg, nil
}
