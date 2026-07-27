// Package store handles persistence for identity-svc.
//
// M0 scope: simple users table, single-region.
// Future:
//   - M2: add devices, sessions tables
//   - M8: emit user events for Neo4j projection
//   - Multi-region: switch to Citus or per-region partitioning
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

// ErrNotFound is returned when a query returns no rows.
var ErrNotFound = errors.New("not found")

// ErrEmailExists is returned when CreateUser sees a duplicate email.
var ErrEmailExists = errors.New("email already exists")

type User struct {
	ID              string
	Email           string
	DisplayName     string
	KYCCountry      string
	CreatedAtUnixMS int64
}

type Store struct {
	pool *pgxpool.Pool
}

// NewPostgres connects to Postgres, retrying for up to 30s while the container starts.
func NewPostgres(ctx context.Context, dsn string) (*Store, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err := pool.Ping(ctx); err == nil {
				return &Store{pool: pool}, nil
			} else {
				pool.Close()
				lastErr = err
			}
		} else {
			lastErr = err
		}
		time.Sleep(1 * time.Second)
	}
	return nil, fmt.Errorf("postgres unreachable after 30s: %w", lastErr)
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Migrate creates the schema if it does not exist (users + M2 auth tables).
// In a later milestone we'll switch to a real migration tool (goose / dbmate);
// for now idempotent CREATE TABLEs are enough.
func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS users (
			id                  UUID PRIMARY KEY,
			email               TEXT NOT NULL UNIQUE,
			display_name        TEXT NOT NULL,
			kyc_country         VARCHAR(2) NOT NULL,
			created_at_unix_ms  BIGINT NOT NULL
		);
		-- email is already indexed by its UNIQUE constraint; no separate index.
		CREATE INDEX IF NOT EXISTS idx_users_kyc_country ON users(kyc_country);
	`); err != nil {
		return err
	}
	return s.migrateAuth(ctx)
}

// CreateUser inserts a new user. ID is generated server-side.
func (s *Store) CreateUser(ctx context.Context, email, displayName, kycCountry string) (*User, error) {
	u := &User{
		ID:              uuid.NewString(),
		Email:           email,
		DisplayName:     displayName,
		KYCCountry:      kycCountry,
		CreatedAtUnixMS: time.Now().UnixMilli(),
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (id, email, display_name, kyc_country, created_at_unix_ms)
		VALUES ($1, $2, $3, $4, $5)
	`, u.ID, u.Email, u.DisplayName, u.KYCCountry, u.CreatedAtUnixMS)
	if err != nil {
		if isDuplicateKey(err) {
			return nil, ErrEmailExists
		}
		return nil, err
	}
	return u, nil
}

// GetUser fetches a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, kyc_country, created_at_unix_ms
		FROM users
		WHERE id = $1
	`, id)
	u := &User{}
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.KYCCountry, &u.CreatedAtUnixMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// CountUsers returns total users (used by health check / smoke tests).
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// PingDB checks connectivity.
func (s *Store) PingDB(ctx context.Context) error {
	return s.pool.Ping(ctx)
}

// TruncateUsers wipes all users. ONLY for test use. Not exported in M1+ (move to test helper).
func (s *Store) TruncateUsers(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, "TRUNCATE TABLE users")
	return err
}

// isDuplicateKey reports whether err is a Postgres unique-violation (SQLSTATE
// 23505), using the typed pgconn.PgError code rather than string matching.
func isDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" // unique_violation
	}
	return false
}
