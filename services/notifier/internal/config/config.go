package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	HTTPPort      string
	DBDSN         string
	LogLevel      string
	KafkaBrokers  string
}

// Load читает переменные окружения и возвращает конфигурацию notifier-сервиса.
// PostgreSQL DSN для хранения инцидентов, Kafka brokers для потребления аномалий.
func Load() (Config, error) {
	_ = godotenv.Load()

	cfg := Config{
		HTTPPort:     getEnv("HTTP_PORT", "8082"),
		DBDSN:        getEnv("DB_DSN", "postgres://postgres:postgres@localhost:5432/notifier?sslmode=disable"),
		LogLevel:     getEnv("LOG_LEVEL", "info"),
		KafkaBrokers: getEnv("KAFKA_BROKERS", "kafka:9092"),
	}

	if cfg.DBDSN == "" {
		return Config{}, fmt.Errorf("DB_DSN must not be empty")
	}

	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}
