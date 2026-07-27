// Package server implements the HTTP handlers for identity-svc.
//
// URL convention follows Connect-RPC: /aurora.identity.v1.IdentityService/{MethodName}.
// Content-Type: application/json. M0 does not yet handle protobuf binary; M1 will.
//
// All handlers:
//   - Read JSON from request body
//   - Validate inputs
//   - Call store layer
//   - Write JSON response with appropriate HTTP status
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

	"github.com/dazn/aurora/services/identity-svc/internal/events"
	"github.com/dazn/aurora/services/identity-svc/internal/store"
)

// UserStore is the M0/M1 user-persistence surface.
type UserStore interface {
	CreateUser(ctx context.Context, email, displayName, kycCountry string) (*store.User, error)
	GetUser(ctx context.Context, id string) (*store.User, error)
	PingDB(ctx context.Context) error
	CountUsers(ctx context.Context) (int, error)
}

// AuthStore is the M2 credential/device/session-persistence surface.
type AuthStore interface {
	SetPassword(ctx context.Context, userID, hash string, updatedAtUnixMS int64) error
	GetPasswordHash(ctx context.Context, userID string) (string, error)
	GetUserByEmail(ctx context.Context, email string) (*store.User, error)
	DeleteUser(ctx context.Context, userID string) (bool, error)
	UpsertDevice(ctx context.Context, userID, platform, userAgent string, nowUnixMS int64) (*store.Device, error)
	ListDevices(ctx context.Context, userID string) ([]*store.Device, error)
	CreateSession(ctx context.Context, sess *store.Session) error
	GetSessionByRefreshHash(ctx context.Context, hash string) (*store.Session, error)
	RotateSessionRefresh(ctx context.Context, sessionID, oldHash, newHash string, lastUsedUnixMS int64) error
	RevokeSession(ctx context.Context, sessionID string, revokedAtUnixMS int64) error
}

// Store is the full persistence surface handlers depend on. *store.Store
// implements it in production; tests supply an in-memory fake so handler and
// error-mapping logic can be unit-tested under plain `go test` (no DB).
type Store interface {
	UserStore
	AuthStore
}

// authConfig holds JWT signing material and token lifetimes.
type authConfig struct {
	secret     []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

type Server struct {
	store     Store
	publisher events.Publisher
	auth      authConfig
	version   string
}

// Option configures a Server (functional options keep New backward-compatible).
type Option func(*Server)

// WithPublisher wires an event publisher. Defaults to events.Nop when unset.
func WithPublisher(p events.Publisher) Option {
	return func(s *Server) {
		if p != nil {
			s.publisher = p
		}
	}
}

// WithAuth configures JWT signing and token TTLs (M2).
func WithAuth(secret []byte, accessTTL, refreshTTL time.Duration) Option {
	return func(s *Server) {
		s.auth = authConfig{secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}
	}
}

func New(st Store, version string, opts ...Option) *Server {
	s := &Server{store: st, version: version, publisher: events.Nop{}}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/CreateUser", s.handleCreateUser)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/GetUser", s.handleGetUser)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/HealthCheck", s.handleHealthCheck)
	// Also allow GET for HealthCheck for easy curl-ing
	mux.HandleFunc("GET /aurora.identity.v1.IdentityService/HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /healthz", s.handleHealthCheck)
	// M2 auth / session / device endpoints.
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/Login", s.handleLogin)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/RefreshToken", s.handleRefreshToken)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/Logout", s.handleLogout)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/GetMe", s.handleGetMe)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/ListDevices", s.handleListDevices)
	mux.HandleFunc("POST /aurora.identity.v1.IdentityService/DeleteUser", s.handleDeleteUser)
	return logMiddleware(mux)
}

// ---------- Request / Response types (mirror proto messages) ----------

type createUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	KYCCountry  string `json:"kyc_country"`
	// M2: optional. If set, a credential is created so the user can Login.
	// Omitted => no credential (preserves M0/M1 behavior).
	Password string `json:"password,omitempty"`
}

