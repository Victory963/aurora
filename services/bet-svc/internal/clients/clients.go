// Package clients holds bet-svc's outbound HTTP clients (wallet, room).
// Caller-side timeouts per repo convention; errors are mapped to sentinel
// values the handlers translate into {code,message} responses.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var (
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different parameters")
	ErrNotMember           = errors.New("not a room member")
)

// ---------------------------------------------------------------- wallet ----

type Wallet struct {
	base string
	http *http.Client
}

func NewWallet(baseURL string) *Wallet {
	return &Wallet{base: baseURL, http: &http.Client{Timeout: 5 * time.Second}}
}

type walletTxReq struct {
	UserID         string `json:"user_id"`
	AmountMinor    int64  `json:"amount_minor"`
	IdempotencyKey string `json:"idempotency_key"`
	Reason         string `json:"reason"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (w *Wallet) call(ctx context.Context, method, userID string, amount int64, key, reason string) error {
	body, _ := json.Marshal(walletTxReq{UserID: userID, AmountMinor: amount, IdempotencyKey: key, Reason: reason})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		w.base+"/aurora.wallet.v1.WalletService/"+method, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return fmt.Errorf("wallet unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	var e apiError
	_ = json.NewDecoder(resp.Body).Decode(&e)
	switch {
	case resp.StatusCode == http.StatusUnprocessableEntity || e.Code == "insufficient_funds":
		return ErrInsufficientFunds
	case resp.StatusCode == http.StatusNotFound || e.Code == "wallet_not_found":
		return ErrWalletNotFound
	case resp.StatusCode == http.StatusConflict || e.Code == "idempotency_conflict":
		return ErrIdempotencyConflict
	default:
		return fmt.Errorf("wallet %s failed: http %d code=%s", method, resp.StatusCode, e.Code)
	}
}

// Debit charges the user (idempotent by key on the wallet side).
func (w *Wallet) Debit(ctx context.Context, userID string, amount int64, key, reason string) error {
	return w.call(ctx, "Debit", userID, amount, key, reason)
}

// Credit pays the user (idempotent by key on the wallet side).
func (w *Wallet) Credit(ctx context.Context, userID string, amount int64, key, reason string) error {
	return w.call(ctx, "Credit", userID, amount, key, reason)
}

// ------------------------------------------------------------------ room ----

type Room struct {
	base string
	http *http.Client
}

func NewRoom(baseURL string) *Room {
	return &Room{base: baseURL, http: &http.Client{Timeout: 3 * time.Second}}
}

// CheckMember verifies room membership by calling ListMembers WITH THE
// CALLER'S OWN bearer token: room-svc 403s non-members, so a 200 proves
// membership without any service credential (ADR-0006 §3).
// Returns nil, ErrNotMember, or a transport/5xx error (=> 503 upstream).
func (r *Room) CheckMember(ctx context.Context, roomID, bearer string) error {
	if r == nil || r.base == "" {
		return nil // membership checks disabled (dev)
	}
	body, _ := json.Marshal(map[string]string{"room_id": roomID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.base+"/aurora.room.v1.RoomService/ListMembers", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("room unreachable: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusBadRequest, http.StatusNotFound:
		return ErrNotMember
	default:
		return fmt.Errorf("room membership check failed: http %d", resp.StatusCode)
	}
}
