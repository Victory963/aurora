// M2 auth handlers: Login, RefreshToken, Logout, GetMe, ListDevices, DeleteUser.
// See docs/adr/0003-sessions-jwt.md. Authenticated endpoints read a bearer access
// token from the Authorization header.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dazn/aurora/services/identity-svc/internal/auth"
	"github.com/dazn/aurora/services/identity-svc/internal/events"
	"github.com/dazn/aurora/services/identity-svc/internal/store"
)

// ---------- JSON types (mirror proto messages) ----------

type deviceInfo struct {
	Platform  string `json:"platform"`
	UserAgent string `json:"user_agent"`
}

type loginRequest struct {
	Email    string     `json:"email"`
	Password string     `json:"password"`
	Device   deviceInfo `json:"device"`
}

type tokenBundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"` // access-token TTL in seconds
	SessionID    string `json:"session_id"`
}

type loginResponse struct {
	tokenBundle
	User userJSON `json:"user"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type logoutResponse struct {
	OK bool `json:"ok"`
}

type deviceJSON struct {
	ID              string `json:"id"`
	Platform        string `json:"platform"`
	UserAgent       string `json:"user_agent"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
	LastSeenUnixMS  int64  `json:"last_seen_unix_ms"`
}

type listDevicesResponse struct {
	Devices []deviceJSON `json:"devices"`
}

type deleteUserResponse struct {
	Deleted bool `json:"deleted"`
}

