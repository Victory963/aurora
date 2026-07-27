// Package store persists pools, options, and bets in Postgres (aurora_bet) and
// implements the settlement state machine OPEN → SETTLING → SETTLED (ADR-0006).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	PoolOpen     = "OPEN"
	PoolSettling = "SETTLING"
	PoolSettled  = "SETTLED"
	BetPlaced    = "PLACED"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrDuplicateKey = errors.New("duplicate idempotency key")
	ErrBadTransition = errors.New("invalid pool state transition")
)

type Option struct {
	ID         string
	Label      string
	OddsX100   int64
	StakeMinor int64
	Bettors    int32
}

type Pool struct {
	ID              string
	RoomID          string
	Question        string
	Status          string
	CreatedBy       string
	RakeBps         int32
	WinningOptionID string
	Options         []Option
	TotalStakeMinor int64
	CreatedAtUnixMS int64
	SettledAtUnixMS int64
}

type Bet struct {
	ID              string
	PoolID          string
	OptionID        string
	UserID          string
	StakeMinor      int64
	IdempotencyKey  string
	Status          string
	PayoutMinor     int64
	CreatedAtUnixMS int64
}

type Store struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, dsn string) (*Store, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		p, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = p.Ping(ctx); err == nil {
				return &Store{pool: p}, nil
			}
			p.Close()
		}
		lastErr = err
		time.Sleep(1 * time.Second)
	}
	return nil, fmt.Errorf("postgres unreachable after 30s: %w", lastErr)
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) PingDB(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS pools (
			id                  UUID PRIMARY KEY,
			room_id             TEXT NOT NULL DEFAULT '',
			question            TEXT NOT NULL,
			status              TEXT NOT NULL,
			rake_bps            INT  NOT NULL,
			created_by          TEXT NOT NULL,
			winning_option_id   UUID,
			created_at_unix_ms  BIGINT NOT NULL,
			settled_at_unix_ms  BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_pools_room ON pools(room_id);
		CREATE TABLE IF NOT EXISTS pool_options (
			id        UUID PRIMARY KEY,
			pool_id   UUID NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
			label     TEXT NOT NULL,
			odds_x100 BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_options_pool ON pool_options(pool_id);
		CREATE TABLE IF NOT EXISTS bets (
			id                 UUID PRIMARY KEY,
			pool_id            UUID NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
			option_id          UUID NOT NULL REFERENCES pool_options(id),
			user_id            TEXT NOT NULL,
			stake_minor        BIGINT NOT NULL,
			idempotency_key    TEXT NOT NULL UNIQUE,
			status             TEXT NOT NULL,
			payout_minor       BIGINT NOT NULL DEFAULT 0,
			created_at_unix_ms BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_bets_pool ON bets(pool_id);
	`)
	return err
}

// CreatePool inserts the pool and its options atomically.
func (s *Store) CreatePool(ctx context.Context, p *Pool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO pools (id, room_id, question, status, rake_bps, created_by, created_at_unix_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
	`, p.ID, p.RoomID, p.Question, p.Status, p.RakeBps, p.CreatedBy, p.CreatedAtUnixMS); err != nil {
		return err
	}
	for i := range p.Options {
		if p.Options[i].ID == "" {
			p.Options[i].ID = uuid.NewString()
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO pool_options (id, pool_id, label, odds_x100) VALUES ($1,$2,$3,$4)
		`, p.Options[i].ID, p.ID, p.Options[i].Label, p.Options[i].OddsX100); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// GetPool returns the pool with per-option stake aggregates.
func (s *Store) GetPool(ctx context.Context, id string) (*Pool, error) {
	p := &Pool{}
	var winning *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, room_id, question, status, rake_bps, created_by, winning_option_id,
		       created_at_unix_ms, settled_at_unix_ms
		FROM pools WHERE id=$1
	`, id).Scan(&p.ID, &p.RoomID, &p.Question, &p.Status, &p.RakeBps, &p.CreatedBy,
		&winning, &p.CreatedAtUnixMS, &p.SettledAtUnixMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if winning != nil {
		p.WinningOptionID = *winning
	}

	rows, err := s.pool.Query(ctx, `
		SELECT o.id, o.label, o.odds_x100,
		       COALESCE(SUM(b.stake_minor), 0), COUNT(b.id)
		FROM pool_options o
		LEFT JOIN bets b ON b.option_id = o.id
		WHERE o.pool_id = $1
		GROUP BY o.id, o.label, o.odds_x100
		ORDER BY o.label
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var o Option
		var bettors int64
		if err := rows.Scan(&o.ID, &o.Label, &o.OddsX100, &o.StakeMinor, &bettors); err != nil {
			return nil, err
		}
		o.Bettors = int32(bettors)
		p.TotalStakeMinor += o.StakeMinor
		p.Options = append(p.Options, o)
	}
	return p, rows.Err()
}

func (s *Store) ListRoomPools(ctx context.Context, roomID string) ([]*Pool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM pools WHERE room_id=$1 ORDER BY created_at_unix_ms DESC LIMIT 50`, roomID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]*Pool, 0, len(ids))
	for _, id := range ids {
		p, err := s.GetPool(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) GetBetByKey(ctx context.Context, key string) (*Bet, error) {
	b := &Bet{}
	err := s.pool.QueryRow(ctx, `
		SELECT id, pool_id, option_id, user_id, stake_minor, idempotency_key, status,
		       payout_minor, created_at_unix_ms
		FROM bets WHERE idempotency_key=$1
	`, key).Scan(&b.ID, &b.PoolID, &b.OptionID, &b.UserID, &b.StakeMinor,
		&b.IdempotencyKey, &b.Status, &b.PayoutMinor, &b.CreatedAtUnixMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// InsertBetIfOpen atomically inserts the bet only while its pool is OPEN
// (single statement: the EXISTS guard and the insert share one snapshot).
// Returns (false, nil) when the pool is not OPEN, ErrDuplicateKey on an
// idempotency-key collision (caller re-reads and replays).
func (s *Store) InsertBetIfOpen(ctx context.Context, b *Bet) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO bets (id, pool_id, option_id, user_id, stake_minor, idempotency_key, status, created_at_unix_ms)
		SELECT $1,$2,$3,$4,$5,$6,$7,$8
		WHERE EXISTS (SELECT 1 FROM pools WHERE id=$2 AND status='OPEN')
	`, b.ID, b.PoolID, b.OptionID, b.UserID, b.StakeMinor, b.IdempotencyKey, b.Status, b.CreatedAtUnixMS)
	if err != nil {
		if isUniqueViolation(err) {
			return false, ErrDuplicateKey
		}
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// MarkSettling moves OPEN → SETTLING (or re-enters SETTLING with the SAME
// winning option, so a crashed settlement can resume). Any other state →
// ErrBadTransition.
func (s *Store) MarkSettling(ctx context.Context, poolID, winningOptionID string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE pools SET status='SETTLING', winning_option_id=$2
		WHERE id=$1 AND (status='OPEN' OR (status='SETTLING' AND winning_option_id=$2))
	`, poolID, winningOptionID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBadTransition
	}
	return nil
}

func (s *Store) ListPoolBets(ctx context.Context, poolID string) ([]*Bet, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, pool_id, option_id, user_id, stake_minor, idempotency_key, status,
		       payout_minor, created_at_unix_ms
		FROM bets WHERE pool_id=$1 ORDER BY created_at_unix_ms
	`, poolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Bet
	for rows.Next() {
		b := &Bet{}
		if err := rows.Scan(&b.ID, &b.PoolID, &b.OptionID, &b.UserID, &b.StakeMinor,
			&b.IdempotencyKey, &b.Status, &b.PayoutMinor, &b.CreatedAtUnixMS); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BetOutcome is one bet's settlement result (status + payout).
type BetOutcome struct {
	Status      string
	PayoutMinor int64
}

// FinalizeSettlement persists per-bet outcomes and flips the pool to SETTLED,
// all in one transaction (runs AFTER the idempotent wallet credits; a crash
// before this commit re-runs Settle safely — wallet replays by key).
func (s *Store) FinalizeSettlement(ctx context.Context, poolID string,
	results map[string]BetOutcome, settledAtUnixMS int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for betID, r := range results {
		if _, err := tx.Exec(ctx,
			`UPDATE bets SET status=$2, payout_minor=$3 WHERE id=$1`, betID, r.Status, r.PayoutMinor); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx,
		`UPDATE pools SET status='SETTLED', settled_at_unix_ms=$2 WHERE id=$1 AND status='SETTLING'`,
		poolID, settledAtUnixMS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrBadTransition
	}
	return tx.Commit(ctx)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
