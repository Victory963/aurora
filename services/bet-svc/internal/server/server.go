// Package server implements bet-svc's HTTP handlers (Connect-RPC URL style).
// Bearer JWT on every endpoint except health; room membership enforced via
// room-svc when a pool is room-scoped (ADR-0006).
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

	"github.com/dazn/aurora/services/bet-svc/internal/auth"
	"github.com/dazn/aurora/services/bet-svc/internal/clients"
	"github.com/dazn/aurora/services/bet-svc/internal/events"
	"github.com/dazn/aurora/services/bet-svc/internal/poolmath"
	"github.com/dazn/aurora/services/bet-svc/internal/store"
)

const (
	maxQuestionLen = 300
	maxStakeMinor  = 1_000_000_000_000_000 // wallet per-op cap (1e15)
)

// PoolStore is the persistence surface (implemented by *store.Store; faked in tests).
type PoolStore interface {
	CreatePool(ctx context.Context, p *store.Pool) error
	GetPool(ctx context.Context, id string) (*store.Pool, error)
	ListRoomPools(ctx context.Context, roomID string) ([]*store.Pool, error)
	GetBetByKey(ctx context.Context, key string) (*store.Bet, error)
	InsertBetIfOpen(ctx context.Context, b *store.Bet) (bool, error)
	MarkSettling(ctx context.Context, poolID, winningOptionID string) error
	ListPoolBets(ctx context.Context, poolID string) ([]*store.Bet, error)
	FinalizeSettlement(ctx context.Context, poolID string, results map[string]store.BetOutcome, settledAtUnixMS int64) error
	PingDB(ctx context.Context) error
}

// WalletClient moves money (idempotent by key on the wallet side).
type WalletClient interface {
	Debit(ctx context.Context, userID string, amount int64, key, reason string) error
	Credit(ctx context.Context, userID string, amount int64, key, reason string) error
}

// MemberChecker verifies room membership with the caller's own bearer token.
type MemberChecker interface {
	CheckMember(ctx context.Context, roomID, bearer string) error
}

type Server struct {
	store     PoolStore
	wallet    WalletClient
	rooms     MemberChecker
	publisher events.Publisher
	jwtSecret []byte
	rakeBps   int
	version   string
}

