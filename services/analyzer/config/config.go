package config

import (
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	HTTPPort      string
	RulesFile     string
	NotifierURL   string
	NotifyTimeout int // seconds
	LogLevel      string
	// Storage
	DBDSN string
	// Analyzer settings
	AnalyzerID   string
	PollInterval time.Duration
	BatchSize    int
}

func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:      envconfig.Get("HTTP_PORT", "8081"),
		RulesFile:     envconfig.Get("RULES_FILE", "rules.yaml"),
		NotifierURL:   envconfig.Get("NOTIFIER_URL", "http://localhost:8082/api/v1/notifications"),
		NotifyTimeout: envconfig.GetInt("NOTIFY_TIMEOUT_SEC", 5),
		LogLevel:      envconfig.Get("LOG_LEVEL", "info"),
		DBDSN:         envconfig.Get("DB_DSN", "postgres://postgres:secret@timescaledb:5432/anomaly?sslmode=disable"),
		AnalyzerID:    envconfig.Get("ANALYZER_ID", "analyzer-1"),
		PollInterval:  envconfig.GetDurationSec("ANALYZER_POLL_INTERVAL_SEC", 10),
		BatchSize:     envconfig.GetInt("ANALYZER_BATCH_SIZE", 100),
	}

	if cfg.RulesFile == "" {
		return Config{}, fmt.Errorf("RULES_FILE must not be empty")
	}
	if cfg.AnalyzerID == "" {
		return Config{}, fmt.Errorf("ANALYZER_ID must not be empty")
	}
	return cfg, nil
}
