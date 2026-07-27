package projector

import (
	"context"
	"testing"

	"github.com/dazn/aurora/services/graph-svc/internal/graph"
)

// fakeStore records projection calls (no Neo4j needed).
type fakeStore struct {
	users    map[string]graph.User
	deleted  []string
	bets     map[string]graph.Bet
	settled  map[string]graph.PoolSettled
	failNext error
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]graph.User{}, bets: map[string]graph.Bet{}, settled: map[string]graph.PoolSettled{}}
}

func (f *fakeStore) take() error { err := f.failNext; f.failNext = nil; return err }

func (f *fakeStore) UpsertUser(_ context.Context, u graph.User) error {
	if err := f.take(); err != nil {
		return err
	}
	f.users[u.ID] = u
	return nil
}
func (f *fakeStore) DeleteUser(_ context.Context, id string) error {
	if err := f.take(); err != nil {
		return err
	}
	delete(f.users, id)
	f.deleted = append(f.deleted, id)
	return nil
}
func (f *fakeStore) UpsertBet(_ context.Context, b graph.Bet) error {
	if err := f.take(); err != nil {
		return err
	}
	f.bets[b.BetID] = b
	return nil
}
func (f *fakeStore) SettlePool(_ context.Context, s graph.PoolSettled) error {
	if err := f.take(); err != nil {
		return err
	}
	f.settled[s.PoolID] = s
	return nil
}
func (f *fakeStore) CoBettors(context.Context, string, int) ([]graph.CoBettor, error) {
	return nil, nil
}
func (f *fakeStore) SybilCandidates(context.Context, string, int) ([]graph.SybilCandidate, error) {
	return nil, nil
}
func (f *fakeStore) Influence(context.Context, string) (*graph.Influence, error) {
	return &graph.Influence{}, nil
}
func (f *fakeStore) UserGraph(context.Context, string) (*graph.UserGraph, error) { return nil, nil }
func (f *fakeStore) Ping(context.Context) error                                  { return nil }
func (f *fakeStore) Close(context.Context) error                                 { return nil }

// ---------------- tests ----------------

func TestHandle_UserCreated_DropsPII(t *testing.T) {
	fs := newFakeStore()
	// Real wire shape from identity-svc — includes email + display_name.
	raw := []byte(`{"event_id":"e1","event_type":"user.lifecycle.created.v1",
		"occurred_at_unix_ms":1720000000000,"user_id":"u1",
		"email":"secret@example.com","display_name":"秘密","kyc_country":"JP",
		"created_at_unix_ms":1719999999000}`)
	if err := Handle(context.Background(), fs, raw); err != nil {
		t.Fatal(err)
	}
	u, ok := fs.users["u1"]
	if !ok {
		t.Fatal("user not projected")
	}
	if u.KYCCountry != "JP" || u.CreatedAtUnixMS != 1719999999000 {
		t.Fatalf("wrong projection: %+v", u)
	}
	// graph.User has no email/display_name fields at all — the PII boundary is
	// structural, but double-check nothing sneaks into the ID.
	if u.ID != "u1" {
		t.Fatalf("id mangled: %q", u.ID)
	}
}

func TestHandle_UserDeleted_GDPR(t *testing.T) {
	fs := newFakeStore()
	_ = Handle(context.Background(), fs, []byte(`{"event_type":"user.lifecycle.created.v1","user_id":"u1","kyc_country":"GB"}`))
	if err := Handle(context.Background(), fs, []byte(`{"event_type":"user.lifecycle.deleted.v1","user_id":"u1","reason":"user_requested"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := fs.users["u1"]; ok {
		t.Fatal("user still in graph after delete")
	}
	if len(fs.deleted) != 1 || fs.deleted[0] != "u1" {
		t.Fatalf("delete not recorded: %v", fs.deleted)
	}
}

func TestHandle_BetPlaced(t *testing.T) {
	fs := newFakeStore()
	raw := []byte(`{"event_id":"e2","event_type":"bet.placed.v1","occurred_at_unix_ms":1720000001000,
		"bet_id":"b1","user_id":"u1","market_id":"pool-9","selection":"YES",
		"stake_minor":500,"odds":2.2,"room_id":"r1"}`)
	if err := Handle(context.Background(), fs, raw); err != nil {
		t.Fatal(err)
	}
	b := fs.bets["b1"]
	if b.PoolID != "pool-9" || b.Selection != "YES" || b.StakeMinor != 500 ||
		b.RoomID != "r1" || b.AtUnixMS != 1720000001000 {
		t.Fatalf("wrong bet projection: %+v", b)
	}
}

func TestHandle_PoolSettled(t *testing.T) {
	fs := newFakeStore()
	raw := []byte(`{"event_type":"bet.pool.settled.v1","pool_id":"pool-9",
		"winning_option_id":"opt-1","total_stake_minor":1600,"rake_minor":30,"winners":1,"refunded":false}`)
	if err := Handle(context.Background(), fs, raw); err != nil {
		t.Fatal(err)
	}
	s := fs.settled["pool-9"]
	if s.WinningOptionID != "opt-1" || s.TotalStakeMinor != 1600 || s.RakeMinor != 30 {
		t.Fatalf("wrong settle projection: %+v", s)
	}
}

func TestHandle_PoisonMessageSkipped(t *testing.T) {
	fs := newFakeStore()
	if err := Handle(context.Background(), fs, []byte(`{not json`)); err != nil {
		t.Fatalf("poison message must be skipped (nil), got %v", err)
	}
	if err := Handle(context.Background(), fs, []byte(`{"event_type":"totally.unknown.v9"}`)); err != nil {
		t.Fatalf("unknown event type must be skipped, got %v", err)
	}
	// Events missing their business key are skipped, not retried forever.
	if err := Handle(context.Background(), fs, []byte(`{"event_type":"bet.placed.v1","user_id":"u1"}`)); err != nil {
		t.Fatalf("keyless event must be skipped, got %v", err)
	}
}

func TestHandle_StoreErrorPropagates(t *testing.T) {
	fs := newFakeStore()
	fs.failNext = context.DeadlineExceeded
	err := Handle(context.Background(), fs, []byte(`{"event_type":"user.lifecycle.created.v1","user_id":"u1"}`))
	if err == nil {
		t.Fatal("store error must propagate (no commit => redelivery)")
	}
}

func TestHandle_CreatedFallsBackToOccurredAt(t *testing.T) {
	fs := newFakeStore()
	raw := []byte(`{"event_type":"user.lifecycle.created.v1","occurred_at_unix_ms":123,"user_id":"u2"}`)
	if err := Handle(context.Background(), fs, raw); err != nil {
		t.Fatal(err)
	}
	if fs.users["u2"].CreatedAtUnixMS != 123 {
		t.Fatalf("created_at fallback missing: %+v", fs.users["u2"])
	}
}
