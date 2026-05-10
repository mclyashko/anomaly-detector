package config

import (
	"fmt"

	"github.com/joho/godotenv"
	"github.com/mclyashko/anomaly-detector/shared/pkg/envconfig"
)

type Config struct {
	Port        string
	WorkerCount int
	LogLevel    string
}

// Load reads environment variables and returns the fake-service configuration.
// HTTP server port, number of CPU-bound workers, and logging level.
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		Port:        envconfig.Get("HTTP_PORT", "8090"),
		WorkerCount: envconfig.GetInt("WORKER_COUNT", 4),
		LogLevel:    envconfig.Get("LOG_LEVEL", "info"),
	}

	if cfg.WorkerCount <= 0 {
		return Config{}, fmt.Errorf("WORKER_COUNT must be > 0")
	}
	return cfg, nil
}