func New(st PoolStore, wallet WalletClient, rooms MemberChecker, pub events.Publisher,
	jwtSecret []byte, rakeBps int, version string) *Server {
	if pub == nil {
		pub = events.Nop{}
	}
	return &Server{store: st, wallet: wallet, rooms: rooms, publisher: pub,
		jwtSecret: jwtSecret, rakeBps: rakeBps, version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	p := "/aurora.bet.v1.BetService/"
	mux.HandleFunc("POST "+p+"CreatePool", s.handleCreatePool)
	mux.HandleFunc("POST "+p+"GetPool", s.handleGetPool)
	mux.HandleFunc("POST "+p+"ListRoomPools", s.handleListRoomPools)
	mux.HandleFunc("POST "+p+"PlaceBet", s.handlePlaceBet)
	mux.HandleFunc("POST "+p+"SettlePool", s.handleSettlePool)
	mux.HandleFunc("POST "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /healthz", s.handleHealthCheck)
	return logMiddleware(mux)
}

// ---------- JSON types (mirror proto) ----------

type optionJSON struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	OddsX100   int64  `json:"odds_x100"`
	StakeMinor int64  `json:"stake_minor"`
	Bettors    int32  `json:"bettors"`
}

type poolJSON struct {
	ID              string       `json:"id"`
	RoomID          string       `json:"room_id"`
	Question        string       `json:"question"`
	Status          string       `json:"status"`
	CreatedBy       string       `json:"created_by"`
	RakeBps         int32        `json:"rake_bps"`
	WinningOptionID string       `json:"winning_option_id"`
	Options         []optionJSON `json:"options"`
	TotalStakeMinor int64        `json:"total_stake_minor"`
	CreatedAtUnixMS int64        `json:"created_at_unix_ms"`
	SettledAtUnixMS int64        `json:"settled_at_unix_ms"`
}

type betJSON struct {
	ID              string `json:"id"`
	PoolID          string `json:"pool_id"`
	OptionID        string `json:"option_id"`
	UserID          string `json:"user_id"`
	StakeMinor      int64  `json:"stake_minor"`
	Status          string `json:"status"`
	PayoutMinor     int64  `json:"payout_minor"`
	Replayed        bool   `json:"replayed"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

type createPoolRequest struct {
	RoomID   string `json:"room_id"`
	Question string `json:"question"`
	Options  []struct {
		Label    string `json:"label"`
		OddsX100 int64  `json:"odds_x100"`
	} `json:"options"`
}

type poolIDRequest struct {
	PoolID string `json:"pool_id"`
}

type listRoomPoolsRequest struct {
	RoomID string `json:"room_id"`
}

type placeBetRequest struct {
	PoolID         string `json:"pool_id"`
	OptionID       string `json:"option_id"`
	StakeMinor     int64  `json:"stake_minor"`
	IdempotencyKey string `json:"idempotency_key"`
}

type settlePoolRequest struct {
	PoolID          string `json:"pool_id"`
	WinningOptionID string `json:"winning_option_id"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------- handlers ----------

func (s *Server) handleCreatePool(w http.ResponseWriter, r *http.Request) {
	userID, bearer, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx10(r)
	defer cancel()

	var req createPoolRequest
	if !decode(w, r, &req) {
		return
	}
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || len(req.Question) > maxQuestionLen {
		writeError(w, http.StatusBadRequest, "invalid_question", "question required (1-300 chars)")
		return
	}
	if len(req.Options) != 2 {
		writeError(w, http.StatusBadRequest, "invalid_options", "exactly 2 options required in M6")
		return
	}
	labels := map[string]bool{}
	for _, o := range req.Options {
		l := strings.TrimSpace(o.Label)
		if l == "" || labels[l] || o.OddsX100 < 101 || o.OddsX100 > 100_000 {
			writeError(w, http.StatusBadRequest, "invalid_options",
				"labels must be non-empty and distinct; odds_x100 in 101..100000")
			return
		}
		labels[l] = true
	}
	if req.RoomID != "" {
		if !s.requireMember(ctx, w, req.RoomID, bearer) {
			return
		}
	}

	pool := &store.Pool{
		ID:              uuid.NewString(),
		RoomID:          req.RoomID,
		Question:        req.Question,
		Status:          store.PoolOpen,
		CreatedBy:       userID,
		RakeBps:         int32(s.rakeBps),
		CreatedAtUnixMS: time.Now().UnixMilli(),
	}
	for _, o := range req.Options {
		pool.Options = append(pool.Options, store.Option{Label: strings.TrimSpace(o.Label), OddsX100: o.OddsX100})
	}
	if err := s.store.CreatePool(ctx, pool); err != nil {
		slog.Error("createPool failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create pool")
		return
	}
	s.respondPool(ctx, w, pool.ID)
}

func (s *Server) handleGetPool(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireUser(w, r); !ok {
		return
	}
	ctx, cancel := ctx10(r)
	defer cancel()
	var req poolIDRequest
	if !decode(w, r, &req) {
		return
	}
	poolID, ok := validPoolID(w, req.PoolID)
	if !ok {
		return
	}
	s.respondPool(ctx, w, poolID)
}

func (s *Server) handleListRoomPools(w http.ResponseWriter, r *http.Request) {
	_, bearer, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx10(r)
	defer cancel()
	var req listRoomPoolsRequest
	if !decode(w, r, &req) {
		return
	}
	if req.RoomID == "" {
		writeError(w, http.StatusBadRequest, "invalid_room", "room_id required")
		return
	}
	if !s.requireMember(ctx, w, req.RoomID, bearer) {
		return
	}
	pools, err := s.store.ListRoomPools(ctx, req.RoomID)
	if err != nil {
		slog.Error("listRoomPools failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list pools")
		return
	}
	out := make([]poolJSON, 0, len(pools))
	for _, p := range pools {
		out = append(out, poolToJSON(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pools": out})
}

func (s *Server) handlePlaceBet(w http.ResponseWriter, r *http.Request) {
	userID, bearer, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx10(r)
	defer cancel()

	var req placeBetRequest
	if !decode(w, r, &req) {
		return
	}
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.PoolID == "" || req.OptionID == "" || req.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "pool_id, option_id, idempotency_key required")
		return
	}
	if _, ok := validPoolID(w, req.PoolID); !ok {
		return
	}
	if req.StakeMinor <= 0 || req.StakeMinor > maxStakeMinor {
		writeError(w, http.StatusBadRequest, "invalid_stake", "stake_minor must be 1..1e15")
		return
	}

	// Idempotent replay FIRST (before any pool-state gate): a bet that was
	// successfully placed must replay identically even after the pool settles.
	if existing, err := s.store.GetBetByKey(ctx, req.IdempotencyKey); err == nil {
		if existing.UserID != userID || existing.PoolID != req.PoolID ||
			existing.OptionID != req.OptionID || existing.StakeMinor != req.StakeMinor {
			writeError(w, http.StatusConflict, "idempotency_conflict", "key reused with different parameters")
			return
		}
		writeJSON(w, http.StatusOK, betToJSON(existing, true))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "internal", "idempotency lookup failed")
		return
	}

	pool, err := s.store.GetPool(ctx, req.PoolID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pool_not_found", "pool not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not load pool")
		return
	}
	validOption := false
	for _, o := range pool.Options {
		if o.ID == req.OptionID {
			validOption = true
			break
		}
	}
	if !validOption {
		writeError(w, http.StatusBadRequest, "invalid_option", "option does not belong to pool")
		return
	}
	if pool.RoomID != "" {
		if !s.requireMember(ctx, w, pool.RoomID, bearer) {
			return
		}
	}
	if pool.Status != store.PoolOpen {
		// No bet row exists for this key, but a PRIOR attempt may have debited
		// and crashed before the insert; that stake would otherwise be orphaned
		// (settlement never sees it). Recover deterministically: replay the
		// idempotent debit (no-op if it already ran; takes fresh money only to
		// refund it below) and void it. Both legs are idempotent, so a retry
		// after any failure converges with no double-charge and no mint.
		s.refundClosedPoolAttempt(ctx, w, userID, req)
		return
	}

	// Money first (wallet is idempotent by this same key): a crash after the
	// debit re-runs safely — the retry replays the debit and inserts the bet.
	if err := s.wallet.Debit(ctx, userID, req.StakeMinor, req.IdempotencyKey, "bet:"+req.PoolID); err != nil {
		switch {
		case errors.Is(err, clients.ErrInsufficientFunds):
			writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "insufficient funds")
		case errors.Is(err, clients.ErrWalletNotFound):
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
		case errors.Is(err, clients.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "key reused with different parameters")
		default:
			slog.Error("wallet debit failed", "error", err)
			writeError(w, http.StatusBadGateway, "wallet_unavailable", "wallet unavailable")
		}
		return
	}

	bet := &store.Bet{
		ID:              uuid.NewString(),
		PoolID:          req.PoolID,
		OptionID:        req.OptionID,
		UserID:          userID,
		StakeMinor:      req.StakeMinor,
		IdempotencyKey:  req.IdempotencyKey,
		Status:          store.BetPlaced,
		CreatedAtUnixMS: time.Now().UnixMilli(),
	}
	inserted, err := s.store.InsertBetIfOpen(ctx, bet)
	if errors.Is(err, store.ErrDuplicateKey) {
		// Lost a concurrent race on the same key: surface the stored bet.
		if existing, err2 := s.store.GetBetByKey(ctx, req.IdempotencyKey); err2 == nil {
			writeJSON(w, http.StatusOK, betToJSON(existing, true))
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "idempotency race")
		return
	}
	if err != nil {
		slog.Error("insert bet failed (debit already taken; retry with same key will replay)", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not record bet — retry with the same idempotency_key")
		return
	}
	if !inserted {
		// Pool closed between the status check and the insert: undo the debit.
		// The void credit must not be fire-and-forget — if it fails, tell the
		// caller to retry (the closed-pool recovery path re-attempts it).
		if err := s.wallet.Credit(ctx, userID, req.StakeMinor, "void-"+req.IdempotencyKey, "bet_void_pool_closed"); err != nil {
			slog.Error("void credit failed; retry with the same key to complete the refund",
				"key", req.IdempotencyKey, "error", err)
			writeError(w, http.StatusBadGateway, "refund_pending",
				"pool closed and the stake refund did not complete — retry with the same idempotency_key")
			return
		}
		writeError(w, http.StatusConflict, "pool_closed", "pool closed while placing bet (stake refunded)")
		return
	}

	// Event (best-effort direct publish; ADR-0006 §4).
	var odds float64
	var label string
	for _, o := range pool.Options {
		if o.ID == req.OptionID {
			odds, label = float64(o.OddsX100)/100.0, o.Label
		}
	}
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 6*time.Second)
	if err := s.publisher.BetPlaced(pubCtx, events.BetPlaced{
		BetID: bet.ID, UserID: userID, MarketID: pool.ID, Selection: label,
		StakeMinor: bet.StakeMinor, Odds: odds, RoomID: pool.RoomID,
	}); err != nil {
		slog.Warn("bet.placed publish failed", "error", err)
	}
	pubCancel()

	writeJSON(w, http.StatusOK, betToJSON(bet, false))
}

// refundClosedPoolAttempt handles PlaceBet against a non-OPEN pool when no bet
// row exists for the idempotency key. A prior attempt may have debited and
// crashed pre-insert; replay the debit (idempotent: a true replay moves no new
// money, a fresh key debits only to be refunded immediately) then void it.
// Requires the wallet to reject same-key/different-amount replays (409), which
// guarantees the void amount below always matches what was actually debited.
func (s *Server) refundClosedPoolAttempt(ctx context.Context, w http.ResponseWriter, userID string, req placeBetRequest) {
	if err := s.wallet.Debit(ctx, userID, req.StakeMinor, req.IdempotencyKey, "bet:"+req.PoolID); err != nil {
		switch {
		case errors.Is(err, clients.ErrInsufficientFunds):
			// A replay never fails on funds, so no prior debit exists: nothing to refund.
			writeError(w, http.StatusConflict, "pool_closed", "pool is not open for bets")
		case errors.Is(err, clients.ErrWalletNotFound):
			writeError(w, http.StatusConflict, "pool_closed", "pool is not open for bets")
		case errors.Is(err, clients.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "key reused with different parameters")
		default:
			slog.Error("closed-pool debit reconcile failed", "key", req.IdempotencyKey, "error", err)
			writeError(w, http.StatusBadGateway, "wallet_unavailable", "wallet unavailable — retry with the same idempotency_key")
		}
		return
	}
	if err := s.wallet.Credit(ctx, userID, req.StakeMinor, "void-"+req.IdempotencyKey, "bet_void_pool_closed"); err != nil {
		slog.Error("closed-pool void credit failed; retry with the same key", "key", req.IdempotencyKey, "error", err)
		writeError(w, http.StatusBadGateway, "refund_pending",
			"pool closed and the stake refund did not complete — retry with the same idempotency_key")
		return
	}
	writeError(w, http.StatusConflict, "pool_closed", "pool is not open for bets (any prior stake for this key was refunded)")
}

func (s *Server) handleSettlePool(w http.ResponseWriter, r *http.Request) {
	userID, _, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var req settlePoolRequest
	if !decode(w, r, &req) {
		return
	}
	if _, ok := validPoolID(w, req.PoolID); !ok {
		return
	}
	pool, err := s.store.GetPool(ctx, req.PoolID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pool_not_found", "pool not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not load pool")
		return
	}
	if pool.CreatedBy != userID {
		writeError(w, http.StatusForbidden, "forbidden", "only the pool creator can settle (oracle: M9/M13)")
		return
	}
	if pool.Status == store.PoolSettled {
		if pool.WinningOptionID != req.WinningOptionID {
			writeError(w, http.StatusConflict, "already_settled", "pool settled with a different outcome")
			return
		}
		writeJSON(w, http.StatusOK, poolToJSON(pool)) // idempotent replay
		return
	}
	valid := false
	for _, o := range pool.Options {
		if o.ID == req.WinningOptionID {
			valid = true
		}
	}
	if !valid {
		writeError(w, http.StatusBadRequest, "invalid_option", "winning_option_id does not belong to pool")
		return
	}

	if err := s.store.MarkSettling(ctx, pool.ID, req.WinningOptionID); err != nil {
		if errors.Is(err, store.ErrBadTransition) {
			writeError(w, http.StatusConflict, "settling_conflict", "pool is settling with a different outcome")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "could not start settlement")
		return
	}

	bets, err := s.store.ListPoolBets(ctx, pool.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not load bets")
		return
	}
	in := make([]poolmath.BetIn, 0, len(bets))
	for _, b := range bets {
		in = append(in, poolmath.BetIn{ID: b.ID, OptionID: b.OptionID, UserID: b.UserID, StakeMinor: b.StakeMinor})
	}
	outcome := poolmath.Compute(in, req.WinningOptionID, int(pool.RakeBps))

	// Idempotent credits BEFORE finalizing: a crash mid-way re-runs Settle and
	// the wallet replays each key without double-paying (ADR-0006 §2).
	byID := map[string]*store.Bet{}
	for _, b := range bets {
		byID[b.ID] = b
	}
	results := make(map[string]store.BetOutcome, len(outcome.Results))
	for betID, res := range outcome.Results {
		results[betID] = store.BetOutcome{Status: res.Status, PayoutMinor: res.PayoutMinor}
		if res.PayoutMinor <= 0 {
			continue
		}
		prefix := "settle-"
		reason := "pool_settlement:" + pool.ID
		if res.Status == poolmath.StatusRefunded {
			prefix, reason = "refund-", "pool_refund:"+pool.ID
		}
		if err := s.wallet.Credit(ctx, byID[betID].UserID, res.PayoutMinor, prefix+betID, reason); err != nil {
			slog.Error("settlement credit failed; re-run SettlePool to resume", "bet", betID, "error", err)
			writeError(w, http.StatusBadGateway, "wallet_unavailable",
				"settlement interrupted — call SettlePool again with the same outcome to resume")
			return
		}
	}

	if err := s.store.FinalizeSettlement(ctx, pool.ID, results, time.Now().UnixMilli()); err != nil {
		slog.Error("finalize settlement failed; re-run SettlePool to resume", "error", err)
		writeError(w, http.StatusInternalServerError, "internal",
			"settlement finalize failed — call SettlePool again with the same outcome to resume")
		return
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 6*time.Second)
	winning := req.WinningOptionID
	if outcome.Refunded {
		winning = ""
	}
	if err := s.publisher.PoolSettled(pubCtx, events.PoolSettled{
		PoolID: pool.ID, RoomID: pool.RoomID, WinningOptionID: winning,
		TotalStakeMinor: outcome.TotalStakeMinor, RakeMinor: outcome.RakeMinor,
		Winners: outcome.Winners, Refunded: outcome.Refunded,
	}); err != nil {
		slog.Warn("pool.settled publish failed", "error", err)
	}
	pubCancel()

	s.respondPool(ctx, w, pool.ID)
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	status, httpStatus := "ok", http.StatusOK
	details := map[string]string{}
	if err := s.store.PingDB(ctx); err != nil {
		slog.Error("healthcheck db ping failed", "error", err)
		status, httpStatus = "degraded", http.StatusServiceUnavailable
		details["db"] = "unreachable"
	} else {
		details["db"] = "ok"
	}
	writeJSON(w, httpStatus, map[string]any{"status": status, "version": s.version, "details": details})
}

// ---------- helpers ----------

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (userID, bearer string, ok bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", "", false
	}
	token := strings.TrimSpace(h[len(prefix):])
	claims, err := auth.Verify(token, s.jwtSecret, time.Now())
	if err != nil || claims.Sub == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", "", false
	}
	return claims.Sub, token, true
}

