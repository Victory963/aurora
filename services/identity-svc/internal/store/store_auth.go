// M2 auth persistence: credentials, devices, sessions.
//
// Kept in the same `store` package as users (one DB), separate file for clarity.
// All timestamps are supplied by the caller (the server) so this layer stays
// time-free and easy to unit-test; it is pure persistence.
package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Device is a (user, platform, user-agent) the user has logged in from.
type Device struct {
	ID              string
	UserID          string
	Platform        string
	UserAgent       string
	CreatedAtUnixMS int64
	LastSeenUnixMS  int64
}

// Session is a refresh-token-backed login session.
type Session struct {
	ID               string
	UserID           string
	DeviceID         string
	RefreshTokenHash string
	CreatedAtUnixMS  int64
	ExpiresAtUnixMS  int64
	LastUsedAtUnixMS int64
	RevokedAtUnixMS  int64 // 0 = active
}

// migrateAuth creates the M2 tables. Called from Migrate. Children cascade-delete
// with the user so DeleteUser cleans everything up.
func (s *Store) migrateAuth(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS user_credentials (
			user_id            UUID PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			password_hash      TEXT NOT NULL,
			updated_at_unix_ms BIGINT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS devices (
			id                 UUID PRIMARY KEY,
			user_id            UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			platform           TEXT NOT NULL,
			user_agent         TEXT NOT NULL DEFAULT '',
			created_at_unix_ms BIGINT NOT NULL,
			last_seen_unix_ms  BIGINT NOT NULL,
			UNIQUE (user_id, platform, user_agent)
		);
		CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id);
		CREATE TABLE IF NOT EXISTS sessions (
			id                   UUID PRIMARY KEY,
			user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			device_id            UUID NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
			refresh_token_hash   TEXT NOT NULL UNIQUE,
			created_at_unix_ms   BIGINT NOT NULL,
			expires_at_unix_ms   BIGINT NOT NULL,
			last_used_at_unix_ms BIGINT NOT NULL,
			revoked_at_unix_ms   BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);
	`)
	return err
}

// ---------- credentials ----------

// SetPassword upserts the user's bcrypt password hash.
func (s *Store) SetPassword(ctx context.Context, userID, hash string, updatedAtUnixMS int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash, updated_at_unix_ms)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
			SET password_hash = EXCLUDED.password_hash,
			    updated_at_unix_ms = EXCLUDED.updated_at_unix_ms
	`, userID, hash, updatedAtUnixMS)
	return err
}

// GetPasswordHash returns the stored bcrypt hash, or ErrNotFound if the user has
// no credential set.
func (s *Store) GetPasswordHash(ctx context.Context, userID string) (string, error) {
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT password_hash FROM user_credentials WHERE user_id = $1`, userID).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	return hash, nil
}

// GetUserByEmail fetches a user by (already-normalized lowercase) email.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, email, display_name, kyc_country, created_at_unix_ms
		FROM users WHERE email = $1
	`, email)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.KYCCountry, &u.CreatedAtUnixMS); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return u, nil
}

// DeleteUser removes the user row; children (credentials, devices, sessions)
// cascade. Returns whether a row existed.
func (s *Store) DeleteUser(ctx context.Context, userID string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ---------- devices ----------

// UpsertDevice inserts or refreshes a device keyed by (user, platform, user_agent).
func (s *Store) UpsertDevice(ctx context.Context, userID, platform, userAgent string, nowUnixMS int64) (*Device, error) {
	d := &Device{}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO devices (id, user_id, platform, user_agent, created_at_unix_ms, last_seen_unix_ms)
		VALUES ($1, $2, $3, $4, $5, $5)
		ON CONFLICT (user_id, platform, user_agent) DO UPDATE
			SET last_seen_unix_ms = EXCLUDED.last_seen_unix_ms
		RETURNING id, user_id, platform, user_agent, created_at_unix_ms, last_seen_unix_ms
	`, uuid.NewString(), userID, platform, userAgent, nowUnixMS).
		Scan(&d.ID, &d.UserID, &d.Platform, &d.UserAgent, &d.CreatedAtUnixMS, &d.LastSeenUnixMS)
	if err != nil {
		return nil, err
	}
	return d, nil
}

// ListDevices returns the user's devices, most-recently-seen first.
func (s *Store) ListDevices(ctx context.Context, userID string) ([]*Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, platform, user_agent, created_at_unix_ms, last_seen_unix_ms
		FROM devices WHERE user_id = $1
		ORDER BY last_seen_unix_ms DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*Device
	for rows.Next() {
		d := &Device{}
		if err := rows.Scan(&d.ID, &d.UserID, &d.Platform, &d.UserAgent, &d.CreatedAtUnixMS, &d.LastSeenUnixMS); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- sessions ----------

// CreateSession inserts a new session. The caller fills all fields/timestamps.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions
			(id, user_id, device_id, refresh_token_hash, created_at_unix_ms, expires_at_unix_ms, last_used_at_unix_ms, revoked_at_unix_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, sess.ID, sess.UserID, sess.DeviceID, sess.RefreshTokenHash,
		sess.CreatedAtUnixMS, sess.ExpiresAtUnixMS, sess.LastUsedAtUnixMS, sess.RevokedAtUnixMS)
	return err
}

// GetSessionByRefreshHash looks up a session by refresh-token hash.
func (s *Store) GetSessionByRefreshHash(ctx context.Context, hash string) (*Session, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, device_id, refresh_token_hash, created_at_unix_ms,
		       expires_at_unix_ms, last_used_at_unix_ms, revoked_at_unix_ms
		FROM sessions WHERE refresh_token_hash = $1
	`, hash)
	sess := &Session{}
	err := row.Scan(&sess.ID, &sess.UserID, &sess.DeviceID, &sess.RefreshTokenHash,
		&sess.CreatedAtUnixMS, &sess.ExpiresAtUnixMS, &sess.LastUsedAtUnixMS, &sess.RevokedAtUnixMS)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return sess, nil
}

// RotateSessionRefresh swaps in a new refresh-token hash for an active session.
// The update is scoped to the presented oldHash so two concurrent refreshes of
// the same (possibly stolen) token cannot both succeed — only the first wins;
// the loser matches 0 rows and gets ErrNotFound (then 401). Also returns
// ErrNotFound if the session is missing or already revoked.
func (s *Store) RotateSessionRefresh(ctx context.Context, sessionID, oldHash, newHash string, lastUsedUnixMS int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET refresh_token_hash = $3, last_used_at_unix_ms = $4
		WHERE id = $1 AND refresh_token_hash = $2 AND revoked_at_unix_ms = 0
	`, sessionID, oldHash, newHash, lastUsedUnixMS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSession marks a session revoked (idempotent). Returns ErrNotFound if no
// such session exists.
func (s *Store) RevokeSession(ctx context.Context, sessionID string, revokedAtUnixMS int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE sessions SET revoked_at_unix_ms = $2 WHERE id = $1
	`, sessionID, revokedAtUnixMS)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
