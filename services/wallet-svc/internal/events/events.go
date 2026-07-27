// Package events defines wallet-svc's outbound event contract. The on-the-wire
// JSON mirrors libs/events/avro/wallet.transaction.completed.v1.avsc exactly.
package events

import (
	"encoding/json"

	"github.com/google/uuid"
)

const (
	TopicWalletTransaction         = "aurora.wallet.transaction.v1"
	TypeWalletTransactionCompleted = "wallet.transaction.completed.v1"
)

// WalletTransaction is the domain payload for wallet.transaction.completed.v1.
type WalletTransaction struct {
	TransactionID         string
	IdempotencyKey        string
	UserID                string
	TxType                string // CREDIT | DEBIT
	AmountMinor           int64
	Currency              string
	ResultingBalanceMinor int64
}

// walletTxEnvelope is the flat JSON record matching the Avro schema.
type walletTxEnvelope struct {
	EventID               string `json:"event_id"`
	EventType             string `json:"event_type"`
	OccurredAtUnixMS      int64  `json:"occurred_at_unix_ms"`
	TransactionID         string `json:"transaction_id"`
	IdempotencyKey        string `json:"idempotency_key"`
	UserID                string `json:"user_id"`
	TxType                string `json:"tx_type"`
	AmountMinor           int64  `json:"amount_minor"`
	Currency              string `json:"currency"`
	ResultingBalanceMinor int64  `json:"resulting_balance_minor"`
}

// MarshalWalletTransaction builds the canonical JSON payload (stamps event_id +
// occurred_at). Used inside the wallet write transaction to fill the outbox row.
func MarshalWalletTransaction(e WalletTransaction, occurredAtUnixMS int64) (eventID string, payload []byte, err error) {
	eventID = uuid.NewString()
	env := walletTxEnvelope{
		EventID:               eventID,
		EventType:             TypeWalletTransactionCompleted,
		OccurredAtUnixMS:      occurredAtUnixMS,
		TransactionID:         e.TransactionID,
		IdempotencyKey:        e.IdempotencyKey,
		UserID:                e.UserID,
		TxType:                e.TxType,
		AmountMinor:           e.AmountMinor,
		Currency:              e.Currency,
		ResultingBalanceMinor: e.ResultingBalanceMinor,
	}
	payload, err = json.Marshal(env)
	return eventID, payload, err
}
