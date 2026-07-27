// Package graph wraps Neo4j behind a small interface so the projector and the
// HTTP server are unit-testable without a database (ADR-0008).
//
// PII boundary: only user ids + kyc_country + created_at enter the graph.
// Email / display_name are dropped at projection time and never stored here.
package graph

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// User is the id-only projection of an identity (no PII).
type User struct {
	ID              string
	KYCCountry      string
	CreatedAtUnixMS int64
}

// Bet is one BET edge from a user to a pool.
type Bet struct {
	BetID      string
	UserID     string
	PoolID     string
	RoomID     string
	Selection  string
	StakeMinor int64
	AtUnixMS   int64
}

// PoolSettled updates a pool node's terminal state.
type PoolSettled struct {
	PoolID          string
	WinningOptionID string
	TotalStakeMinor int64
	RakeMinor       int64
	Refunded        bool
}

type CoBettor struct {
	UserID           string
	SharedPools      int32
	LastSharedAtUnix int64
}

type SybilCandidate struct {
	UserID            string
	SharedSameSelPool int32
	CreatedGapMS      int64
}

type Influence struct {
	Followers    int32
	FollowEvents int32
	AvgLagMS     int64
	PoolsLed     int32
}

type UserGraph struct {
	User      User
	Bets      []Bet
	CoBettors int32
}

// Store is the persistence surface (implemented by *Neo4j; faked in tests).
type Store interface {
	UpsertUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, userID string) error
	UpsertBet(ctx context.Context, b Bet) error
	SettlePool(ctx context.Context, s PoolSettled) error

	CoBettors(ctx context.Context, userID string, limit int) ([]CoBettor, error)
	SybilCandidates(ctx context.Context, userID string, minShared int) ([]SybilCandidate, error)
	Influence(ctx context.Context, userID string) (*Influence, error)
	UserGraph(ctx context.Context, userID string) (*UserGraph, error)

	Ping(ctx context.Context) error
	Close(ctx context.Context) error
}

// ---------------------------------------------------------------- Neo4j -----

type Neo4j struct {
	driver neo4j.DriverWithContext
}

func New(ctx context.Context, uri, user, pass string) (*Neo4j, error) {
	driver, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(user, pass, ""))
	if err != nil {
		return nil, fmt.Errorf("neo4j driver: %w", err)
	}
	if err := driver.VerifyConnectivity(ctx); err != nil {
		_ = driver.Close(ctx)
		return nil, fmt.Errorf("neo4j connectivity: %w", err)
	}
	return &Neo4j{driver: driver}, nil
}

// EnsureSchema creates uniqueness constraints (id lookups back every write).
func (g *Neo4j) EnsureSchema(ctx context.Context) error {
	stmts := []string{
		"CREATE CONSTRAINT user_id_unique IF NOT EXISTS FOR (u:User) REQUIRE u.id IS UNIQUE",
		"CREATE CONSTRAINT pool_id_unique IF NOT EXISTS FOR (p:Pool) REQUIRE p.id IS UNIQUE",
	}
	for _, s := range stmts {
		if _, err := g.write(ctx, s, nil); err != nil {
			return fmt.Errorf("ensure schema (%s): %w", s, err)
		}
	}
	return nil
}

func (g *Neo4j) UpsertUser(ctx context.Context, u User) error {
	// Tombstone guard: never resurrect a GDPR-deleted user (see DeleteUser).
	_, err := g.write(ctx, `
		MERGE (u:User {id: $id})
		WITH u
		WHERE coalesce(u.deleted, false) = false
		SET u.kyc_country = $country, u.created_at_ms = $created
	`, map[string]any{"id": u.ID, "country": u.KYCCountry, "created": u.CreatedAtUnixMS})
	return err
}

// DeleteUser is the GDPR path. It leaves a TOMBSTONE ({id, deleted:true}, all
// edges and attributes removed) rather than hard-deleting the node: the bet
// topic has no cross-ordering with the identity topic, so a late/redelivered
// bet.placed.v1 would otherwise MERGE the user straight back into existence
// with their betting history ("resurrection"). The tombstone blocks that in
// UpsertBet while keeping zero personal data (id is an opaque UUID).
func (g *Neo4j) DeleteUser(ctx context.Context, userID string) error {
	_, err := g.write(ctx, `
		MERGE (u:User {id: $id})
		WITH u
		OPTIONAL MATCH (u)-[r]-()
		DELETE r
		WITH DISTINCT u
		SET u = {id: $id, deleted: true}
	`, map[string]any{"id": userID})
	return err
}

