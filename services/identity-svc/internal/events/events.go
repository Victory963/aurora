// Package events defines identity-svc's outbound event contracts and the
// Publisher abstraction (M1, see docs/adr/0002-event-mesh.md).
//
// The concrete Kafka implementation lives in the kafka subpackage so that code
// and tests depending only on the contract (e.g. the HTTP server) do not pull
// in the Kafka client. The on-the-wire shape mirrors the Avro records in
// libs/events/avro/*.avsc exactly; M1 ships JSON, binary Avro is deferred.
package events

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event type identifiers. These MUST match libs/events/avro/*.avsc and the
// subjects registered in Schema Registry by scripts/register_schemas.sh.
const (
	TypeUserCreated = "user.lifecycle.created.v1"
	TypeUserDeleted = "user.lifecycle.deleted.v1"
)

// UserCreated is the domain payload for a user.lifecycle.created.v1 event.
type UserCreated struct {
	UserID          string
	Email           string
	DisplayName     string
	KYCCountry      string
	CreatedAtUnixMS int64
}

// UserDeleted is the domain payload for a user.lifecycle.deleted.v1 event (M2).
type UserDeleted struct {
	UserID string
	Reason string
}

// userCreatedEnvelope is the on-the-wire JSON shape. Field names mirror
// libs/events/avro/user.lifecycle.created.v1.avsc one-to-one (flat record).
type userCreatedEnvelope struct {
	EventID          string `json:"event_id"`
	EventType        string `json:"event_type"`
	OccurredAtUnixMS int64  `json:"occurred_at_unix_ms"`
	UserID           string `json:"user_id"`
	Email            string `json:"email"`
	DisplayName      string `json:"display_name"`
	KYCCountry       string `json:"kyc_country"`
	CreatedAtUnixMS  int64  `json:"created_at_unix_ms"`
}

// userDeletedEnvelope mirrors libs/events/avro/user.lifecycle.deleted.v1.avsc.
type userDeletedEnvelope struct {
	EventID          string `json:"event_id"`
	EventType        string `json:"event_type"`
	OccurredAtUnixMS int64  `json:"occurred_at_unix_ms"`
	UserID           string `json:"user_id"`
	Reason           string `json:"reason"`
}

// Publisher publishes identity domain events. Implementations must be safe for
// concurrent use and must honor ctx for cancellation/timeout.
type Publisher interface {
	UserCreated(ctx context.Context, e UserCreated) error
	UserDeleted(ctx context.Context, e UserDeleted) error
	Close() error
}

// MarshalUserCreated builds the canonical JSON for a UserCreated event, stamping
// a fresh event_id and occurred_at. The Kafka publisher and tests share this one
// encoder so the wire shape has a single source of truth.
func MarshalUserCreated(e UserCreated) (eventID string, payload []byte, err error) {
	eventID = uuid.NewString()
	env := userCreatedEnvelope{
		EventID:          eventID,
		EventType:        TypeUserCreated,
		OccurredAtUnixMS: time.Now().UnixMilli(),
		UserID:           e.UserID,
		Email:            e.Email,
		DisplayName:      e.DisplayName,
		KYCCountry:       e.KYCCountry,
		CreatedAtUnixMS:  e.CreatedAtUnixMS,
	}
	payload, err = json.Marshal(env)
	return eventID, payload, err
}

// MarshalUserDeleted builds the canonical JSON for a UserDeleted event.
func MarshalUserDeleted(e UserDeleted) (eventID string, payload []byte, err error) {
	eventID = uuid.NewString()
	env := userDeletedEnvelope{
		EventID:          eventID,
		EventType:        TypeUserDeleted,
		OccurredAtUnixMS: time.Now().UnixMilli(),
		UserID:           e.UserID,
		Reason:           e.Reason,
	}
	payload, err = json.Marshal(env)
	return eventID, payload, err
}

// Nop is a Publisher that does nothing. Used when AURORA_IDENTITY_KAFKA_BROKERS
// is unset (M0-style runs without the event mesh) and in unit tests.
type Nop struct{}

func (Nop) UserCreated(context.Context, UserCreated) error { return nil }
func (Nop) UserDeleted(context.Context, UserDeleted) error { return nil }
func (Nop) Close() error                                   { return nil }
