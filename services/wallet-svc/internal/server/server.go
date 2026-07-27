// Package server implements wallet-svc's HTTP handlers (Connect-RPC URL style).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/dazn/aurora/services/wallet-svc/internal/store"
)

// WalletStore is the persistence surface handlers depend on. *store.Store
// implements it; tests use an in-memory fake.
type WalletStore interface {
	GetBalance(ctx context.Context, userID, currency string) (int64, error)
	ApplyTransaction(ctx context.Context, in store.TxInput) (*store.TxResult, error)
	PingDB(ctx context.Context) error
}

type Server struct {
	store       WalletStore
	currency    string
	outboxTopic string
	version     string
}

func New(st WalletStore, currency, outboxTopic, version string) *Server {
	return &Server{store: st, currency: currency, outboxTopic: outboxTopic, version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /aurora.wallet.v1.WalletService/Credit", s.handleCredit)
	mux.HandleFunc("POST /aurora.wallet.v1.WalletService/Debit", s.handleDebit)
	mux.HandleFunc("POST /aurora.wallet.v1.WalletService/GetBalance", s.handleGetBalance)
	mux.HandleFunc("POST /aurora.wallet.v1.WalletService/HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /aurora.wallet.v1.WalletService/HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /healthz", s.handleHealthCheck)
	return logMiddleware(mux)
}

// ---------- JSON types ----------

type txRequest struct {
	UserID         string `json:"user_id"`
	AmountMinor    int64  `json:"amount_minor"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

type txResponse struct {
	TransactionID         string `json:"transaction_id"`
	UserID                string `json:"user_id"`
	Type                  string `json:"type"`
	AmountMinor           int64  `json:"amount_minor"`
	Currency              string `json:"currency"`
	ResultingBalanceMinor int64  `json:"resulting_balance_minor"`
	Replayed              bool   `json:"replayed"`
}

type getBalanceRequest struct {
	UserID string `json:"user_id"`
}

type getBalanceResponse struct {
	UserID       string `json:"user_id"`
	BalanceMinor int64  `json:"balance_minor"`
	Currency     string `json:"currency"`
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

// ---------- handlers ----------

func (s *Server) handleCredit(w http.ResponseWriter, r *http.Request) { s.applyTx(w, r, store.TypeCredit) }
func (s *Server) handleDebit(w http.ResponseWriter, r *http.Request)  { s.applyTx(w, r, store.TypeDebit) }

func (s *Server) applyTx(w http.ResponseWriter, r *http.Request, txType string) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req txRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "invalid_user", "user_id required")
		return
	}
	if req.AmountMinor <= 0 || req.AmountMinor > store.MaxAmountMinor {
		writeError(w, http.StatusBadRequest, "invalid_amount", "amount_minor must be between 1 and 1e15")
		return
	}
	if req.IdempotencyKey == "" {
		writeError(w, http.StatusBadRequest, "invalid_idempotency_key", "idempotency_key required")
		return
	}

	res, err := s.store.ApplyTransaction(ctx, store.TxInput{
		UserID:         req.UserID,
		Type:           txType,
		AmountMinor:    req.AmountMinor,
		Currency:       s.currency,
		IdempotencyKey: req.IdempotencyKey,
		OutboxTopic:    s.outboxTopic,
		NowUnixMS:      time.Now().UnixMilli(),
	})
	if err != nil {
		switch {
		case errors.Is(err, store.ErrWalletNotFound):
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found (open one by creating the user)")
		case errors.Is(err, store.ErrInsufficientFunds):
			writeError(w, http.StatusUnprocessableEntity, "insufficient_funds", "insufficient funds")
		case errors.Is(err, store.ErrIdempotencyConflict):
			writeError(w, http.StatusConflict, "idempotency_conflict", "idempotency_key reused with different parameters")
		default:
			slog.Error("applyTx failed", "type", txType, "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "transaction failed")
		}
		return
	}

	t := res.Transaction
	writeJSON(w, http.StatusOK, txResponse{
		TransactionID:         t.ID,
		UserID:                t.UserID,
		Type:                  t.Type,
		AmountMinor:           t.AmountMinor,
		Currency:              t.Currency,
		ResultingBalanceMinor: t.ResultingBalanceMinor,
		Replayed:              res.Replayed,
	})
}

func (s *Server) handleGetBalance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var req getBalanceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.UserID = strings.TrimSpace(req.UserID)
	if req.UserID == "" {
		writeError(w, http.StatusBadRequest, "invalid_user", "user_id required")
		return
	}
	bal, err := s.store.GetBalance(ctx, req.UserID, s.currency)
	if err != nil {
		if errors.Is(err, store.ErrWalletNotFound) {
			writeError(w, http.StatusNotFound, "wallet_not_found", "wallet not found")
			return
		}
		slog.Error("getBalance failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not fetch balance")
		return
	}
	writeJSON(w, http.StatusOK, getBalanceResponse{UserID: req.UserID, BalanceMinor: bal, Currency: s.currency})
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
	writeJSON(w, httpStatus, healthCheckResponse{Status: status, Version: s.version, Details: details})
}

// ---------- helpers ----------

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
