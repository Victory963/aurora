// Package main is the wallet-svc entrypoint (M3).
//
// Wires: Postgres store (double-entry ledger + outbox), an outbox relay that
// drains events to Kafka, and a consumer that opens a wallet on each new user.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dazn/aurora/services/wallet-svc/internal/config"
	"github.com/dazn/aurora/services/wallet-svc/internal/consumer"
	"github.com/dazn/aurora/services/wallet-svc/internal/kafkapub"
	"github.com/dazn/aurora/services/wallet-svc/internal/outbox"
	"github.com/dazn/aurora/services/wallet-svc/internal/server"
	"github.com/dazn/aurora/services/wallet-svc/internal/store"
)

const serviceName = "wallet-svc"
const version = "0.1.0-m3"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}
	slog.Info("starting service", "service", serviceName, "version", version,
		"addr", cfg.HTTPAddr, "currency", cfg.Currency, "db_host", cfg.DBHost)

	connectCtx, connectCancel := context.WithTimeout(context.Background(), 30*time.Second)
	st, err := store.NewPostgres(connectCtx, cfg.DBDSN())
	connectCancel()
	if err != nil {
		slog.Error("failed to connect to postgres", "error", err)
		os.Exit(1)
	}
	defer st.Close()

	migCtx, migCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = st.Migrate(migCtx, cfg.Currency)
	migCancel()
	if err != nil {
		slog.Error("failed to run migrations", "error", err)
		os.Exit(1)
	}
	slog.Info("migrations applied")

	// Background workers (relay + consumer) share this context.
	bgCtx, bgCancel := context.WithCancel(context.Background())

	var pub kafkapub.Publisher = kafkapub.Nop{}
	var cons *consumer.Consumer
	if cfg.KafkaEnabled() {
		w := kafkapub.New(cfg.KafkaBrokers)
		pub = w
		go outbox.New(st, w, cfg.RelayInterval).Run(bgCtx)
		slog.Info("outbox relay started", "interval", cfg.RelayInterval.String(), "topic", cfg.OutboxTopic)

		cons = consumer.New(cfg.KafkaBrokers, cfg.UserEventsTopic, cfg.ConsumerGroup, cfg.Currency, st)
		go cons.Run(bgCtx)
		slog.Info("user-events consumer started", "topic", cfg.UserEventsTopic, "group", cfg.ConsumerGroup)
	} else {
		slog.Info("kafka disabled (AURORA_WALLET_KAFKA_BROKERS unset); relay + consumer off")
	}

	srv := server.New(st, cfg.Currency, cfg.OutboxTopic, version)
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
	bgCancel() // stop relay + consumer
	if cons != nil {
		_ = cons.Close()
	}
	_ = pub.Close()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
	slog.Info("shutdown complete")
}
