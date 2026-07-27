// Package main is the entry point for identity-svc.
//
// M0 scope:
//   - HTTP server on :8081
//   - Endpoints follow Connect-RPC URL convention: /aurora.identity.v1.IdentityService/{Method}
//   - JSON request/response (M1 will add binary protobuf via buf-generated SDKs)
//   - Persists users to PostgreSQL via internal/store
//
// Out of scope for M0 (deferred to later milestones):
//   - Authentication / JWT issuance        → M2 phase 2 (Identity Provider)
//   - Device tracking, sessions            → M2
//   - Identity Graph projection            → M8 (Neo4j)
//   - KYC integration                      → M9
//   - Event Mesh publishing                → M1
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dazn/aurora/services/identity-svc/internal/config"
	"github.com/dazn/aurora/services/identity-svc/internal/events"
	eventskafka "github.com/dazn/aurora/services/identity-svc/internal/events/kafka"
	"github.com/dazn/aurora/services/identity-svc/internal/server"
	"github.com/dazn/aurora/services/identity-svc/internal/store"
)

const serviceName = "identity-svc"
const version = "0.1.0-m0"

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	slog.Info("starting service",
		"service", serviceName,
		"version", version,
		"addr", cfg.HTTPAddr,
		"db_host", cfg.DBHost,
	)

	// Connect to Postgres with retry (gives Postgres container time to come up)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	st, err := store.NewPostgres(ctx, cfg.DBDSN())
	if err != nil {
		slog.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.Migrate(ctx); err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	// M1 Event Mesh: build the event publisher. Without brokers configured we use
	// a Nop publisher, so the service still runs in an M0-style stack (no Kafka).
	var publisher events.Publisher = events.Nop{}
	if cfg.KafkaEnabled() {
		publisher = eventskafka.New(cfg.KafkaBrokers, cfg.KafkaTopic)
		slog.Info("event mesh enabled", "brokers", cfg.KafkaBrokers, "topic", cfg.KafkaTopic)
	} else {
		slog.Info("event mesh disabled (AURORA_IDENTITY_KAFKA_BROKERS unset); using Nop publisher")
	}
	defer func() {
		if err := publisher.Close(); err != nil {
			slog.Warn("publisher close failed", "error", err)
		}
	}()

	srv := server.New(st, version,
		server.WithPublisher(publisher),
		server.WithAuth([]byte(cfg.JWTSecret), cfg.AccessTTL, cfg.RefreshTTL),
	)
	slog.Info("auth configured", "access_ttl", cfg.AccessTTL.String(), "refresh_ttl", cfg.RefreshTTL.String())

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	// Graceful shutdown
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
