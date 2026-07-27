// Package kafka provides a Kafka-backed events.Publisher for identity-svc.
//
// M1 wire format is JSON (see docs/adr/0002-event-mesh.md). Messages are keyed
// by user_id so a user's events land on one partition (ordering + future log
// compaction). The writer waits for all in-sync replicas (RequireAll).
package kafka

import (
	"context"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/dazn/aurora/services/identity-svc/internal/events"
)

// Publisher implements events.Publisher over a Kafka topic.
type Publisher struct {
	w     *kafka.Writer
	topic string
}

// New builds a Kafka publisher. brokers is a comma-separated host:port list.
func New(brokers, topic string) *Publisher {
	w := &kafka.Writer{
		Addr:                   kafka.TCP(splitBrokers(brokers)...),
		Topic:                  topic,
		Balancer:               &kafka.Hash{}, // key-based: same user_id → same partition
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
		WriteTimeout:           5 * time.Second,
	}
	return &Publisher{w: w, topic: topic}
}

// UserCreated publishes a user.lifecycle.created.v1 event. Honors ctx timeout.
func (p *Publisher) UserCreated(ctx context.Context, e events.UserCreated) error {
	_, payload, err := events.MarshalUserCreated(e)
	if err != nil {
		return err
	}
	return p.w.WriteMessages(ctx, kafka.Message{
		Key:   []byte(e.UserID),
		Value: payload,
	})
}

// UserDeleted publishes a user.lifecycle.deleted.v1 event (same topic, keyed by
// user_id so it lands on the same partition as the user's created event).
func (p *Publisher) UserDeleted(ctx context.Context, e events.UserDeleted) error {
	_, payload, err := events.MarshalUserDeleted(e)
	if err != nil {
		return err
	}
	return p.w.WriteMessages(ctx, kafka.Message{
		Key:   []byte(e.UserID),
		Value: payload,
	})
}

// Close flushes and closes the underlying writer.
func (p *Publisher) Close() error { return p.w.Close() }

func splitBrokers(csv string) []string {
	parts := strings.Split(csv, ",")
	out := make([]string, 0, len(parts))
	for _, s := range parts {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