type userJSON struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	DisplayName     string `json:"display_name"`
	KYCCountry      string `json:"kyc_country"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

type createUserResponse struct {
	User userJSON `json:"user"`
}

type getUserRequest struct {
	ID string `json:"id"`
}

type getUserResponse struct {
	User userJSON `json:"user"`
}

type healthCheckResponse struct {
	Status  string            `json:"status"`
	Version string            `json:"version"`
	Details map[string]string `json:"details"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------- Handlers ----------

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	// Trim & basic validation
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	req.KYCCountry = strings.ToUpper(strings.TrimSpace(req.KYCCountry))

	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeError(w, http.StatusBadRequest, "invalid_email", "email must contain @")
		return
	}
	if req.DisplayName == "" {
		writeError(w, http.StatusBadRequest, "invalid_display_name", "display_name required")
		return
	}
	if len(req.KYCCountry) != 2 {
		writeError(w, http.StatusBadRequest, "invalid_country", "kyc_country must be ISO 3166-1 alpha-2")
		return
	}
	// bcrypt silently truncates at 72 bytes, so cap there: a >72-byte password
	// would have its tail contribute no entropy.
	if req.Password != "" && (len(req.Password) < 8 || len(req.Password) > 72) {
		writeError(w, http.StatusBadRequest, "invalid_password", "password must be 8-72 characters")
		return
	}

	u, err := s.store.CreateUser(ctx, req.Email, req.DisplayName, req.KYCCountry)
	if err != nil {
		if errors.Is(err, store.ErrEmailExists) {
			writeError(w, http.StatusConflict, "email_exists", "email already registered")
			return
		}
		slog.Error("createUser failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create user")
		return
	}

	// M2: if a password was supplied, store its bcrypt hash so the user can Login.
	if req.Password != "" {
		if err := s.setUserPassword(ctx, u.ID, req.Password); err != nil {
			slog.Error("set password failed", "user_id", u.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "user created but password not set")
			return
		}
	}

	// M1 Event Mesh: publish user.lifecycle.created.v1 (dual-write). The user is
	// already persisted, so a publish failure is logged but does NOT fail the
	// request (no outbox yet — see ADR-0002 §5). Uses a detached context with its
	// own timeout so a client disconnect doesn't drop the event.
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 2*time.Second)
	pubErr := s.publisher.UserCreated(pubCtx, events.UserCreated{
		UserID:          u.ID,
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		KYCCountry:      u.KYCCountry,
		CreatedAtUnixMS: u.CreatedAtUnixMS,
	})
	pubCancel()
	if pubErr != nil {
		slog.Warn("failed to publish user.created event", "user_id", u.ID, "error", pubErr)
	}

	writeJSON(w, http.StatusOK, createUserResponse{User: userToJSON(u)})
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req getUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "invalid_id", "id required")
		return
	}
	// The id column is UUID; a non-UUID value would make Postgres raise a 22P02
	// error that we'd otherwise surface as a misleading 500. Reject up front.
	if _, err := uuid.Parse(req.ID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
		return
	}

	u, err := s.store.GetUser(ctx, req.ID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		slog.Error("getUser failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not fetch user")
		return
	}

	writeJSON(w, http.StatusOK, getUserResponse{User: userToJSON(u)})
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	status := "ok"
	httpStatus := http.StatusOK
	details := map[string]string{}

	if err := s.store.PingDB(ctx); err != nil {
		// Log the real error (pgx messages can embed DSN host/port/user); return
		// only a generic reason to the client to avoid leaking internal topology.
		slog.Error("healthcheck db ping failed", "error", err)
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
		details["db"] = "unreachable"
	} else {
		details["db"] = "ok"
		if n, err := s.store.CountUsers(ctx); err == nil {
			details["users_count"] = intToStr(n)
		}
	}

	// Return non-2xx when degraded so the Docker/compose healthcheck and
	// wait_healthy.sh (which judge readiness by HTTP status) actually treat a
	// dead DB as unhealthy instead of reporting the container green.
	writeJSON(w, httpStatus, healthCheckResponse{
		Status:  status,
		Version: s.version,
		Details: details,
	})
}

// ---------- Helpers ----------

func userToJSON(u *store.User) userJSON {
	return userJSON{
		ID:              u.ID,
		Email:           u.Email,
		DisplayName:     u.DisplayName,
		KYCCountry:      u.KYCCountry,
		CreatedAtUnixMS: u.CreatedAtUnixMS,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorResponse{Code: code, Message: msg})
}

func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 12)
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// logMiddleware logs every request with method, path, status, and duration.
func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		slog.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
