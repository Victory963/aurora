// Package main is the graph-svc entrypoint (M8): Identity Graph read model —
// Kafka events projected into Neo4j + graph query RPCs (ADR-0008).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/dazn/aurora/services/graph-svc/internal/config"
	"github.com/dazn/aurora/services/graph-svc/internal/graph"
	"github.com/dazn/aurora/services/graph-svc/internal/projector"
	"github.com/dazn/aurora/services/graph-svc/internal/server"
)

const serviceName = "graph-svc"
const version = "0.1.0-m8"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	slog.Info("starting service", "service", serviceName, "version", version,
		"addr", cfg.HTTPAddr, "neo4j", cfg.Neo4jURI, "kafka", cfg.KafkaBrokers)

	// Neo4j can take ~30-60s to accept bolt connections on a cold start.
	var store *graph.Neo4j
	deadline := time.Now().Add(90 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		store, err = graph.New(ctx, cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPass)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			slog.Error("failed to connect to neo4j", "error", err)
			os.Exit(1)
		}
		slog.Info("waiting for neo4j…", "error", err)
		time.Sleep(3 * time.Second)
	}
	defer store.Close(context.Background())

	schemaCtx, schemaCancel := context.WithTimeout(context.Background(), 30*time.Second)
	err = store.EnsureSchema(schemaCtx)
	schemaCancel()
	if err != nil {
		slog.Error("failed to ensure graph schema", "error", err)
		os.Exit(1)
	}
	slog.Info("graph schema ensured (constraints)")

	// Projection consumers (identity + bet lifecycles).
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	var proj *projector.Projector
	if cfg.KafkaEnabled() {
		brokers := []string{}
		for _, b := range strings.Split(cfg.KafkaBrokers, ",") {
			if b = strings.TrimSpace(b); b != "" {
				brokers = append(brokers, b)
			}
		}
		proj = projector.New(brokers, cfg.GroupID, []string{cfg.IdentityTopic, cfg.BetTopic}, store)
		go proj.Run(rootCtx)
		slog.Info("projection enabled", "brokers", cfg.KafkaBrokers,
			"topics", []string{cfg.IdentityTopic, cfg.BetTopic}, "group", cfg.GroupID)
	} else {
		slog.Warn("AURORA_GRAPH_KAFKA_BROKERS unset — projection DISABLED (queries serve existing graph only)")
	}

	srv := server.New(store, []byte(cfg.JWTSecret), cfg.KafkaEnabled(), version)
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		slog.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server error", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	slog.Info("shutdown signal received")
	rootCancel()
	if proj != nil {
		_ = proj.Close()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
