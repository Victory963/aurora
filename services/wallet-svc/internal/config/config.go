// Package config loads wallet-svc runtime configuration from AURORA_WALLET_* env.
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

	Currency string

	// Event mesh. KafkaBrokers empty => relay + consumer disabled (Nop), so the
	// service still serves Credit/Debit/GetBalance without Kafka.
	KafkaBrokers     string
	OutboxTopic      string
	UserEventsTopic  string
	ConsumerGroup    string
	RelayInterval    time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:        getEnv("AURORA_WALLET_HTTP_ADDR", ":8083"),
		DBHost:          getEnv("AURORA_WALLET_DB_HOST", "localhost"),
		DBPort:          getEnv("AURORA_WALLET_DB_PORT", "5432"),
		DBName:          getEnv("AURORA_WALLET_DB_NAME", "aurora_wallet"),
		DBUser:          getEnv("AURORA_WALLET_DB_USER", "aurora"),
		DBPass:          getEnv("AURORA_WALLET_DB_PASS", "aurora_dev_password"),
		Currency:        getEnv("AURORA_WALLET_CURRENCY", "AUC"),
		KafkaBrokers:    getEnv("AURORA_WALLET_KAFKA_BROKERS", ""),
		OutboxTopic:     getEnv("AURORA_WALLET_OUTBOX_TOPIC", "aurora.wallet.transaction.v1"),
		UserEventsTopic: getEnv("AURORA_WALLET_USER_EVENTS_TOPIC", "aurora.identity.user-lifecycle.v1"),
		ConsumerGroup:   getEnv("AURORA_WALLET_CONSUMER_GROUP", "wallet-svc"),
	}
	if cfg.HTTPAddr == "" {
		return nil, fmt.Errorf("AURORA_WALLET_HTTP_ADDR must be set")
	}
	var err error
	if cfg.RelayInterval, err = time.ParseDuration(getEnv("AURORA_WALLET_RELAY_INTERVAL", "1s")); err != nil {
		return nil, fmt.Errorf("invalid AURORA_WALLET_RELAY_INTERVAL: %w", err)
	}
	return cfg, nil
}

func (c *Config) KafkaEnabled() bool { return c.KafkaBrokers != "" }

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