func (s *Server) requireMember(ctx context.Context, w http.ResponseWriter, roomID, bearer string) bool {
	err := s.rooms.CheckMember(ctx, roomID, bearer)
	switch {
	case err == nil:
		return true
	case errors.Is(err, clients.ErrNotMember):
		writeError(w, http.StatusForbidden, "forbidden", "not a member of this room")
		return false
	default:
		slog.Error("membership check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "room_unavailable", "room service unavailable")
		return false
	}
}

// validPoolID rejects non-UUID ids up front (the pools.id column is UUID; a
// bad id would otherwise surface as a Postgres 22P02 => misleading 500).
func validPoolID(w http.ResponseWriter, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if _, err := uuid.Parse(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_pool", "pool_id must be a UUID")
		return "", false
	}
	return raw, true
}

func (s *Server) respondPool(ctx context.Context, w http.ResponseWriter, id string) {
	p, err := s.store.GetPool(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "pool_not_found", "pool not found")
			return
		}
		slog.Error("getPool failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not fetch pool")
		return
	}
	writeJSON(w, http.StatusOK, poolToJSON(p))
}

func poolToJSON(p *store.Pool) poolJSON {
	out := poolJSON{
		ID: p.ID, RoomID: p.RoomID, Question: p.Question, Status: p.Status,
		CreatedBy: p.CreatedBy, RakeBps: p.RakeBps, WinningOptionID: p.WinningOptionID,
		TotalStakeMinor: p.TotalStakeMinor, CreatedAtUnixMS: p.CreatedAtUnixMS,
		SettledAtUnixMS: p.SettledAtUnixMS, Options: []optionJSON{},
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, optionJSON{
			ID: o.ID, Label: o.Label, OddsX100: o.OddsX100, StakeMinor: o.StakeMinor, Bettors: o.Bettors,
		})
	}
	return out
}

func betToJSON(b *store.Bet, replayed bool) betJSON {
	return betJSON{
		ID: b.ID, PoolID: b.PoolID, OptionID: b.OptionID, UserID: b.UserID,
		StakeMinor: b.StakeMinor, Status: b.Status, PayoutMinor: b.PayoutMinor,
		Replayed: replayed, CreatedAtUnixMS: b.CreatedAtUnixMS,
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

func ctx10(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
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
