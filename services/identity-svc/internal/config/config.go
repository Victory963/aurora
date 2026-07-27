// Package config loads runtime configuration from environment variables.
//
// All env vars are prefixed AURORA_IDENTITY_ to avoid collision when running
// multiple services in the same shell.
package config

import (
	"fmt"
	"os"
	"time"
)

type Config struct {
	HTTPAddr string
	DBHost   string
	DBPort   string
	DBName   string
	DBUser   string
	DBPass   string

	// M1 Event Mesh. KafkaBrokers empty => event publishing disabled (Nop),
	// so the service still runs in an M0-style stack without Kafka.
	KafkaBrokers string
	KafkaTopic   string

	// M2 auth. JWTSecret signs HS256 access tokens (dev default; Vault in M9).
	JWTSecret  string
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:     getEnv("AURORA_IDENTITY_HTTP_ADDR", ":8081"),
		DBHost:       getEnv("AURORA_IDENTITY_DB_HOST", "localhost"),
		DBPort:       getEnv("AURORA_IDENTITY_DB_PORT", "5432"),
		DBName:       getEnv("AURORA_IDENTITY_DB_NAME", "aurora_identity"),
		DBUser:       getEnv("AURORA_IDENTITY_DB_USER", "aurora"),
		DBPass:       getEnv("AURORA_IDENTITY_DB_PASS", "aurora_dev_password"),
		KafkaBrokers: getEnv("AURORA_IDENTITY_KAFKA_BROKERS", ""),
		KafkaTopic:   getEnv("AURORA_IDENTITY_KAFKA_TOPIC", "aurora.identity.user-lifecycle.v1"),
		JWTSecret:    getEnv("AURORA_IDENTITY_JWT_SECRET", "aurora_dev_jwt_secret_change_me"),
	}
	if cfg.HTTPAddr == "" {
		return nil, fmt.Errorf("AURORA_IDENTITY_HTTP_ADDR must be set")
	}

	var err error
	if cfg.AccessTTL, err = time.ParseDuration(getEnv("AURORA_IDENTITY_ACCESS_TTL", "15m")); err != nil {
		return nil, fmt.Errorf("invalid AURORA_IDENTITY_ACCESS_TTL: %w", err)
	}
	if cfg.RefreshTTL, err = time.ParseDuration(getEnv("AURORA_IDENTITY_REFRESH_TTL", "720h")); err != nil {
		return nil, fmt.Errorf("invalid AURORA_IDENTITY_REFRESH_TTL: %w", err)
	}
	return cfg, nil
}

// KafkaEnabled reports whether event publishing is configured.
func (c *Config) KafkaEnabled() bool {
	return c.KafkaBrokers != ""
}

func (c *Config) DBDSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName)
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