// UpsertBet is idempotent on bet_id (at-least-once delivery replays safely).
// The user node is MERGEd too: a bet event may arrive before the user event
// on a fresh consumer group (different topics have no cross-ordering) — but a
// tombstoned (GDPR-deleted) user never gets edges again.
func (g *Neo4j) UpsertBet(ctx context.Context, b Bet) error {
	_, err := g.write(ctx, `
		MERGE (u:User {id: $uid})
		WITH u
		WHERE coalesce(u.deleted, false) = false
		MERGE (p:Pool {id: $pid})
		ON CREATE SET p.room_id = $room
		MERGE (u)-[bet:BET {bet_id: $bid}]->(p)
		SET bet.selection = $sel, bet.stake_minor = $stake, bet.at_ms = $at
	`, map[string]any{
		"uid": b.UserID, "pid": b.PoolID, "room": b.RoomID,
		"bid": b.BetID, "sel": b.Selection, "stake": b.StakeMinor, "at": b.AtUnixMS,
	})
	return err
}

func (g *Neo4j) SettlePool(ctx context.Context, s PoolSettled) error {
	_, err := g.write(ctx, `
		MERGE (p:Pool {id: $pid})
		SET p.settled = true, p.winning_option_id = $win,
		    p.total_stake_minor = $total, p.rake_minor = $rake, p.refunded = $refunded
	`, map[string]any{
		"pid": s.PoolID, "win": s.WinningOptionID,
		"total": s.TotalStakeMinor, "rake": s.RakeMinor, "refunded": s.Refunded,
	})
	return err
}

// ----------------------------------------------------------------- queries --

func (g *Neo4j) CoBettors(ctx context.Context, userID string, limit int) ([]CoBettor, error) {
	records, err := g.read(ctx, `
		MATCH (me:User {id: $id})-[mb:BET]->(p:Pool)<-[ob:BET]-(o:User)
		WHERE o.id <> $id
		RETURN o.id AS user_id,
		       count(DISTINCT p) AS shared_pools,
		       max(ob.at_ms) AS last_at
		ORDER BY shared_pools DESC, last_at DESC
		LIMIT $limit
	`, map[string]any{"id": userID, "limit": limit})
	if err != nil {
		return nil, err
	}
	out := make([]CoBettor, 0, len(records))
	for _, r := range records {
		out = append(out, CoBettor{
			UserID:           asString(r, "user_id"),
			SharedPools:      int32(asInt(r, "shared_pools")),
			LastSharedAtUnix: asInt(r, "last_at"),
		})
	}
	return out, nil
}

func (g *Neo4j) SybilCandidates(ctx context.Context, userID string, minShared int) ([]SybilCandidate, error) {
	// created_gap is -1 (= unknown) when either account's creation time hasn't
	// been projected yet (bare node from a bet-before-user-event window):
	// coalesce-to-0 would FABRICATE a zero gap — the strongest sybil signal.
	// Unknown gaps sort last, not first.
	records, err := g.read(ctx, `
		MATCH (me:User {id: $id})-[mb:BET]->(p:Pool)<-[ob:BET]-(o:User)
		WHERE o.id <> $id AND ob.selection = mb.selection
		WITH me, o, count(DISTINCT p) AS shared
		WHERE shared >= $min
		RETURN o.id AS user_id, shared,
		       CASE WHEN me.created_at_ms IS NULL OR o.created_at_ms IS NULL THEN -1
		            ELSE abs(me.created_at_ms - o.created_at_ms) END AS created_gap
		ORDER BY shared DESC,
		         CASE WHEN created_gap < 0 THEN 9223372036854775807 ELSE created_gap END ASC
		LIMIT 20
	`, map[string]any{"id": userID, "min": minShared})
	if err != nil {
		return nil, err
	}
	out := make([]SybilCandidate, 0, len(records))
	for _, r := range records {
		out = append(out, SybilCandidate{
			UserID:            asString(r, "user_id"),
			SharedSameSelPool: int32(asInt(r, "shared")),
			CreatedGapMS:      asInt(r, "created_gap"),
		})
	}
	return out, nil
}

