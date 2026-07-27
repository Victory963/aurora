// Package outbox drains the transactional-outbox table to Kafka (at-least-once).
// See ADR-0004 §3.
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/dazn/aurora/services/wallet-svc/internal/store"
)

// Store is the persistence surface the relay needs.
type Store interface {
	FetchUnpublishedOutbox(ctx context.Context, limit int) ([]*store.OutboxRow, error)
	MarkOutboxPublished(ctx context.Context, id string, nowUnixMS int64) error
}

// Publisher delivers a single message.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, payload []byte) error
}

type Relay struct {
	store    Store
	pub      Publisher
	interval time.Duration
	batch    int
}

func New(st Store, pub Publisher, interval time.Duration) *Relay {
	return &Relay{store: st, pub: pub, interval: interval, batch: 100}
}

// Run polls until ctx is cancelled. Delivery is at-least-once: a row is marked
// published only after a successful Publish; a crash in between re-delivers it.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.drain(ctx)
		}
	}
}

func (r *Relay) drain(ctx context.Context) {
	rows, err := r.store.FetchUnpublishedOutbox(ctx, r.batch)
	if err != nil {
		slog.Warn("outbox fetch failed", "error", err)
		return
	}
	for _, row := range rows {
		if err := r.pub.Publish(ctx, row.Topic, row.MsgKey, row.Payload); err != nil {
			// Stop this round; unpublished rows are retried on the next tick.
			slog.Warn("outbox publish failed", "id", row.ID, "topic", row.Topic, "error", err)
			return
		}
		if err := r.store.MarkOutboxPublished(ctx, row.ID, time.Now().UnixMilli()); err != nil {
			// Already delivered to Kafka; will be re-published next tick (consumers
			// must dedupe by transaction_id — documented in ADR-0004).
			slog.Warn("outbox mark failed", "id", row.ID, "error", err)
			return
		}
	}
}
