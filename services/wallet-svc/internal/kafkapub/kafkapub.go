// Package kafkapub publishes outbox messages to Kafka. The topic is set
// per-message (the writer has no fixed topic) so one writer serves all topics.
package kafkapub

import (
	"context"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/dazn/aurora/services/wallet-svc/internal/kafkautil"
)

// Publisher is the minimal interface the outbox relay needs.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, payload []byte) error
	Close() error
}

// Writer is a Kafka-backed Publisher.
type Writer struct {
	w *kafka.Writer
}

func New(brokers string) *Writer {
	return &Writer{w: &kafka.Writer{
		Addr:                   kafka.TCP(kafkautil.Brokers(brokers)...),
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
		WriteTimeout:           5 * time.Second,
	}}
}

func (p *Writer) Publish(ctx context.Context, topic, key string, payload []byte) error {
	return p.w.WriteMessages(ctx, kafka.Message{Topic: topic, Key: []byte(key), Value: payload})
}

func (p *Writer) Close() error { return p.w.Close() }

// Nop is a Publisher that drops messages (used when Kafka is not configured).
type Nop struct{}

func (Nop) Publish(context.Context, string, string, []byte) error { return nil }
func (Nop) Close() error                                          { return nil }
