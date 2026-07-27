// Package config loads room-svc runtime configuration from AURORA_ROOM_* env.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	HTTPAddr string

	DBHost string
	DBPort string
	DBName string
	DBUser string
	DBPass string

	ScyllaHosts    []string
	ScyllaPort     int
	ScyllaKeyspace string

	// NATSURL empty => ephemeral signals disabled (Nop publisher).
	NATSURL string

	// JWTSecret is shared with identity-svc (HS256). See ADR-0005 §3.
	JWTSecret string

	// AIURL/BetURL power the M7 ProposeAiPool orchestration (ADR-0007).
	// Empty => that dependency is disabled; ProposeAiPool returns 503
	// not_configured while the rest of room-svc keeps working.
	AIURL  string
	BetURL string
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:       getEnv("AURORA_ROOM_HTTP_ADDR", ":8084"),
		DBHost:         getEnv("AURORA_ROOM_DB_HOST", "localhost"),
		DBPort:         getEnv("AURORA_ROOM_DB_PORT", "5432"),
		DBName:         getEnv("AURORA_ROOM_DB_NAME", "aurora_room"),
		DBUser:         getEnv("AURORA_ROOM_DB_USER", "aurora"),
		DBPass:         getEnv("AURORA_ROOM_DB_PASS", "aurora_dev_password"),
		ScyllaHosts:    splitCSV(getEnv("AURORA_ROOM_SCYLLA_HOSTS", "localhost")),
		ScyllaKeyspace: getEnv("AURORA_ROOM_SCYLLA_KEYSPACE", "aurora_room"),
		NATSURL:        getEnv("AURORA_ROOM_NATS_URL", ""),
		JWTSecret:      getEnv("AURORA_ROOM_JWT_SECRET", "aurora_dev_jwt_secret_change_me"),
		AIURL:          getEnv("AURORA_ROOM_AI_URL", ""),
		BetURL:         getEnv("AURORA_ROOM_BET_URL", ""),
	}
	if cfg.HTTPAddr == "" {
		return nil, fmt.Errorf("AURORA_ROOM_HTTP_ADDR must be set")
	}
	port, err := strconv.Atoi(getEnv("AURORA_ROOM_SCYLLA_PORT", "9042"))
	if err != nil {
		return nil, fmt.Errorf("invalid AURORA_ROOM_SCYLLA_PORT: %w", err)
	}
	cfg.ScyllaPort = port
	if len(cfg.ScyllaHosts) == 0 {
		return nil, fmt.Errorf("AURORA_ROOM_SCYLLA_HOSTS must list at least one host")
	}
	return cfg, nil
}

func (c *Config) NATSEnabled() bool { return c.NATSURL != "" }

func (c *Config) DBDSN() string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		c.DBHost, c.DBPort, c.DBUser, c.DBPass, c.DBName)
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
