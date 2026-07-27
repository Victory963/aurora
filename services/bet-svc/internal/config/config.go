// Package config loads bet-svc runtime configuration from AURORA_BET_* env.
package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	HTTPAddr string

	DBHost string
	DBPort string
	DBName string
	DBUser string
	DBPass string

	// Cross-service HTTP (caller-side timeouts per repo convention).
	WalletURL string
	RoomURL   string // empty => room-membership checks disabled (dev only)

	// KafkaBrokers empty => event publishing disabled (Nop).
	KafkaBrokers string
	KafkaTopic   string

	// JWTSecret is shared with identity-svc (HS256; ADR-0006 §3).
	JWTSecret string

	// RakeBps: pool rake in basis points (500 = 5%).
	RakeBps int
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:     getEnv("AURORA_BET_HTTP_ADDR", ":8086"),
		DBHost:       getEnv("AURORA_BET_DB_HOST", "localhost"),
		DBPort:       getEnv("AURORA_BET_DB_PORT", "5432"),
		DBName:       getEnv("AURORA_BET_DB_NAME", "aurora_bet"),
		DBUser:       getEnv("AURORA_BET_DB_USER", "aurora"),
		DBPass:       getEnv("AURORA_BET_DB_PASS", "aurora_dev_password"),
		WalletURL:    getEnv("AURORA_BET_WALLET_URL", "http://localhost:8083"),
		RoomURL:      getEnv("AURORA_BET_ROOM_URL", ""),
		KafkaBrokers: getEnv("AURORA_BET_KAFKA_BROKERS", ""),
		KafkaTopic:   getEnv("AURORA_BET_KAFKA_TOPIC", "aurora.bet.lifecycle.v1"),
		JWTSecret:    getEnv("AURORA_BET_JWT_SECRET", "aurora_dev_jwt_secret_change_me"),
	}
	if cfg.HTTPAddr == "" {
		return nil, fmt.Errorf("AURORA_BET_HTTP_ADDR must be set")
	}
	rake, err := strconv.Atoi(getEnv("AURORA_BET_RAKE_BPS", "500"))
	if err != nil || rake < 0 || rake > 2000 {
		return nil, fmt.Errorf("invalid AURORA_BET_RAKE_BPS (0-2000): %v", err)
	}
	cfg.RakeBps = rake
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
