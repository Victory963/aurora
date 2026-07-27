// Package events defines bet-svc's outbound event contracts. Wire JSON mirrors
// libs/events/avro/bet.placed.v1.avsc and bet.pool.settled.v1.avsc exactly.
// M6 publishes directly (no outbox — ADR-0006 §4); wallet.transaction events
// remain the money source of truth.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

const (
	TypeBetPlaced   = "bet.placed.v1"
	TypePoolSettled = "bet.pool.settled.v1"
)

type BetPlaced struct {
	BetID      string
	UserID     string
	MarketID   string // pool id
	Selection  string // option label
	StakeMinor int64
	Odds       float64 // indicative option odds
	RoomID     string
}

type betPlacedEnvelope struct {
	EventID          string  `json:"event_id"`
	EventType        string  `json:"event_type"`
	OccurredAtUnixMS int64   `json:"occurred_at_unix_ms"`
	BetID            string  `json:"bet_id"`
	UserID           string  `json:"user_id"`
	MarketID         string  `json:"market_id"`
	Selection        string  `json:"selection"`
	StakeMinor       int64   `json:"stake_minor"`
	Odds             float64 `json:"odds"`
	RoomID           string  `json:"room_id"`
}

type PoolSettled struct {
	PoolID          string
	RoomID          string
	WinningOptionID string
	TotalStakeMinor int64
	RakeMinor       int64
	Winners         int
	Refunded        bool
}

type poolSettledEnvelope struct {
	EventID          string `json:"event_id"`
	EventType        string `json:"event_type"`
	OccurredAtUnixMS int64  `json:"occurred_at_unix_ms"`
	PoolID           string `json:"pool_id"`
	RoomID           string `json:"room_id"`
	WinningOptionID  string `json:"winning_option_id"`
	TotalStakeMinor  int64  `json:"total_stake_minor"`
	RakeMinor        int64  `json:"rake_minor"`
	Winners          int    `json:"winners"`
	Refunded         bool   `json:"refunded"`
}

// Publisher publishes bet domain events (key = pool id).
type Publisher interface {
	BetPlaced(ctx context.Context, e BetPlaced) error
	PoolSettled(ctx context.Context, e PoolSettled) error
	Close() error
}

func MarshalBetPlaced(e BetPlaced) (string, []byte, error) {
	id := uuid.NewString()
	payload, err := json.Marshal(betPlacedEnvelope{
		EventID: id, EventType: TypeBetPlaced, OccurredAtUnixMS: time.Now().UnixMilli(),
		BetID: e.BetID, UserID: e.UserID, MarketID: e.MarketID, Selection: e.Selection,
		StakeMinor: e.StakeMinor, Odds: e.Odds, RoomID: e.RoomID,
	})
	return id, payload, err
}

func MarshalPoolSettled(e PoolSettled) (string, []byte, error) {
	id := uuid.NewString()
	payload, err := json.Marshal(poolSettledEnvelope{
		EventID: id, EventType: TypePoolSettled, OccurredAtUnixMS: time.Now().UnixMilli(),
		PoolID: e.PoolID, RoomID: e.RoomID, WinningOptionID: e.WinningOptionID,
		TotalStakeMinor: e.TotalStakeMinor, RakeMinor: e.RakeMinor, Winners: e.Winners,
		Refunded: e.Refunded,
	})
	return id, payload, err
}

// Nop drops events (Kafka not configured).
type Nop struct{}

func (Nop) BetPlaced(context.Context, BetPlaced) error     { return nil }
func (Nop) PoolSettled(context.Context, PoolSettled) error { return nil }
func (Nop) Close() error                                   { return nil }
