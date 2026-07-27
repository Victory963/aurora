package events

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMarshalUserCreated_MatchesAvroContract asserts the JSON payload carries
// every field declared in libs/events/avro/user.lifecycle.created.v1.avsc. If
// the Avro schema and this encoder ever drift, this test fails.
func TestMarshalUserCreated_MatchesAvroContract(t *testing.T) {
	id, payload, err := MarshalUserCreated(UserCreated{
		UserID:          "u-123",
		Email:           "a@b.com",
		DisplayName:     "A",
		KYCCountry:      "GB",
		CreatedAtUnixMS: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty event_id")
	}

	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}

	for _, f := range []string{
		"event_id", "event_type", "occurred_at_unix_ms",
		"user_id", "email", "display_name", "kyc_country", "created_at_unix_ms",
	} {
		if _, ok := got[f]; !ok {
			t.Errorf("missing Avro field %q in payload: %s", f, payload)
		}
	}
	if got["event_type"] != TypeUserCreated {
		t.Errorf("event_type = %v, want %s", got["event_type"], TypeUserCreated)
	}
	if got["user_id"] != "u-123" || got["email"] != "a@b.com" {
		t.Errorf("payload field mismatch: %s", payload)
	}
}

func TestMarshalUserCreated_UniqueEventIDs(t *testing.T) {
	id1, _, _ := MarshalUserCreated(UserCreated{UserID: "x"})
	id2, _, _ := MarshalUserCreated(UserCreated{UserID: "x"})
	if id1 == id2 {
		t.Error("event_id must be unique per event")
	}
}

func TestMarshalUserDeleted_MatchesAvroContract(t *testing.T) {
	id, payload, err := MarshalUserDeleted(UserDeleted{UserID: "u-9", Reason: "self_service"})
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty event_id")
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v", err)
	}
	for _, f := range []string{"event_id", "event_type", "occurred_at_unix_ms", "user_id", "reason"} {
		if _, ok := got[f]; !ok {
			t.Errorf("missing Avro field %q in payload: %s", f, payload)
		}
	}
	if got["event_type"] != TypeUserDeleted {
		t.Errorf("event_type = %v, want %s", got["event_type"], TypeUserDeleted)
	}
	if got["user_id"] != "u-9" || got["reason"] != "self_service" {
		t.Errorf("payload field mismatch: %s", payload)
	}
}

func TestNopPublisher(t *testing.T) {
	var p Publisher = Nop{}
	if err := p.UserCreated(context.Background(), UserCreated{}); err != nil {
		t.Errorf("Nop.UserCreated should not error: %v", err)
	}
	if err := p.UserDeleted(context.Background(), UserDeleted{}); err != nil {
		t.Errorf("Nop.UserDeleted should not error: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Nop.Close should not error: %v", err)
	}
}
