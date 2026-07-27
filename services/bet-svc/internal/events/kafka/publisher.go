// Package kafka provides the Kafka-backed events.Publisher for bet-svc.
// Messages are keyed by pool id (same-pool ordering). JSON wire format (M1
// convention); direct publish, failures are the caller's to log (ADR-0006 §4).
package kafka

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/segmentio/kafka-go"

	"github.com/dazn/aurora/services/bet-svc/internal/events"
)

type Publisher struct {
	w *kafka.Writer
}

func New(brokers, topic string) *Publisher {
	parts := []string{}
	for _, s := range strings.Split(brokers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	// Pre-create the topic so the FIRST bet's publish doesn't land in the
	// auto-creation leader-election window ("Leader Not Available") and get
	// dropped — publish is best-effort direct (no outbox, ADR-0006 §4), so
	// that first event would otherwise be silently lost.
	ensureTopic(parts, topic)
	return &Publisher{w: &kafka.Writer{
		Addr:                   kafka.TCP(parts...),
		Topic:                  topic,
		Balancer:               &kafka.Hash{},
		RequiredAcks:           kafka.RequireAll,
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
		WriteTimeout:           5 * time.Second,
	}}
}

func ensureTopic(brokers []string, topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := &kafka.Client{Addr: kafka.TCP(brokers...), Timeout: 10 * time.Second}
	resp, err := client.CreateTopics(ctx, &kafka.CreateTopicsRequest{
		Topics: []kafka.TopicConfig{{Topic: topic, NumPartitions: 1, ReplicationFactor: 1}},
	})
	if err != nil {
		slog.Warn("kafka topic pre-create failed (writer auto-create remains)", "topic", topic, "error", err)
		return
	}
	if topicErr := resp.Errors[topic]; topicErr != nil && !errors.Is(topicErr, kafka.TopicAlreadyExists) {
		slog.Warn("kafka topic pre-create rejected (writer auto-create remains)", "topic", topic, "error", topicErr)
		return
	}
	slog.Info("kafka topic ready", "topic", topic)
}

func (p *Publisher) BetPlaced(ctx context.Context, e events.BetPlaced) error {
	_, payload, err := events.MarshalBetPlaced(e)
	if err != nil {
		return err
	}
	return p.w.WriteMessages(ctx, kafka.Message{Key: []byte(e.MarketID), Value: payload})
}

func (p *Publisher) PoolSettled(ctx context.Context, e events.PoolSettled) error {
	_, payload, err := events.MarshalPoolSettled(e)
	if err != nil {
		return err
	}
	return p.w.WriteMessages(ctx, kafka.Message{Key: []byte(e.PoolID), Value: payload})
}

func (p *Publisher) Close() error { return p.w.Close() }
