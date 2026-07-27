// Package store is wallet-svc's persistence layer: a double-entry ledger with
// materialized balances, idempotent transactions, and a transactional outbox
// (M3, see docs/adr/0004-wallet-ledger.md). Money is always integer minor units.
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

	"github.com/dazn/aurora/services/wallet-svc/internal/events"
)

const (
	OwnerUser   = "USER"
	OwnerSystem = "SYSTEM"
	MintOwnerID = "mint"
	TypeCredit  = "CREDIT"
	TypeDebit   = "DEBIT"

	// MaxAmountMinor caps a single credit/debit, well below int64 max so a
	// realistic sequence of operations cannot overflow the balance.
	MaxAmountMinor = 1_000_000_000_000_000 // 1e15
)

var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different parameters")
	ErrInvalidAmount       = errors.New("amount must be positive")
	ErrInvalidType         = errors.New("invalid transaction type")

	// errUniqueRetry is internal: a concurrent insert won the idempotency-key
	// race, so we retry and resolve via the idempotency lookup branch.
	errUniqueRetry = errors.New("unique violation, retry")
)

type Transaction struct {
	ID                    string
	IdempotencyKey        string
	Type                  string
	UserID                string
	AmountMinor           int64
	Currency              string
	ResultingBalanceMinor int64
	CreatedAtUnixMS       int64
}

type OutboxRow struct {
	ID      string
	Topic   string
	MsgKey  string
	Payload []byte
}

// TxInput is a credit/debit request.
type TxInput struct {
	UserID         string
	Type           string // TypeCredit | TypeDebit
	AmountMinor    int64  // > 0
	Currency       string
	IdempotencyKey string
	OutboxTopic    string
	NowUnixMS      int64
}

// TxResult carries the applied (or replayed) transaction.
type TxResult struct {
	Transaction *Transaction
	Replayed    bool
}

type Store struct {
	pool *pgxpool.Pool
}

// NewPostgres connects with retry while Postgres starts.
func NewPostgres(ctx context.Context, dsn string) (*Store, error) {
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		pool, err := pgxpool.New(ctx, dsn)
		if err == nil {
			if err = pool.Ping(ctx); err == nil {
				return &Store{pool: pool}, nil
			}
			pool.Close()
		}
		lastErr = err
		time.Sleep(1 * time.Second)
	}
	return nil, fmt.Errorf("postgres unreachable after 30s: %w", lastErr)
}

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