// ---------- Handlers ----------

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "email and password required")
		return
	}

	// Generic invalid_credentials for both unknown email and wrong password to
	// avoid user enumeration.
	u, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.CompareDummy(req.Password) // equalize timing vs. the real bcrypt path
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
			return
		}
		slog.Error("login: lookup user failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	hash, err := s.store.GetPasswordHash(ctx, u.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			auth.CompareDummy(req.Password) // equalize timing vs. the real bcrypt path
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
			return
		}
		slog.Error("login: get password failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	if !auth.ComparePassword(hash, req.Password) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid email or password")
		return
	}

	now := time.Now()
	platform := strings.TrimSpace(req.Device.Platform)
	if platform == "" {
		platform = "unknown"
	}
	dev, err := s.store.UpsertDevice(ctx, u.ID, platform, req.Device.UserAgent, now.UnixMilli())
	if err != nil {
		slog.Error("login: upsert device failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}

	bundle, err := s.issueSession(ctx, u, dev.ID, now)
	if err != nil {
		slog.Error("login: issue session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "login failed")
		return
	}
	writeJSON(w, http.StatusOK, loginResponse{tokenBundle: bundle, User: userToJSON(u)})
}

func (s *Server) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req refreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}

	oldHash := auth.HashToken(req.RefreshToken)
	sess, err := s.store.GetSessionByRefreshHash(ctx, oldHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid_token", "invalid or rotated refresh token")
			return
		}
		slog.Error("refresh: lookup session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "refresh failed")
		return
	}

	now := time.Now()
	if sess.RevokedAtUnixMS != 0 || now.UnixMilli() >= sess.ExpiresAtUnixMS {
		writeError(w, http.StatusUnauthorized, "invalid_token", "session revoked or expired")
		return
	}

	u, err := s.store.GetUser(ctx, sess.UserID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusUnauthorized, "invalid_token", "user no longer exists")
			return
		}
		slog.Error("refresh: get user failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "refresh failed")
		return
	}

	newRefresh, newHash, err := auth.NewRefreshToken()
	if err != nil {
		slog.Error("refresh: mint token failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "refresh failed")
		return
	}
	if err := s.store.RotateSessionRefresh(ctx, sess.ID, oldHash, newHash, now.UnixMilli()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Concurrently revoked between read and rotate.
			writeError(w, http.StatusUnauthorized, "invalid_token", "session revoked")
			return
		}
		slog.Error("refresh: rotate failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "refresh failed")
		return
	}

	access, err := s.signAccess(u, sess.ID, now)
	if err != nil {
		slog.Error("refresh: sign access failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "refresh failed")
		return
	}
	writeJSON(w, http.StatusOK, tokenBundle{
		AccessToken:  access,
		RefreshToken: newRefresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.auth.accessTTL.Seconds()),
		SessionID:    sess.ID,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req logoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.RefreshToken) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}

	sess, err := s.store.GetSessionByRefreshHash(ctx, auth.HashToken(req.RefreshToken))
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Already gone — logout is idempotent.
	case err != nil:
		slog.Error("logout: lookup session failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "logout failed")
		return
	default:
		if err := s.store.RevokeSession(ctx, sess.ID, time.Now().UnixMilli()); err != nil && !errors.Is(err, store.ErrNotFound) {
			slog.Error("logout: revoke failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "logout failed")
			return
		}
	}
	writeJSON(w, http.StatusOK, logoutResponse{OK: true})
}

func (s *Server) handleGetMe(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	u, err := s.store.GetUser(ctx, claims.Sub)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		slog.Error("getMe failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not fetch user")
		return
	}
	writeJSON(w, http.StatusOK, getUserResponse{User: userToJSON(u)})
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	devs, err := s.store.ListDevices(ctx, claims.Sub)
	if err != nil {
		slog.Error("listDevices failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list devices")
		return
	}
	out := make([]deviceJSON, 0, len(devs))
	for _, d := range devs {
		out = append(out, deviceJSON{
			ID:              d.ID,
			Platform:        d.Platform,
			UserAgent:       d.UserAgent,
			CreatedAtUnixMS: d.CreatedAtUnixMS,
			LastSeenUnixMS:  d.LastSeenUnixMS,
		})
	}
	writeJSON(w, http.StatusOK, listDevicesResponse{Devices: out})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	claims, ok := s.requireAuth(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	deleted, err := s.store.DeleteUser(ctx, claims.Sub)
	if err != nil {
		slog.Error("deleteUser failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete user")
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	// M1 Event Mesh: publish user.lifecycle.deleted.v1 (non-fatal, detached ctx).
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
	pubErr := s.publisher.UserDeleted(pubCtx, events.UserDeleted{UserID: claims.Sub, Reason: "self_service"})
	pubCancel()
	if pubErr != nil {
		slog.Warn("failed to publish user.deleted event", "user_id", claims.Sub, "error", pubErr)
	}

	writeJSON(w, http.StatusOK, deleteUserResponse{Deleted: true})
}

// ---------- Helpers ----------

// setUserPassword hashes plain and stores the credential. Used by CreateUser.
func (s *Server) setUserPassword(ctx context.Context, userID, plain string) error {
	hash, err := auth.HashPassword(plain)
	if err != nil {
		return err
	}
	return s.store.SetPassword(ctx, userID, hash, time.Now().UnixMilli())
}

// issueSession mints a device-bound session + access/refresh tokens.
func (s *Server) issueSession(ctx context.Context, u *store.User, deviceID string, now time.Time) (tokenBundle, error) {
	refresh, refreshHash, err := auth.NewRefreshToken()
	if err != nil {
		return tokenBundle{}, err
	}
	sess := &store.Session{
		ID:               uuid.NewString(),
		UserID:           u.ID,
		DeviceID:         deviceID,
		RefreshTokenHash: refreshHash,
		CreatedAtUnixMS:  now.UnixMilli(),
		ExpiresAtUnixMS:  now.Add(s.auth.refreshTTL).UnixMilli(),
		LastUsedAtUnixMS: now.UnixMilli(),
		RevokedAtUnixMS:  0,
	}
	if err := s.store.CreateSession(ctx, sess); err != nil {
		return tokenBundle{}, err
	}
	access, err := s.signAccess(u, sess.ID, now)
	if err != nil {
		return tokenBundle{}, err
	}
	return tokenBundle{
		AccessToken:  access,
		RefreshToken: refresh,
		TokenType:    "Bearer",
		ExpiresIn:    int64(s.auth.accessTTL.Seconds()),
		SessionID:    sess.ID,
	}, nil
}

func (s *Server) signAccess(u *store.User, sessionID string, now time.Time) (string, error) {
	return auth.Sign(auth.Claims{
		Sub:     u.ID,
		Sid:     sessionID,
		Iss:     auth.Issuer,
		Iat:     now.Unix(),
		Exp:     now.Add(s.auth.accessTTL).Unix(),
		Country: u.KYCCountry,
	}, s.auth.secret)
}

// bearerClaims extracts and verifies the Authorization: Bearer access token.
func (s *Server) bearerClaims(r *http.Request) (*auth.Claims, error) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return nil, auth.ErrInvalidToken
	}
	return auth.Verify(strings.TrimSpace(h[len(prefix):]), s.auth.secret, time.Now())
}

// requireAuth writes a 401 and returns ok=false when the request is not
// authenticated; otherwise returns the verified claims.
func (s *Server) requireAuth(w http.ResponseWriter, r *http.Request) (*auth.Claims, bool) {
	c, err := s.bearerClaims(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return nil, false
	}
	return c, true
}
