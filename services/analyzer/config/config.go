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
	KafkaBrokers string
	// Analyzer settings
	AnalyzerID   string
	PollInterval time.Duration
	BatchSize    int
	// MinIO / Model storage
	MinIOEndpoint   string
	MinIOAccessKey  string
	MinIOSecretKey  string
	MinIOBucket     string
	MinIOUseSSL     bool
	// Model refresh
	ModelRefreshInterval time.Duration
	ModelsConfigFile    string
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:    envconfig.Get("HTTP_PORT", "8081"),
		RulesFile:   envconfig.Get("RULES_FILE", "rules.yaml"),
		LogLevel:    envconfig.Get("LOG_LEVEL", "info"),
		DBDSN:       envconfig.Get("DB_DSN", "postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable"),
		KafkaBrokers: envconfig.Get("KAFKA_BROKERS", "kafka:9092"),
		AnalyzerID:  envconfig.Get("ANALYZER_ID", "analyzer-1"),
		PollInterval: envconfig.GetDurationSec("ANALYZER_POLL_INTERVAL_SEC", 10),
		BatchSize:    envconfig.GetInt("ANALYZER_BATCH_SIZE", 100),
		// MinIO
		MinIOEndpoint:  envconfig.Get("MINIO_ENDPOINT", "minio:9000"),
		MinIOAccessKey: envconfig.Get("MINIO_ACCESS_KEY", "minioadmin"),
		MinIOSecretKey: envconfig.Get("MINIO_SECRET_KEY", "minioadmin"),
		MinIOBucket:    envconfig.Get("MINIO_BUCKET", "models"),
		MinIOUseSSL:    envconfig.GetBool("MINIO_USE_SSL"),
		// Model refresh
		ModelRefreshInterval: envconfig.GetDurationSec("MODEL_REFRESH_INTERVAL_SEC", 300),
		ModelsConfigFile:    envconfig.Get("MODELS_CONFIG_FILE", "models.yaml"),
	}

	if cfg.RulesFile == "" {
		return Config{}, fmt.Errorf("RULES_FILE must not be empty")
	}
	if cfg.AnalyzerID == "" {
		return Config{}, fmt.Errorf("ANALYZER_ID must not be empty")
	}
	return cfg, nil
}
