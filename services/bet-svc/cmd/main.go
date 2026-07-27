// Package main is the bet-svc entrypoint (M6): room-scoped closed pools with
// parimutuel settlement over wallet-svc (ADR-0006).
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dazn/aurora/services/bet-svc/internal/clients"
	"github.com/dazn/aurora/services/bet-svc/internal/config"
	"github.com/dazn/aurora/services/bet-svc/internal/events"
	eventskafka "github.com/dazn/aurora/services/bet-svc/internal/events/kafka"
	"github.com/dazn/aurora/services/bet-svc/internal/server"
	"github.com/dazn/aurora/services/bet-svc/internal/store"
)

const serviceName = "bet-svc"
const version = "0.1.0-m6"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	slog.Info("starting service", "service", serviceName, "version", version,
		"addr", cfg.HTTPAddr, "db_host", cfg.DBHost, "rake_bps", cfg.RakeBps,
		"wallet", cfg.WalletURL, "room", cfg.RoomURL)

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.NewPostgres(connectCtx, cfg.DBDSN())
	connectCancel()
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
	slog.Info("migrations applied")

	var publisher events.Publisher = events.Nop{}
	if cfg.KafkaEnabled() {
		publisher = eventskafka.New(cfg.KafkaBrokers, cfg.KafkaTopic)
		slog.Info("event publishing enabled", "brokers", cfg.KafkaBrokers, "topic", cfg.KafkaTopic)
	} else {
		slog.Info("kafka disabled (AURORA_BET_KAFKA_BROKERS unset); Nop publisher")
	}
	defer func() { _ = publisher.Close() }()

	if cfg.RoomURL == "" {
		slog.Warn("AURORA_BET_ROOM_URL unset — room membership checks DISABLED (dev only)")
	}

	srv := server.New(st,
		clients.NewWallet(cfg.WalletURL),
		clients.NewRoom(cfg.RoomURL),
		publisher,
		[]byte(cfg.JWTSecret), cfg.RakeBps, version)

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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
