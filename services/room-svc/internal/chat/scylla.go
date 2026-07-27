// Package chat persists chat messages in ScyllaDB (Cassandra-compatible) via
// gocql. Messages are partitioned by room_id and clustered by a timeuuid DESC,
// so "recent messages in a room" is a single-partition query (ADR-0005 §1).
package chat

import (
	"context"
	"fmt"
	"time"

	"github.com/gocql/gocql"
)

type Message struct {
	ID              string // timeuuid
	RoomID          string
	UserID          string
	Body            string
	CreatedAtUnixMS int64
}

type Store struct {
	session  *gocql.Session
	keyspace string
}

// New connects to Scylla (retrying while it starts), creating the keyspace + table.
func New(hosts []string, port int, keyspace string) (*Store, error) {
	deadline := time.Now().Add(90 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		st, err := connect(hosts, port, keyspace)
		if err == nil {
			return st, nil
		}
		lastErr = err
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("scylla unreachable after 90s: %w", lastErr)
}

func connect(hosts []string, port int, keyspace string) (*Store, error) {
	// 1) keyspace-less session to create the keyspace.
	sys := gocql.NewCluster(hosts...)
	sys.Port = port
	sys.Consistency = gocql.One
	sys.Timeout = 10 * time.Second
	sysSession, err := sys.CreateSession()
	if err != nil {
		return nil, err
	}
	err = sysSession.Query(fmt.Sprintf(
		`CREATE KEYSPACE IF NOT EXISTS %s WITH replication = {'class':'SimpleStrategy','replication_factor':1}`,
		keyspace)).Exec()
	sysSession.Close()
	if err != nil {
		return nil, err
	}

	// 2) keyspace-bound session; create the table.
	c := gocql.NewCluster(hosts...)
	c.Port = port
	c.Keyspace = keyspace
	c.Consistency = gocql.One
	c.Timeout = 10 * time.Second
	session, err := c.CreateSession()
	if err != nil {
		return nil, err
	}
	if err := session.Query(`
		CREATE TABLE IF NOT EXISTS messages (
			room_id            text,
			message_id         timeuuid,
			user_id            text,
			body               text,
			created_at_unix_ms bigint,
			PRIMARY KEY (room_id, message_id)
		) WITH CLUSTERING ORDER BY (message_id DESC)
	`).Exec(); err != nil {
		session.Close()
		return nil, err
	}
	return &Store{session: session, keyspace: keyspace}, nil
}

func (s *Store) Close() {
	if s.session != nil {
		s.session.Close()
	}
}

// Health runs a cheap system query.
func (s *Store) Health(ctx context.Context) error {
	return s.session.Query(`SELECT release_version FROM system.local`).WithContext(ctx).Exec()
}

// InsertMessage appends a message and returns it (message_id is a fresh timeuuid).
func (s *Store) InsertMessage(ctx context.Context, roomID, userID, body string, nowUnixMS int64) (*Message, error) {
	id := gocql.TimeUUID()
	if err := s.session.Query(`
		INSERT INTO messages (room_id, message_id, user_id, body, created_at_unix_ms)
		VALUES (?, ?, ?, ?, ?)
	`, roomID, id, userID, body, nowUnixMS).WithContext(ctx).Exec(); err != nil {
		return nil, err
	}
	return &Message{ID: id.String(), RoomID: roomID, UserID: userID, Body: body, CreatedAtUnixMS: nowUnixMS}, nil
}

// ListMessages returns up to limit most-recent messages (newest first).
func (s *Store) ListMessages(ctx context.Context, roomID string, limit int) ([]*Message, error) {
	iter := s.session.Query(`
		SELECT message_id, user_id, body, created_at_unix_ms FROM messages
		WHERE room_id = ? LIMIT ?
	`, roomID, limit).WithContext(ctx).Iter()

	var (
		out       []*Message
		mid       gocql.UUID
		userID    string
		body      string
		createdAt int64
	)
	for iter.Scan(&mid, &userID, &body, &createdAt) {
		out = append(out, &Message{
			ID: mid.String(), RoomID: roomID, UserID: userID, Body: body, CreatedAtUnixMS: createdAt,
		})
	}
	if err := iter.Close(); err != nil {
		return nil, err
	}
	return out, nil
}
