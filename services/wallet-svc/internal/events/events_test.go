package events

import (
	"encoding/json"
	"testing"
)

func TestMarshalWalletTransaction_MatchesAvroContract(t *testing.T) {
	id, payload, err := MarshalWalletTransaction(WalletTransaction{
		TransactionID:         "t1",
		IdempotencyKey:        "k1",
		UserID:                "u1",
		TxType:                "CREDIT",
		AmountMinor:           1000,
		Currency:              "AUC",
		ResultingBalanceMinor: 1000,
	}, 42)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty event_id")
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	for _, f := range []string{
		"event_id", "event_type", "occurred_at_unix_ms", "transaction_id",
		"idempotency_key", "user_id", "tx_type", "amount_minor", "currency", "resulting_balance_minor",
	} {
		if _, ok := got[f]; !ok {
			t.Errorf("missing Avro field %q in payload: %s", f, payload)
		}
	}
	if got["event_type"] != TypeWalletTransactionCompleted {
		t.Errorf("event_type = %v", got["event_type"])
	}
	if got["tx_type"] != "CREDIT" {
		t.Errorf("tx_type = %v", got["tx_type"])
	}
}
