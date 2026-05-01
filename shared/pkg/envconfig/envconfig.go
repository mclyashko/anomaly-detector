// Package envconfig provides small helpers for reading typed values from
// environment variables with fallback defaults. It intentionally contains no
// external dependencies so every service can import it cheaply.
package envconfig

import (
	"os"
	"strconv"
	"time"
)

// Get returns the value of key, or fallback when the variable is unset or empty.
func Get(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// GetInt returns the integer value of key, or fallback on parse failure or absence.
func GetInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

// GetDurationSec returns key's value interpreted as whole seconds, or a default.
func GetDurationSec(key string, fallbackSec int) time.Duration {
	return time.Duration(GetInt(key, fallbackSec)) * time.Second
}

// GetBool returns the boolean value of key (true/false/1/0), or the fallback.
func GetBool(key string, fallback ...bool) bool {
	v := os.Getenv(key)
	if v == "" {
		if len(fallback) > 0 {
			return fallback[0]
		}
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		if len(fallback) > 0 {
			return fallback[0]
		}
		return false
	}
	return b
}
