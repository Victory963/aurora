// Package consumer subscribes to identity lifecycle events and opens a wallet
// for each new user — the first consumer of the M1/M2 event mesh (ADR-0004 §4).
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/dazn/aurora/services/wallet-svc/internal/kafkautil"
)

const eventUserCreated = "user.lifecycle.created.v1"

// WalletOpener opens a wallet idempotently.
type WalletOpener interface {
	OpenWallet(ctx context.Context, userID, currency string, nowUnixMS int64) error
}

type Consumer struct {
	reader   *kafka.Reader
	opener   WalletOpener
	currency string
}

func New(brokers, topic, group, currency string, opener WalletOpener) *Consumer {
	return &Consumer{
		reader: kafka.NewReader(kafka.ReaderConfig{
			Brokers: kafkautil.Brokers(brokers),
			Topic:   topic,
			GroupID: group,
		}),
		opener:   opener,
		currency: currency,
	}
}

// envelope is the minimal shape we need off the identity event.
type envelope struct {
	EventType string `json:"event_type"`
	UserID    string `json:"user_id"`
}

// Run consumes until ctx is cancelled. Offsets are committed only after a
// message is successfully processed (at-least-once); OpenWallet is idempotent,
// so re-delivery is harmless.
func (c *Consumer) Run(ctx context.Context) {
	for {
		m, err := c.reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			slog.Warn("consumer fetch failed", "error", err)
			continue
		}
		if err := c.handle(ctx, m.Value); err != nil {
			// Do not commit — the message will be redelivered and retried.
			slog.Error("consumer handle failed; will retry", "error", err)
			continue
		}
		if err := c.reader.CommitMessages(ctx, m); err != nil {
			slog.Warn("consumer commit failed", "error", err)
		}
	}
}

func (c *Consumer) handle(ctx context.Context, value []byte) error {
	var env envelope
	if err := json.Unmarshal(value, &env); err != nil {
		// Unparseable message: log and skip (commit) rather than block the group.
		slog.Warn("consumer: undecodable message, skipping", "error", err)
		return nil
	}
	if env.EventType != eventUserCreated || env.UserID == "" {
		return nil // not interested
	}
	return c.opener.OpenWallet(ctx, env.UserID, c.currency, time.Now().UnixMilli())
}

func (c *Consumer) Close() error { return c.reader.Close() }
