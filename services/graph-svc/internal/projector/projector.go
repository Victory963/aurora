// Package projector consumes identity + bet lifecycle events and projects
// them into the graph (ADR-0008 §2). At-least-once: offsets commit only after
// a successful graph write; every write is a MERGE keyed by a business id, so
// redelivery is harmless. Poison messages are logged and skipped.
//
// PII boundary: user events carry email/display_name — they are dropped here
// and never reach the graph.
package projector

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/dazn/aurora/services/graph-svc/internal/graph"
)

const (
	evUserCreated = "user.lifecycle.created.v1"
	evUserDeleted = "user.lifecycle.deleted.v1"
	evBetPlaced   = "bet.placed.v1"
	evPoolSettled = "bet.pool.settled.v1"
)

// envelope is the superset of fields we need across all four event types.
// Wire JSON mirrors libs/events/avro/*.avsc (flat records).
type envelope struct {
	EventType        string `json:"event_type"`
	OccurredAtUnixMS int64  `json:"occurred_at_unix_ms"`

	// user.lifecycle.*
	UserID          string `json:"user_id"`
	KYCCountry      string `json:"kyc_country"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`

	// bet.placed.v1
	BetID      string `json:"bet_id"`
	MarketID   string `json:"market_id"` // pool id
	Selection  string `json:"selection"`
	StakeMinor int64  `json:"stake_minor"`
	RoomID     string `json:"room_id"`

	// bet.pool.settled.v1
	PoolID          string `json:"pool_id"`
	WinningOptionID string `json:"winning_option_id"`
	TotalStakeMinor int64  `json:"total_stake_minor"`
	RakeMinor       int64  `json:"rake_minor"`
	Refunded        bool   `json:"refunded"`
}

// Handle projects one raw event payload. Returning a non-nil error means
// "do not commit — retry"; nil means done (including deliberately skipped).
// Exported so unit tests can drive it without Kafka.
func Handle(ctx context.Context, store graph.Store, value []byte) error {
	var env envelope
	if err := json.Unmarshal(value, &env); err != nil {
		slog.Warn("projector: undecodable message, skipping", "error", err)
		return nil
	}
	switch env.EventType {
	case evUserCreated:
		if env.UserID == "" {
			return nil
		}
		created := env.CreatedAtUnixMS
		if created == 0 {
			created = env.OccurredAtUnixMS
		}
		// email / display_name intentionally dropped (PII boundary).
		return store.UpsertUser(ctx, graph.User{
			ID: env.UserID, KYCCountry: env.KYCCountry, CreatedAtUnixMS: created,
		})
	case evUserDeleted:
		if env.UserID == "" {
			return nil
		}
		return store.DeleteUser(ctx, env.UserID)
	case evBetPlaced:
		if env.BetID == "" || env.UserID == "" || env.MarketID == "" {
			return nil
		}
		return store.UpsertBet(ctx, graph.Bet{
			BetID: env.BetID, UserID: env.UserID, PoolID: env.MarketID,
			RoomID: env.RoomID, Selection: env.Selection,
			StakeMinor: env.StakeMinor, AtUnixMS: env.OccurredAtUnixMS,
		})
	case evPoolSettled:
		if env.PoolID == "" {
			return nil
		}
		return store.SettlePool(ctx, graph.PoolSettled{
			PoolID: env.PoolID, WinningOptionID: env.WinningOptionID,
			TotalStakeMinor: env.TotalStakeMinor, RakeMinor: env.RakeMinor,
			Refunded: env.Refunded,
		})
	default:
		return nil // unknown/uninteresting event type
	}
}

// Projector runs one consumer per topic.
type Projector struct {
	readers []*kafka.Reader
	store   graph.Store
}

func New(brokers []string, groupID string, topics []string, store graph.Store) *Projector {
	p := &Projector{store: store}
	for _, t := range topics {
		p.readers = append(p.readers, kafka.NewReader(kafka.ReaderConfig{
			Brokers: brokers,
			Topic:   t,
			GroupID: groupID,
			// Fresh group starts from the beginning: the graph is a pure
			// projection and rebuilds itself from history (ADR-0008 §2).
			StartOffset: kafka.FirstOffset,
		}))
	}
	return p
}

// Run consumes all topics until ctx is cancelled (one goroutine per reader;
// blocks until every reader exits).
func (p *Projector) Run(ctx context.Context) {
	done := make(chan struct{})
	for _, r := range p.readers {
		go func(r *kafka.Reader) {
			defer func() { done <- struct{}{} }()
			p.consume(ctx, r)
		}(r)
	}
	for range p.readers {
		<-done
	}
}

func (p *Projector) consume(ctx context.Context, r *kafka.Reader) {
	for {
		m, err := r.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			slog.Warn("projector fetch failed", "topic", r.Config().Topic, "error", err)
			continue
		}
		// Retry the SAME message in place until the graph write succeeds:
		// kafka-go never re-fetches a skipped offset in-session, and committing
		// any later message would commit past it — a `continue` here would
		// silently LOSE the event. Poison messages already return nil from
		// Handle, so blocking only happens on retryable store errors.
		for {
			if err := Handle(ctx, p.store, m.Value); err == nil {
				break
			} else {
				slog.Error("projector handle failed; retrying same offset",
					"topic", r.Config().Topic, "offset", m.Offset, "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
		if err := r.CommitMessages(ctx, m); err != nil {
			slog.Warn("projector commit failed", "error", err)
		}
	}
}

func (p *Projector) Close() error {
	var first error
	for _, r := range p.readers {
		if err := r.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}
