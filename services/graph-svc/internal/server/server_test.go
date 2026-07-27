package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazn/aurora/services/graph-svc/internal/auth"
	"github.com/dazn/aurora/services/graph-svc/internal/graph"
)

const testSecret = "test-secret"

type fakeStore struct {
	co      []graph.CoBettor
	sybil   []graph.SybilCandidate
	inf     graph.Influence
	ug      *graph.UserGraph
	pingErr error
}

func (f *fakeStore) UpsertUser(context.Context, graph.User) error        { return nil }
func (f *fakeStore) DeleteUser(context.Context, string) error            { return nil }
func (f *fakeStore) UpsertBet(context.Context, graph.Bet) error          { return nil }
func (f *fakeStore) SettlePool(context.Context, graph.PoolSettled) error { return nil }
func (f *fakeStore) CoBettors(_ context.Context, _ string, limit int) ([]graph.CoBettor, error) {
	if limit < len(f.co) {
		return f.co[:limit], nil
	}
	return f.co, nil
}
func (f *fakeStore) SybilCandidates(_ context.Context, _ string, min int) ([]graph.SybilCandidate, error) {
	out := []graph.SybilCandidate{}
	for _, c := range f.sybil {
		if int(c.SharedSameSelPool) >= min {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeStore) Influence(context.Context, string) (*graph.Influence, error) {
	inf := f.inf
	return &inf, nil
}
func (f *fakeStore) UserGraph(context.Context, string) (*graph.UserGraph, error) { return f.ug, nil }
func (f *fakeStore) Ping(context.Context) error                                  { return f.pingErr }
func (f *fakeStore) Close(context.Context) error                                 { return nil }

// token signs an access token for the given subject. Query RPCs bind the
// queried user_id to the caller's sub (self-only in M8), so tests sign as the
// user they query.
func token(t *testing.T, sub string) string {
	t.Helper()
	tok, err := auth.Sign(auth.Claims{Sub: sub, Iss: auth.Issuer,
		Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()}, []byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func post(t *testing.T, s *Server, method, body, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/aurora.graph.v1.GraphService/"+method, bytes.NewReader([]byte(body)))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func TestAuthRequired(t *testing.T) {
	s := New(&fakeStore{}, []byte(testSecret), true, "test")
	for _, m := range []string{"CoBettors", "SybilCandidates", "Influence", "GetUserGraph"} {
		if w := post(t, s, m, `{"user_id":"u1"}`, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token: %d, want 401", m, w.Code)
		}
	}
}

func TestCoBettors(t *testing.T) {
	fs := &fakeStore{co: []graph.CoBettor{
		{UserID: "u2", SharedPools: 3, LastSharedAtUnix: 111},
		{UserID: "u3", SharedPools: 1, LastSharedAtUnix: 222},
	}}
	s := New(fs, []byte(testSecret), true, "test")
	w := post(t, s, "CoBettors", `{"user_id":"u1"}`, token(t, "u1"))
	if w.Code != http.StatusOK {
		t.Fatalf("code %d: %s", w.Code, w.Body.String())
	}
	var res struct {
		CoBettors []struct {
			UserID      string `json:"user_id"`
			SharedPools int32  `json:"shared_pools"`
		} `json:"co_bettors"`
	}
	_ = json.NewDecoder(w.Body).Decode(&res)
	if len(res.CoBettors) != 2 || res.CoBettors[0].UserID != "u2" || res.CoBettors[0].SharedPools != 3 {
		t.Fatalf("wrong result: %+v", res)
	}
}

func TestSybilMinSharedDefault(t *testing.T) {
	fs := &fakeStore{sybil: []graph.SybilCandidate{
		{UserID: "twin", SharedSameSelPool: 2, CreatedGapMS: 5000},
		{UserID: "meh", SharedSameSelPool: 1},
	}}
	s := New(fs, []byte(testSecret), true, "test")
	w := post(t, s, "SybilCandidates", `{"user_id":"u1"}`, token(t, "u1")) // min_shared omitted => 2
	var res struct {
		Candidates []struct {
			UserID string `json:"user_id"`
		} `json:"candidates"`
	}
	_ = json.NewDecoder(w.Body).Decode(&res)
	if len(res.Candidates) != 1 || res.Candidates[0].UserID != "twin" {
		t.Fatalf("default min_shared=2 not applied: %+v", res)
	}
}

func TestInfluence(t *testing.T) {
	fs := &fakeStore{inf: graph.Influence{Followers: 2, FollowEvents: 5, AvgLagMS: 1234, PoolsLed: 3}}
	s := New(fs, []byte(testSecret), true, "test")
	w := post(t, s, "Influence", `{"user_id":"u1"}`, token(t, "u1"))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"follow_events":5`) {
		t.Fatalf("influence wrong: %d %s", w.Code, w.Body.String())
	}
}

func TestUserGraph_NoPIIFieldsAndNotFound(t *testing.T) {
	fs := &fakeStore{ug: &graph.UserGraph{
		User: graph.User{ID: "u1", KYCCountry: "JP", CreatedAtUnixMS: 42},
		Bets: []graph.Bet{{PoolID: "p1", RoomID: "r1", Selection: "YES", StakeMinor: 100, AtUnixMS: 43}},
	}}
	s := New(fs, []byte(testSecret), true, "test")
	w := post(t, s, "GetUserGraph", `{"user_id":"u1"}`, token(t, "u1"))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("code %d", w.Code)
	}
	for _, pii := range []string{"email", "display_name"} {
		if strings.Contains(body, pii) {
			t.Fatalf("PII field %q leaked into graph response: %s", pii, body)
		}
	}
	fs.ug = nil
	if w := post(t, s, "GetUserGraph", `{"user_id":"ghost"}`, token(t, "ghost")); w.Code != http.StatusNotFound {
		t.Fatalf("missing user: %d, want 404", w.Code)
	}
}

// Regression (review finding): a valid token must NOT read another user's
// behavioral graph — user_id is bound to the caller's sub (self-only in M8).
func TestQueriesAreSelfOnly(t *testing.T) {
	fs := &fakeStore{co: []graph.CoBettor{{UserID: "u2", SharedPools: 3}}}
	s := New(fs, []byte(testSecret), true, "test")
	for _, m := range []string{"CoBettors", "SybilCandidates", "Influence", "GetUserGraph"} {
		w := post(t, s, m, `{"user_id":"victim"}`, token(t, "attacker"))
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s cross-user query: %d, want 403", m, w.Code)
		}
	}
}

func TestHealthDegradedWhenNeo4jDown(t *testing.T) {
	fs := &fakeStore{pingErr: context.DeadlineExceeded}
	s := New(fs, []byte(testSecret), true, "test")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("neo4j down => 503, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unreachable") {
		t.Fatalf("details missing: %s", w.Body.String())
	}
}

func TestBadRequests(t *testing.T) {
	s := New(&fakeStore{}, []byte(testSecret), true, "test")
	if w := post(t, s, "CoBettors", `{}`, token(t, "u1")); w.Code != http.StatusBadRequest {
		t.Fatalf("empty user_id: %d, want 400", w.Code)
	}
	if w := post(t, s, "CoBettors", `{nope`, token(t, "u1")); w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d, want 400", w.Code)
	}
}
