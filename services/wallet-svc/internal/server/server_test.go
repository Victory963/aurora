package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dazn/aurora/services/wallet-svc/internal/store"
)

// In-memory fake mirroring store semantics enough to test handler + error mapping.
type fakeStore struct {
	opened   map[string]bool
	balances map[string]int64
	txByKey  map[string]*store.Transaction
	pingErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{opened: map[string]bool{}, balances: map[string]int64{}, txByKey: map[string]*store.Transaction{}}
}

func (f *fakeStore) open(userID string) { f.opened[userID] = true }

func (f *fakeStore) GetBalance(_ context.Context, userID, _ string) (int64, error) {
	if !f.opened[userID] {
		return 0, store.ErrWalletNotFound
	}
	return f.balances[userID], nil
}

func (f *fakeStore) ApplyTransaction(_ context.Context, in store.TxInput) (*store.TxResult, error) {
	if !f.opened[in.UserID] {
		return nil, store.ErrWalletNotFound
	}
	if existing, ok := f.txByKey[in.IdempotencyKey]; ok {
		if existing.UserID != in.UserID {
			return nil, store.ErrIdempotencyConflict
		}
		return &store.TxResult{Transaction: existing, Replayed: true}, nil
	}
	bal := f.balances[in.UserID]
	var newBal int64
	if in.Type == store.TypeCredit {
		newBal = bal + in.AmountMinor
	} else {
		if bal < in.AmountMinor {
			return nil, store.ErrInsufficientFunds
		}
		newBal = bal - in.AmountMinor
	}
	f.balances[in.UserID] = newBal
	t := &store.Transaction{
		ID:                    "tx-" + in.IdempotencyKey,
		IdempotencyKey:        in.IdempotencyKey,
		Type:                  in.Type,
		UserID:                in.UserID,
		AmountMinor:           in.AmountMinor,
		Currency:              in.Currency,
		ResultingBalanceMinor: newBal,
		CreatedAtUnixMS:       in.NowUnixMS,
	}
	f.txByKey[in.IdempotencyKey] = t
	return &store.TxResult{Transaction: t}, nil
}

func (f *fakeStore) PingDB(context.Context) error { return f.pingErr }

func newServer(fs *fakeStore) *Server { return New(fs, "AUC", "aurora.wallet.transaction.v1", "test") }

func post(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

const credit = "/aurora.wallet.v1.WalletService/Credit"
const debit = "/aurora.wallet.v1.WalletService/Debit"
const getBalance = "/aurora.wallet.v1.WalletService/GetBalance"

func TestCredit_HappyPath(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	s := newServer(fs)
	w := post(t, s, credit, `{"user_id":"u1","amount_minor":1000,"idempotency_key":"k1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp txResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.ResultingBalanceMinor != 1000 || resp.Type != "CREDIT" || resp.Replayed {
		t.Errorf("unexpected: %+v", resp)
	}
}

func TestCredit_IdempotentReplay(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	s := newServer(fs)
	post(t, s, credit, `{"user_id":"u1","amount_minor":1000,"idempotency_key":"k1"}`)
	w := post(t, s, credit, `{"user_id":"u1","amount_minor":1000,"idempotency_key":"k1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("replay expected 200, got %d", w.Code)
	}
	var resp txResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if !resp.Replayed {
		t.Error("expected replayed=true")
	}
	if fs.balances["u1"] != 1000 {
		t.Errorf("replay double-applied: balance=%d", fs.balances["u1"])
	}
}

func TestCredit_InvalidAmount(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	w := post(t, newServer(fs), credit, `{"user_id":"u1","amount_minor":0,"idempotency_key":"k1"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCredit_MissingIdempotencyKey(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	w := post(t, newServer(fs), credit, `{"user_id":"u1","amount_minor":100}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCredit_AmountTooLarge(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	// store.MaxAmountMinor is 1e15; one above must be rejected (int64 overflow guard).
	w := post(t, newServer(fs), credit, `{"user_id":"u1","amount_minor":1000000000000001,"idempotency_key":"k1"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for over-cap amount, got %d", w.Code)
	}
}

func TestDebit_Insufficient(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1") // balance 0
	w := post(t, newServer(fs), debit, `{"user_id":"u1","amount_minor":100,"idempotency_key":"d1"}`)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	var er errorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Code != "insufficient_funds" {
		t.Errorf("expected insufficient_funds, got %q", er.Code)
	}
}

func TestDebit_HappyPath(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	s := newServer(fs)
	post(t, s, credit, `{"user_id":"u1","amount_minor":1000,"idempotency_key":"c1"}`)
	w := post(t, s, debit, `{"user_id":"u1","amount_minor":300,"idempotency_key":"d1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp txResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.ResultingBalanceMinor != 700 {
		t.Errorf("expected balance 700, got %d", resp.ResultingBalanceMinor)
	}
}

func TestCreditAndDebit_WalletNotFound(t *testing.T) {
	fs := newFakeStore() // u1 not opened
	s := newServer(fs)
	if w := post(t, s, credit, `{"user_id":"u1","amount_minor":100,"idempotency_key":"k1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("credit no wallet expected 404, got %d", w.Code)
	}
	if w := post(t, s, getBalance, `{"user_id":"u1"}`); w.Code != http.StatusNotFound {
		t.Fatalf("balance no wallet expected 404, got %d", w.Code)
	}
}

func TestGetBalance_Happy(t *testing.T) {
	fs := newFakeStore()
	fs.open("u1")
	s := newServer(fs)
	post(t, s, credit, `{"user_id":"u1","amount_minor":500,"idempotency_key":"k1"}`)
	w := post(t, s, getBalance, `{"user_id":"u1"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp getBalanceResponse
	_ = json.NewDecoder(w.Body).Decode(&resp)
	if resp.BalanceMinor != 500 || resp.Currency != "AUC" {
		t.Errorf("unexpected: %+v", resp)
	}
}

func TestHealthDegradedReturns503(t *testing.T) {
	fs := newFakeStore()
	fs.pingErr = context.DeadlineExceeded
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/healthz", nil)
	newServer(fs).Routes().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
