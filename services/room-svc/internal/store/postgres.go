// Package store persists rooms and membership in Postgres (aurora_room).
// Chat messages live in ScyllaDB (see internal/chat).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

const (
	RoleOwner  = "OWNER"
	RoleMember = "MEMBER"
)

type Room struct {
	ID              string
	Name            string
	OwnerUserID     string
	CreatedAtUnixMS int64
}

type Membership struct {
	RoomID         string
	UserID         string
	Role           string
	JoinedAtUnixMS int64
}

type Store struct {
	pool *pgxpool.Pool
}

func NewPostgres(ctx context.Context, dsn string) (*Store, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return &Store{pool: pool}, nil
			}
			pool.Close()
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
		CREATE TABLE IF NOT EXISTS rooms (
			id                 UUID PRIMARY KEY,
			name               TEXT NOT NULL,
			owner_user_id      TEXT NOT NULL,
			created_at_unix_ms BIGINT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS room_members (
			room_id          UUID NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
			user_id          TEXT NOT NULL,
			role             TEXT NOT NULL,
			joined_at_unix_ms BIGINT NOT NULL,
			PRIMARY KEY (room_id, user_id)
		);
		CREATE INDEX IF NOT EXISTS idx_room_members_user ON room_members(user_id);
	`)
	return err
}

// CreateRoom inserts a room and the owner membership atomically.
func (s *Store) CreateRoom(ctx context.Context, id, name, ownerID string, nowUnixMS int64) (*Room, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO rooms (id, name, owner_user_id, created_at_unix_ms) VALUES ($1,$2,$3,$4)`,
		id, name, ownerID, nowUnixMS); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO room_members (room_id, user_id, role, joined_at_unix_ms) VALUES ($1,$2,$3,$4)`,
		id, ownerID, RoleOwner, nowUnixMS); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Room{ID: id, Name: name, OwnerUserID: ownerID, CreatedAtUnixMS: nowUnixMS}, nil
}

func (s *Store) GetRoom(ctx context.Context, id string) (*Room, error) {
	r := &Room{}
	err := s.pool.QueryRow(ctx, `SELECT id, name, owner_user_id, created_at_unix_ms FROM rooms WHERE id=$1`, id).
		Scan(&r.ID, &r.Name, &r.OwnerUserID, &r.CreatedAtUnixMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

// AddMember idempotently adds a member (used by JoinRoom).
func (s *Store) AddMember(ctx context.Context, roomID, userID, role string, nowUnixMS int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO room_members (room_id, user_id, role, joined_at_unix_ms)
		VALUES ($1,$2,$3,$4) ON CONFLICT (room_id, user_id) DO NOTHING
	`, roomID, userID, role, nowUnixMS)
	return err
}

func (s *Store) RemoveMember(ctx context.Context, roomID, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM room_members WHERE room_id=$1 AND user_id=$2`, roomID, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) GetMembership(ctx context.Context, roomID, userID string) (*Membership, error) {
	m := &Membership{}
	err := s.pool.QueryRow(ctx, `
		SELECT room_id, user_id, role, joined_at_unix_ms FROM room_members WHERE room_id=$1 AND user_id=$2
	`, roomID, userID).Scan(&m.RoomID, &m.UserID, &m.Role, &m.JoinedAtUnixMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return m, err
}

func (s *Store) IsMember(ctx context.Context, roomID, userID string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM room_members WHERE room_id=$1 AND user_id=$2`, roomID, userID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) ListMembers(ctx context.Context, roomID string) ([]*Membership, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT room_id, user_id, role, joined_at_unix_ms FROM room_members
		WHERE room_id=$1 ORDER BY joined_at_unix_ms
	`, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Membership
	for rows.Next() {
		m := &Membership{}
		if err := rows.Scan(&m.RoomID, &m.UserID, &m.Role, &m.JoinedAtUnixMS); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ListRoomsForUser(ctx context.Context, userID string) ([]*Room, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.name, r.owner_user_id, r.created_at_unix_ms
		FROM rooms r JOIN room_members m ON m.room_id = r.id
		WHERE m.user_id=$1 ORDER BY r.created_at_unix_ms DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Room
	for rows.Next() {
		r := &Room{}
		if err := rows.Scan(&r.ID, &r.Name, &r.OwnerUserID, &r.CreatedAtUnixMS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
