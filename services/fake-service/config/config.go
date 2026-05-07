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

// Load читает переменные окружения и возвращает конфигурацию fake-service.
// Порт HTTP-сервера, количество CPU-bound воркеров и уровень логирования.
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
