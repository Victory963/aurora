// Package main is the room-svc entrypoint (M4).
//
// Wires: Postgres (rooms/members) + ScyllaDB (chat messages) + NATS (ephemeral
// presence/typing/message signals — the first NATS producer in Aurora).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dazn/aurora/services/room-svc/internal/chat"
	"github.com/dazn/aurora/services/room-svc/internal/clients"
	"github.com/dazn/aurora/services/room-svc/internal/config"
	"github.com/dazn/aurora/services/room-svc/internal/server"
	"github.com/dazn/aurora/services/room-svc/internal/signals"
	"github.com/dazn/aurora/services/room-svc/internal/store"
)

const serviceName = "room-svc"
const version = "0.2.0-m7"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	slog.Info("starting service", "service", serviceName, "version", version,
		"addr", cfg.HTTPAddr, "db_host", cfg.DBHost, "scylla", cfg.ScyllaHosts)

	// Postgres (rooms/members).
	pgCtx, pgCancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.NewPostgres(pgCtx, cfg.DBDSN())
	pgCancel()
	if err != nil {
		slog.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	migCtx, migCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = st.Migrate(migCtx)
	migCancel()
	if err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("postgres migrations applied")

	// ScyllaDB (chat).
	chatStore, err := chat.New(cfg.ScyllaHosts, cfg.ScyllaPort, cfg.ScyllaKeyspace)
	if err != nil {
		slog.Error("failed to connect to scylla", "error", err)
		os.Exit(1)
	}
	defer chatStore.Close()
	slog.Info("scylla connected", "keyspace", cfg.ScyllaKeyspace)

	// NATS (ephemeral signals).
	var sig signals.Publisher = signals.Nop{}
	if cfg.NATSEnabled() {
		n, err := signals.New(cfg.NATSURL)
		if err != nil {
			slog.Error("failed to connect to nats", "error", err)
			os.Exit(1)
		}
		sig = n
		slog.Info("nats connected", "url", cfg.NATSURL)
	} else {
		slog.Info("nats disabled (AURORA_ROOM_NATS_URL unset); signals are nop")
	}
	defer func() { _ = sig.Close() }()

	// M7 AI-in-room dependencies (ADR-0007). Empty URL => disabled client;
	// ProposeAiPool returns 503 not_configured, the rest of room-svc is unaffected.
	aiClient := clients.NewAI(cfg.AIURL)
	betClient := clients.NewBet(cfg.BetURL)
	if aiClient.Enabled() && betClient.Enabled() {
		slog.Info("ai-in-room enabled", "ai_url", cfg.AIURL, "bet_url", cfg.BetURL)
	} else {
		slog.Info("ai-in-room disabled (AURORA_ROOM_AI_URL/BET_URL unset); ProposeAiPool => 503")
	}

	srv := server.New(st, chatStore, sig, aiClient, betClient, []byte(cfg.JWTSecret), version)
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           srv.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		// ProposeAiPool orchestrates ai-agent (up to ~22s) + bet-svc; allow headroom.
		WriteTimeout: 45 * time.Second,
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