func (g *Neo4j) Influence(ctx context.Context, userID string) (*Influence, error) {
	// Aggregate PER FOLLOWER BET (min lag over my qualifying bets), not per
	// (my-bet, follower-bet) pair — multiple own bets on the same pool would
	// otherwise inflate follow_events and skew avg_lag across the product.
	records, err := g.read(ctx, `
		MATCH (me:User {id: $id})-[mb:BET]->(p:Pool)<-[ob:BET]-(o:User)
		WHERE o.id <> $id AND ob.selection = mb.selection AND ob.at_ms > mb.at_ms
		WITH o, p, ob, min(ob.at_ms - mb.at_ms) AS lag
		RETURN count(DISTINCT o) AS followers,
		       count(*) AS follow_events,
		       coalesce(avg(lag), 0) AS avg_lag,
		       count(DISTINCT p) AS pools_led
	`, map[string]any{"id": userID})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return &Influence{}, nil
	}
	r := records[0]
	return &Influence{
		Followers:    int32(asInt(r, "followers")),
		FollowEvents: int32(asInt(r, "follow_events")),
		AvgLagMS:     int64(asFloat(r, "avg_lag")),
		PoolsLed:     int32(asInt(r, "pools_led")),
	}, nil
}

func (g *Neo4j) UserGraph(ctx context.Context, userID string) (*UserGraph, error) {
	records, err := g.read(ctx, `
		MATCH (u:User {id: $id})
		WHERE coalesce(u.deleted, false) = false
		OPTIONAL MATCH (u)-[b:BET]->(p:Pool)
		WITH u, collect({pool: p.id, room: p.room_id, sel: b.selection,
		                 stake: b.stake_minor, at: b.at_ms}) AS bets
		OPTIONAL MATCH (u)-[:BET]->(:Pool)<-[:BET]-(o:User)
		WHERE o.id <> u.id
		RETURN u.id AS id, u.kyc_country AS country, u.created_at_ms AS created,
		       bets, count(DISTINCT o) AS co_bettors
	`, map[string]any{"id": userID})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil // user not (yet) projected
	}
	r := records[0]
	out := &UserGraph{
		User: User{
			ID:              asString(r, "id"),
			KYCCountry:      asString(r, "country"),
			CreatedAtUnixMS: asInt(r, "created"),
		},
		CoBettors: int32(asInt(r, "co_bettors")),
	}
	if raw, ok := r.Get("bets"); ok {
		if list, ok := raw.([]any); ok {
			for _, item := range list {
				m, ok := item.(map[string]any)
				if !ok || m["pool"] == nil { // OPTIONAL MATCH miss => null row
					continue
				}
				out.Bets = append(out.Bets, Bet{
					UserID:     userID,
					PoolID:     str(m["pool"]),
					RoomID:     str(m["room"]),
					Selection:  str(m["sel"]),
					StakeMinor: i64(m["stake"]),
					AtUnixMS:   i64(m["at"]),
				})
			}
		}
	}
	return out, nil
}

func (g *Neo4j) Ping(ctx context.Context) error {
	return g.driver.VerifyConnectivity(ctx)
}

func (g *Neo4j) Close(ctx context.Context) error { return g.driver.Close(ctx) }

// ----------------------------------------------------------------- helpers --

func (g *Neo4j) write(ctx context.Context, cypher string, params map[string]any) (neo4j.ResultWithContext, error) {
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeWrite})
	defer session.Close(ctx)
	_, err := session.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		_, err = res.Consume(ctx)
		return nil, err
	})
	return nil, err
}

func (g *Neo4j) read(ctx context.Context, cypher string, params map[string]any) ([]*neo4j.Record, error) {
	session := g.driver.NewSession(ctx, neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead})
	defer session.Close(ctx)
	out, err := session.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		res, err := tx.Run(ctx, cypher, params)
		if err != nil {
			return nil, err
		}
		return res.Collect(ctx)
	})
	if err != nil {
		return nil, err
	}
	records, _ := out.([]*neo4j.Record)
	return records, nil
}

func asString(r *neo4j.Record, key string) string {
	v, _ := r.Get(key)
	return str(v)
}

func asInt(r *neo4j.Record, key string) int64 {
	v, _ := r.Get(key)
	return i64(v)
}

func asFloat(r *neo4j.Record, key string) float64 {
	v, _ := r.Get(key)
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	default:
		return 0
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func i64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}
