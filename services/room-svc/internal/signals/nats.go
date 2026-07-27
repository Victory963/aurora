// Package signals publishes ephemeral room signals to NATS — the first NATS
// producer in Aurora (ADR-0005 §2). Subjects: aurora.room.<room_id>.<event>.
// At-most-once; the durable copy of chat lives in ScyllaDB.
package signals

import (
	"fmt"

	"github.com/nats-io/nats.go"
)

// Publisher publishes a room signal.
type Publisher interface {
	PublishRoom(roomID, event string, payload []byte) error
	Close() error
}

type NATS struct {
	nc *nats.Conn
}

func New(url string) (*NATS, error) {
	nc, err := nats.Connect(url, nats.Name("room-svc"))
	if err != nil {
		return nil, err
	}
	return &NATS{nc: nc}, nil
}

func subject(roomID, event string) string {
	return fmt.Sprintf("aurora.room.%s.%s", roomID, event)
}

// PublishRoom publishes and flushes so the signal reaches the server promptly
// (M4 favors determinism over batching; a real-time gateway can relax this).
func (n *NATS) PublishRoom(roomID, event string, payload []byte) error {
	if err := n.nc.Publish(subject(roomID, event), payload); err != nil {
		return err
	}
	return n.nc.Flush()
}

func (n *NATS) Close() error {
	n.nc.Close()
	return nil
}

// Nop drops signals (used when AURORA_ROOM_NATS_URL is unset).
type Nop struct{}

func (Nop) PublishRoom(string, string, []byte) error { return nil }
func (Nop) Close() error                             { return nil }
