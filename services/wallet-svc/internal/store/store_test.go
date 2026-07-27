// Integration tests for the real ledger. Gated behind AURORA_TEST_DB (needs
// `make up`), so default `go test` skips them.
//
//	AURORA_TEST_DB=1 go test ./internal/store/...
package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	if os.Getenv("AURORA_TEST_DB") == "" {
		t.Skip("set AURORA_TEST_DB=1 to run wallet integration tests (requires `make up`)")
	}
	dsn := os.Getenv("AURORA_TEST_DSN")
	if dsn == "" {
		dsn = "host=localhost port=5432 user=aurora password=aurora_dev_password dbname=aurora_wallet sslmode=disable"
	}
	ctx := context.Background()
	s, err := NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if err := s.Migrate(ctx, "AUC"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Clean slate, then restore the mint account.
	if _, err := s.pool.Exec(ctx, `TRUNCATE outbox, ledger_entries, transactions, accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.Migrate(ctx, "AUC"); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	return s
}

func TestLedger_Integration(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	const u = "user-int-1"

	if err := s.OpenWallet(ctx, u, "AUC", time.Now().UnixMilli()); err != nil {
		t.Fatalf("open wallet: %v", err)
	}

	apply := func(typ, key string, amt int64) (*TxResult, error) {
		return s.ApplyTransaction(ctx, TxInput{
			UserID: u, Type: typ, AmountMinor: amt, Currency: "AUC",
			IdempotencyKey: key, OutboxTopic: "aurora.wallet.transaction.v1", NowUnixMS: time.Now().UnixMilli(),
		})
	}

	if r, err := apply(TypeCredit, "k1", 1000); err != nil || r.Transaction.ResultingBalanceMinor != 1000 || r.Replayed {
		t.Fatalf("credit: err=%v res=%+v", err, r)
	}
	// Idempotent replay must NOT double-apply.
	if r, err := apply(TypeCredit, "k1", 1000); err != nil || !r.Replayed || r.Transaction.ResultingBalanceMinor != 1000 {
		t.Fatalf("replay: err=%v res=%+v", err, r)
	}
	if bal, _ := s.GetBalance(ctx, u, "AUC"); bal != 1000 {
		t.Fatalf("balance after replay = %d, want 1000", bal)
	}

	if r, err := apply(TypeDebit, "k2", 300); err != nil || r.Transaction.ResultingBalanceMinor != 700 {
		t.Fatalf("debit: err=%v res=%+v", err, r)
	}

	// Insufficient funds: rejected, balance unchanged, key NOT consumed.
	if _, err := apply(TypeDebit, "k3", 99999); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if bal, _ := s.GetBalance(ctx, u, "AUC"); bal != 700 {
		t.Fatalf("balance after failed debit = %d, want 700", bal)
	}
	var k3count int
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM transactions WHERE idempotency_key='k3'`).Scan(&k3count)
	if k3count != 0 {
		t.Errorf("failed debit must not consume the idempotency key (found %d)", k3count)
	}

	// Double-entry invariant: all ledger entries net to zero.
	var sum int64
	_ = s.pool.QueryRow(ctx, `SELECT COALESCE(SUM(amount_minor),0) FROM ledger_entries`).Scan(&sum)
	if sum != 0 {
		t.Errorf("ledger not balanced: sum=%d", sum)
	}
	// User's own entries equal the materialized balance.
	var userSum int64
	_ = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(le.amount_minor),0) FROM ledger_entries le
		JOIN accounts a ON a.id = le.account_id
		WHERE a.owner_type = $1 AND a.owner_id = $2`, OwnerUser, u).Scan(&userSum)
	if userSum != 700 {
		t.Errorf("user ledger sum = %d, want 700", userSum)
	}

	// Outbox: two successful txns (k1 credit, k2 debit) => two outbox rows.
	var obCount int
	_ = s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM outbox`).Scan(&obCount)
	if obCount != 2 {
		t.Errorf("outbox rows = %d, want 2", obCount)
	}
}

func TestIdempotencyConflict_DifferentUser(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UnixMilli()
	_ = s.OpenWallet(ctx, "user-a", "AUC", now)
	_ = s.OpenWallet(ctx, "user-b", "AUC", now)

	if _, err := s.ApplyTransaction(ctx, TxInput{UserID: "user-a", Type: TypeCredit, AmountMinor: 100, Currency: "AUC", IdempotencyKey: "shared", OutboxTopic: "t", NowUnixMS: now}); err != nil {
		t.Fatalf("first credit: %v", err)
	}
	// Same key, different user -> conflict.
	_, err := s.ApplyTransaction(ctx, TxInput{UserID: "user-b", Type: TypeCredit, AmountMinor: 100, Currency: "AUC", IdempotencyKey: "shared", OutboxTopic: "t", NowUnixMS: now})
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected ErrIdempotencyConflict, got %v", err)
	}
}

func TestWalletNotFound_Integration(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	ctx := context.Background()
	_, err := s.ApplyTransaction(ctx, TxInput{UserID: "ghost", Type: TypeCredit, AmountMinor: 100, Currency: "AUC", IdempotencyKey: "g1", OutboxTopic: "t", NowUnixMS: time.Now().UnixMilli()})
	if !errors.Is(err, ErrWalletNotFound) {
		t.Fatalf("expected ErrWalletNotFound, got %v", err)
	}
}
