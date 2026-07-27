// Package server implements graph-svc's HTTP handlers (Connect-RPC URL style).
// Query RPCs require Authorization: Bearer (behavioral data is sensitive);
// responses carry user ids only — PII lives in identity-svc (ADR-0008 §3).
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dazn/aurora/services/graph-svc/internal/auth"
	"github.com/dazn/aurora/services/graph-svc/internal/graph"
)

const (
	defaultCoBettorLimit = 10
	maxCoBettorLimit     = 50
	defaultMinShared     = 2
)

type Server struct {
	store        graph.Store
	jwtSecret    []byte
	version      string
	kafkaEnabled bool
}

func New(store graph.Store, jwtSecret []byte, kafkaEnabled bool, version string) *Server {
	return &Server{store: store, jwtSecret: jwtSecret, kafkaEnabled: kafkaEnabled, version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	p := "/aurora.graph.v1.GraphService/"
	mux.HandleFunc("POST "+p+"CoBettors", s.handleCoBettors)
	mux.HandleFunc("POST "+p+"SybilCandidates", s.handleSybil)
	mux.HandleFunc("POST "+p+"Influence", s.handleInfluence)
	mux.HandleFunc("POST "+p+"GetUserGraph", s.handleUserGraph)
	mux.HandleFunc("POST "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /healthz", s.handleHealthCheck)
	return logMiddleware(mux)
}

// ---------- JSON types (mirror proto) ----------

type userIDRequest struct {
	UserID    string `json:"user_id"`
	Limit     int    `json:"limit"`
	MinShared int    `json:"min_shared"`
}

type coBettorJSON struct {
	UserID              string `json:"user_id"`
	SharedPools         int32  `json:"shared_pools"`
	LastSharedAtUnixMS  int64  `json:"last_shared_at_unix_ms"`
}

type sybilJSON struct {
	UserID                   string `json:"user_id"`
	SharedSameSelectionPools int32  `json:"shared_same_selection_pools"`
	CreatedGapMS             int64  `json:"created_gap_ms"`
}

type betJSON struct {
	PoolID     string `json:"pool_id"`
	RoomID     string `json:"room_id"`
	Selection  string `json:"selection"`
	StakeMinor int64  `json:"stake_minor"`
	AtUnixMS   int64  `json:"at_unix_ms"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------- handlers ----------

func (s *Server) handleCoBettors(w http.ResponseWriter, r *http.Request) {
	req, ok := s.authedUserReq(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()
	limit := req.Limit
	if limit <= 0 {
		limit = defaultCoBettorLimit
	}
	if limit > maxCoBettorLimit {
		limit = maxCoBettorLimit
	}
	rows, err := s.store.CoBettors(ctx, req.UserID, limit)
	if err != nil {
		slog.Error("coBettors failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "graph query failed")
		return
	}
	out := make([]coBettorJSON, 0, len(rows))
	for _, c := range rows {
		out = append(out, coBettorJSON{UserID: c.UserID, SharedPools: c.SharedPools, LastSharedAtUnixMS: c.LastSharedAtUnix})
	}
	writeJSON(w, http.StatusOK, map[string]any{"co_bettors": out})
}

func (s *Server) handleSybil(w http.ResponseWriter, r *http.Request) {
	req, ok := s.authedUserReq(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()
	min := req.MinShared
	if min <= 0 {
		min = defaultMinShared
	}
	rows, err := s.store.SybilCandidates(ctx, req.UserID, min)
	if err != nil {
		slog.Error("sybilCandidates failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "graph query failed")
		return
	}
	out := make([]sybilJSON, 0, len(rows))
	for _, c := range rows {
		out = append(out, sybilJSON{UserID: c.UserID, SharedSameSelectionPools: c.SharedSameSelPool, CreatedGapMS: c.CreatedGapMS})
	}
	writeJSON(w, http.StatusOK, map[string]any{"candidates": out})
}

func (s *Server) handleInfluence(w http.ResponseWriter, r *http.Request) {
	req, ok := s.authedUserReq(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()
	inf, err := s.store.Influence(ctx, req.UserID)
	if err != nil {
		slog.Error("influence failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "graph query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"followers":     inf.Followers,
		"follow_events": inf.FollowEvents,
		"avg_lag_ms":    inf.AvgLagMS,
		"pools_led":     inf.PoolsLed,
	})
}

func (s *Server) handleUserGraph(w http.ResponseWriter, r *http.Request) {
	req, ok := s.authedUserReq(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()
	ug, err := s.store.UserGraph(ctx, req.UserID)
	if err != nil {
		slog.Error("userGraph failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "graph query failed")
		return
	}
	if ug == nil {
		writeError(w, http.StatusNotFound, "not_projected", "user not in graph (yet)")
		return
	}
	bets := make([]betJSON, 0, len(ug.Bets))
	for _, b := range ug.Bets {
		bets = append(bets, betJSON{PoolID: b.PoolID, RoomID: b.RoomID, Selection: b.Selection, StakeMinor: b.StakeMinor, AtUnixMS: b.AtUnixMS})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user_id":            ug.User.ID,
		"kyc_country":        ug.User.KYCCountry,
		"created_at_unix_ms": ug.User.CreatedAtUnixMS,
		"bets":               bets,
		"co_bettor_count":    ug.CoBettors,
	})
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	status, httpStatus := "ok", http.StatusOK
	details := map[string]string{}
	if err := s.store.Ping(ctx); err != nil {
		slog.Error("healthcheck neo4j ping failed", "error", err)
		status, httpStatus = "degraded", http.StatusServiceUnavailable
		details["neo4j"] = "unreachable"
	} else {
		details["neo4j"] = "ok"
	}
	if s.kafkaEnabled {
		details["projection"] = "enabled"
	} else {
		details["projection"] = "disabled (AURORA_GRAPH_KAFKA_BROKERS unset)"
		status = "degraded"
	}
	writeJSON(w, httpStatus, map[string]any{"status": status, "version": s.version, "details": details})
}

// ---------- helpers ----------

// authedUserReq verifies the bearer token, decodes the common request body,
// and BINDS the queried user_id to the caller: behavioral graph data is
// sensitive, so M8 only allows self-queries (analyst/admin roles are an M12
// authorization concern — do not widen this without a policy layer).
func (s *Server) authedUserReq(w http.ResponseWriter, r *http.Request) (*userIDRequest, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return nil, false
	}
	claims, err := auth.Verify(strings.TrimSpace(h[len(prefix):]), s.jwtSecret, time.Now())
	if err != nil || claims.Sub == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return nil, false
	}
	var req userIDRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return nil, false
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "invalid_user", "user_id required")
		return nil, false
	}
	if req.UserID != claims.Sub {
		writeError(w, http.StatusForbidden, "forbidden", "can only query your own graph (analyst access: M12)")
		return nil, false
	}
	return &req, true
}

func ctx5(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorResponse{Code: code, Message: msg})
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
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