func (s *Store) PingDB(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate creates the schema and ensures the SYSTEM mint account exists.
func (s *Store) Migrate(ctx context.Context, currency string) error {
	if _, err := s.pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS accounts (
			id                 UUID PRIMARY KEY,
			owner_type         TEXT NOT NULL,
			owner_id           TEXT NOT NULL,
			currency           TEXT NOT NULL,
			balance_minor      BIGINT NOT NULL DEFAULT 0,
			created_at_unix_ms BIGINT NOT NULL,
			UNIQUE (owner_type, owner_id, currency)
		);
		CREATE TABLE IF NOT EXISTS transactions (
			id                      UUID PRIMARY KEY,
			idempotency_key         TEXT NOT NULL UNIQUE,
			type                    TEXT NOT NULL,
			user_id                 TEXT NOT NULL,
			amount_minor            BIGINT NOT NULL,
			currency                TEXT NOT NULL,
			resulting_balance_minor BIGINT NOT NULL,
			created_at_unix_ms      BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_transactions_user ON transactions(user_id);
		CREATE TABLE IF NOT EXISTS ledger_entries (
			id                 UUID PRIMARY KEY,
			transaction_id     UUID NOT NULL REFERENCES transactions(id),
			account_id         UUID NOT NULL REFERENCES accounts(id),
			amount_minor       BIGINT NOT NULL,
			created_at_unix_ms BIGINT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_ledger_account ON ledger_entries(account_id);
		CREATE TABLE IF NOT EXISTS outbox (
			id                   UUID PRIMARY KEY,
			topic                TEXT NOT NULL,
			msg_key              TEXT NOT NULL,
			payload              JSONB NOT NULL,
			created_at_unix_ms   BIGINT NOT NULL,
			published_at_unix_ms BIGINT NOT NULL DEFAULT 0
		);
		CREATE INDEX IF NOT EXISTS idx_outbox_unpublished ON outbox(published_at_unix_ms) WHERE published_at_unix_ms = 0;
	`); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO accounts (id, owner_type, owner_id, currency, balance_minor, created_at_unix_ms)
		VALUES ($1, $2, $3, $4, 0, $5)
		ON CONFLICT (owner_type, owner_id, currency) DO NOTHING
	`, uuid.NewString(), OwnerSystem, MintOwnerID, currency, time.Now().UnixMilli())
	return err
}

// OpenWallet idempotently creates a USER account (called by the user-created
// event consumer). Safe to call repeatedly.
func (s *Store) OpenWallet(ctx context.Context, userID, currency string, nowUnixMS int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO accounts (id, owner_type, owner_id, currency, balance_minor, created_at_unix_ms)
		VALUES ($1, $2, $3, $4, 0, $5)
		ON CONFLICT (owner_type, owner_id, currency) DO NOTHING
	`, uuid.NewString(), OwnerUser, userID, currency, nowUnixMS)
	return err
}

// GetBalance returns the user's balance, or ErrWalletNotFound.
func (s *Store) GetBalance(ctx context.Context, userID, currency string) (int64, error) {
	var bal int64
	err := s.pool.QueryRow(ctx, `
		SELECT balance_minor FROM accounts WHERE owner_type = $1 AND owner_id = $2 AND currency = $3
	`, OwnerUser, userID, currency).Scan(&bal)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrWalletNotFound
	}
	return bal, err
}

// ApplyTransaction applies a credit/debit atomically (double-entry + outbox),
// idempotent on IdempotencyKey. Returns Replayed=true for a repeat key.
func (s *Store) ApplyTransaction(ctx context.Context, in TxInput) (*TxResult, error) {
	if in.AmountMinor <= 0 || in.AmountMinor > MaxAmountMinor {
		return nil, ErrInvalidAmount
	}
	if in.Type != TypeCredit && in.Type != TypeDebit {
		return nil, ErrInvalidType
	}
	// At most one retry: a lost idempotency-key race resolves on the second pass
	// via the idempotency lookup. This relies on READ COMMITTED (pgx default):
	// the winning insert is committed and therefore visible on the retry's fresh
	// snapshot, so the second pass finds the row instead of re-colliding.
	for attempt := 0; attempt < 2; attempt++ {
		res, err := s.applyOnce(ctx, in)
		if errors.Is(err, errUniqueRetry) {
			continue
		}
		return res, err
	}
	return nil, fmt.Errorf("idempotency resolution failed for key %q", in.IdempotencyKey)
}

func (s *Store) applyOnce(ctx context.Context, in TxInput) (*TxResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) // no-op once committed

	// Lock the user account first to serialize all ops on this wallet; the
	// idempotency check then runs under that lock, making check+insert atomic.
	var userAcctID string
	var userBal int64
	err = tx.QueryRow(ctx, `
		SELECT id, balance_minor FROM accounts
		WHERE owner_type = $1 AND owner_id = $2 AND currency = $3 FOR UPDATE
	`, OwnerUser, in.UserID, in.Currency).Scan(&userAcctID, &userBal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrWalletNotFound
	}
	if err != nil {
		return nil, err
	}

	if existing, err := getTxByKey(ctx, tx, in.IdempotencyKey); err == nil {
		// A replay must match the recorded transaction exactly; otherwise a
		// caller could "replay" key K with a different amount/type and treat the
		// recorded success as covering the new parameters (money-safety).
		if existing.UserID != in.UserID || existing.AmountMinor != in.AmountMinor || existing.Type != in.Type {
			return nil, ErrIdempotencyConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &TxResult{Transaction: existing, Replayed: true}, nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Lock the system mint account too (balances change on both sides).
	var sysAcctID string
	var sysBal int64
	if err := tx.QueryRow(ctx, `
		SELECT id, balance_minor FROM accounts
		WHERE owner_type = $1 AND owner_id = $2 AND currency = $3 FOR UPDATE
	`, OwnerSystem, MintOwnerID, in.Currency).Scan(&sysAcctID, &sysBal); err != nil {
		return nil, fmt.Errorf("mint account lookup: %w", err)
	}

	var userDelta int64
	if in.Type == TypeCredit {
		userDelta = in.AmountMinor
	} else {
		if userBal < in.AmountMinor {
			return nil, ErrInsufficientFunds
		}
		userDelta = -in.AmountMinor
	}
	sysDelta := -userDelta
	newUserBal := userBal + userDelta
	newSysBal := sysBal + sysDelta
	if addOverflows(userBal, userDelta, newUserBal) || addOverflows(sysBal, sysDelta, newSysBal) {
		return nil, ErrInvalidAmount
	}

	txID := uuid.NewString()
	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions
			(id, idempotency_key, type, user_id, amount_minor, currency, resulting_balance_minor, created_at_unix_ms)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, txID, in.IdempotencyKey, in.Type, in.UserID, in.AmountMinor, in.Currency, newUserBal, in.NowUnixMS); err != nil {
		if isUniqueViolation(err) {
			return nil, errUniqueRetry
		}
		return nil, err
	}

	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance_minor = $1 WHERE id = $2`, newUserBal, userAcctID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance_minor = $1 WHERE id = $2`, newSysBal, sysAcctID); err != nil {
		return nil, err
	}
	if err := insertEntry(ctx, tx, txID, userAcctID, userDelta, in.NowUnixMS); err != nil {
		return nil, err
	}
	if err := insertEntry(ctx, tx, txID, sysAcctID, sysDelta, in.NowUnixMS); err != nil {
		return nil, err
	}

	// Transactional outbox: the event is persisted in the SAME tx as the ledger.
	_, payload, err := events.MarshalWalletTransaction(events.WalletTransaction{
		TransactionID:         txID,
		IdempotencyKey:        in.IdempotencyKey,
		UserID:                in.UserID,
		TxType:                in.Type,
		AmountMinor:           in.AmountMinor,
		Currency:              in.Currency,
		ResultingBalanceMinor: newUserBal,
	}, in.NowUnixMS)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO outbox (id, topic, msg_key, payload, created_at_unix_ms, published_at_unix_ms)
		VALUES ($1, $2, $3, $4, $5, 0)
	`, uuid.NewString(), in.OutboxTopic, in.UserID, payload, in.NowUnixMS); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &TxResult{
		Transaction: &Transaction{
			ID:                    txID,
			IdempotencyKey:        in.IdempotencyKey,
			Type:                  in.Type,
			UserID:                in.UserID,
			AmountMinor:           in.AmountMinor,
			Currency:              in.Currency,
			ResultingBalanceMinor: newUserBal,
			CreatedAtUnixMS:       in.NowUnixMS,
		},
	}, nil
}

// ---------- outbox relay support ----------

// FetchUnpublishedOutbox returns up to limit unpublished outbox rows, oldest first.
func (s *Store) FetchUnpublishedOutbox(ctx context.Context, limit int) ([]*OutboxRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, topic, msg_key, payload FROM outbox
		WHERE published_at_unix_ms = 0
		ORDER BY created_at_unix_ms
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OutboxRow
	for rows.Next() {
		r := &OutboxRow{}
		if err := rows.Scan(&r.ID, &r.Topic, &r.MsgKey, &r.Payload); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkOutboxPublished marks an outbox row delivered.
func (s *Store) MarkOutboxPublished(ctx context.Context, id string, nowUnixMS int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE outbox SET published_at_unix_ms = $1 WHERE id = $2`, nowUnixMS, id)
	return err
}

// ---------- helpers ----------

func getTxByKey(ctx context.Context, q pgx.Tx, key string) (*Transaction, error) {
	t := &Transaction{}
	err := q.QueryRow(ctx, `
		SELECT id, idempotency_key, type, user_id, amount_minor, currency, resulting_balance_minor, created_at_unix_ms
		FROM transactions WHERE idempotency_key = $1
	`, key).Scan(&t.ID, &t.IdempotencyKey, &t.Type, &t.UserID, &t.AmountMinor, &t.Currency, &t.ResultingBalanceMinor, &t.CreatedAtUnixMS)
	if err != nil {
		return nil, err
	}
	return t, nil
}

func insertEntry(ctx context.Context, tx pgx.Tx, txID, accountID string, amount, nowUnixMS int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (id, transaction_id, account_id, amount_minor, created_at_unix_ms)
		VALUES ($1, $2, $3, $4, $5)
	`, uuid.NewString(), txID, accountID, amount, nowUnixMS)
	return err
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// addOverflows reports whether a+delta wrapped around int64 (sum is the result).
func addOverflows(a, delta, sum int64) bool {
	return (delta > 0 && sum < a) || (delta < 0 && sum > a)
}
