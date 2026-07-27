// Package config loads graph-svc runtime configuration from AURORA_GRAPH_* env.
package config

import (
	"fmt"
	"os"
)

type Config struct {
	HTTPAddr string

	// Neo4j connection (bolt).
	Neo4jURI  string
	Neo4jUser string
	Neo4jPass string

	// Kafka: empty brokers => projection disabled (queries still serve
	// whatever is already in the graph;健康检查会标 degraded)。
	KafkaBrokers  string
	IdentityTopic string
	BetTopic      string
	GroupID       string

	// JWTSecret is shared with identity-svc (HS256). 4th copy — ADR-0008.
	JWTSecret string
}

func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:      getEnv("AURORA_GRAPH_HTTP_ADDR", ":8087"),
		Neo4jURI:      getEnv("AURORA_GRAPH_NEO4J_URI", "bolt://localhost:7687"),
		Neo4jUser:     getEnv("AURORA_GRAPH_NEO4J_USER", "neo4j"),
		Neo4jPass:     getEnv("AURORA_GRAPH_NEO4J_PASS", "aurora_dev_password"),
		KafkaBrokers:  getEnv("AURORA_GRAPH_KAFKA_BROKERS", ""),
		IdentityTopic: getEnv("AURORA_GRAPH_IDENTITY_TOPIC", "aurora.identity.user-lifecycle.v1"),
		BetTopic:      getEnv("AURORA_GRAPH_BET_TOPIC", "aurora.bet.lifecycle.v1"),
		GroupID:       getEnv("AURORA_GRAPH_GROUP_ID", "graph-svc"),
		JWTSecret:     getEnv("AURORA_GRAPH_JWT_SECRET", "aurora_dev_jwt_secret_change_me"),
	}
	if cfg.Neo4jURI == "" {
		return nil, fmt.Errorf("AURORA_GRAPH_NEO4J_URI must be set")
	}
	return cfg, nil
}

func (c *Config) KafkaEnabled() bool { return c.KafkaBrokers != "" }

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
