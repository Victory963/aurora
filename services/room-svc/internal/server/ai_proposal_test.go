package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/dazn/aurora/services/room-svc/internal/clients"
)

// ---- AI / Bet fakes ----

type fakeAI struct {
	recs    []clients.Recommendation
	err     error
	enabled bool

	gotUser, gotMatch, gotRoom string
}

func (f *fakeAI) Enabled() bool { return f.enabled }
func (f *fakeAI) Recommend(_ context.Context, userID, matchID, roomID string) ([]clients.Recommendation, error) {
	f.gotUser, f.gotMatch, f.gotRoom = userID, matchID, roomID
	if f.err != nil {
		return nil, f.err
	}
	return f.recs, nil
}

type fakeBet struct {
	err     error
	enabled bool

	gotBearer, gotRoom, gotQuestion string
	gotOptions                      []clients.NewOption
	pool                            *clients.Pool
}

func (f *fakeBet) Enabled() bool { return f.enabled }
func (f *fakeBet) CreatePool(_ context.Context, bearer, roomID, question string, options []clients.NewOption) (*clients.Pool, error) {
	f.gotBearer, f.gotRoom, f.gotQuestion, f.gotOptions = bearer, roomID, question, options
	if f.err != nil {
		return nil, f.err
	}
	if f.pool != nil {
		return f.pool, nil
	}
	return &clients.Pool{
		ID: "00000000-0000-0000-0000-0000000000aa", RoomID: roomID, Question: question,
		Status: "OPEN", CreatedBy: "userA", RakeBps: 500,
		Options: []clients.PoolOption{
			{ID: "opt-yes", Label: options[0].Label, OddsX100: options[0].OddsX100},
			{ID: "opt-no", Label: options[1].Label, OddsX100: options[1].OddsX100},
		},
	}, nil
}

// newServerAI wires a server with explicit AI/Bet fakes.
func newServerAI(ai *fakeAI, bet *fakeBet) (*Server, *fakeRooms, *fakeChat, *fakeSignals) {
	fr, fc, fs := newFakeRooms(), newFakeChat(), &fakeSignals{}
	return New(fr, fc, fs, ai, bet, []byte(testSecret), "test"), fr, fc, fs
}

// makeRoomWithMember creates a room owned by owner and returns its id.
func makeRoomWithMember(t *testing.T, s *Server, ownerTok string) string {
	t.Helper()
	w := post(t, s, "CreateRoom", `{"name":"ai-room"}`, ownerTok)
	if w.Code != http.StatusOK {
		t.Fatalf("create room: %d %s", w.Code, w.Body.String())
	}
	var room roomJSON
	_ = json.NewDecoder(w.Body).Decode(&room)
	return room.ID
}

// ---- tests ----

func TestProposeAiPool_HappyPath(t *testing.T) {
	ai := &fakeAI{enabled: true, recs: []clients.Recommendation{{
		MarketID: "over_2_5_goals", Selection: "Over 2.5 Goals", SuggestedOdds: 2.20,
		AIConfidence: 0.63, ExpectedValue: 0.12, Reasoning: "xG trend favors goals",
		DataSources: []string{"tool:player_stats", "tool:odds", "engine:fallback:template"},
	}}}
	bet := &fakeBet{enabled: true}
	s, _, fc, fsig := newServerAI(ai, bet)
	tok := token(t, "userA")
	rid := makeRoomWithMember(t, s, tok)

	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"match-1"}`, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d %s", w.Code, w.Body.String())
	}
	var resp proposeAiPoolResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// AI was asked in ROOM context for this caller/match/room.
	if ai.gotUser != "userA" || ai.gotMatch != "match-1" || ai.gotRoom != rid {
		t.Errorf("ai called with wrong args: %+v", ai)
	}
	// Bet got the forwarded token and YES/NO options; NO is the fair complement
	// of 2.20 => 2.20/1.20 = 1.833 => 183 (x100).
	if bet.gotBearer != tok {
		t.Errorf("bearer not forwarded to bet")
	}
	if len(bet.gotOptions) != 2 || bet.gotOptions[0].Label != "YES" || bet.gotOptions[1].Label != "NO" {
		t.Fatalf("unexpected options: %+v", bet.gotOptions)
	}
	if bet.gotOptions[0].OddsX100 != 220 {
		t.Errorf("YES odds = %d, want 220", bet.gotOptions[0].OddsX100)
	}
	if bet.gotOptions[1].OddsX100 != 183 {
		t.Errorf("NO complement odds = %d, want 183", bet.gotOptions[1].OddsX100)
	}
	// Pool + recommendation echoed back.
	if resp.Pool == nil || resp.Pool.ID == "" {
		t.Errorf("pool missing in response")
	}
	if resp.Recommendation.Selection != "Over 2.5 Goals" {
		t.Errorf("recommendation not echoed: %+v", resp.Recommendation)
	}
	// AI chat message persisted under the reserved AIUserID.
	msgs := fc.byRoom[rid]
	if len(msgs) != 1 || msgs[0].UserID != AIUserID {
		t.Fatalf("expected 1 AI message from %s, got %+v", AIUserID, msgs)
	}
	if resp.Message == nil || resp.Message.UserID != AIUserID {
		t.Errorf("response message not from AI: %+v", resp.Message)
	}
	// ai_proposal signal emitted carrying the pool id.
	raw, ok := fsig.payloads["ai_proposal"]
	if !ok {
		t.Fatalf("no ai_proposal signal; events=%v", fsig.events)
	}
	var sig map[string]any
	_ = json.Unmarshal(raw, &sig)
	if sig["pool_id"] != resp.Pool.ID {
		t.Errorf("ai_proposal pool_id=%v, want %s", sig["pool_id"], resp.Pool.ID)
	}
}

func TestProposeAiPool_NonMemberForbidden(t *testing.T) {
	ai := &fakeAI{enabled: true, recs: []clients.Recommendation{{Selection: "X", SuggestedOdds: 2.0}}}
	bet := &fakeBet{enabled: true}
	s, _, _, _ := newServerAI(ai, bet)
	rid := makeRoomWithMember(t, s, token(t, "userA"))

	// userB is not a member.
	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"m"}`, token(t, "userB"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-member expected 403, got %d %s", w.Code, w.Body.String())
	}
	// AI must not have been called for a non-member.
	if ai.gotUser != "" {
		t.Errorf("ai should not be called for non-member")
	}
}

func TestProposeAiPool_AINoRecommendation(t *testing.T) {
	ai := &fakeAI{enabled: true, recs: nil} // degraded: empty
	bet := &fakeBet{enabled: true}
	s, _, fc, _ := newServerAI(ai, bet)
	tok := token(t, "userA")
	rid := makeRoomWithMember(t, s, tok)

	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"m"}`, tok)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d %s", w.Code, w.Body.String())
	}
	// No half-finished state: no pool created, no message written.
	if bet.gotRoom != "" {
		t.Errorf("bet should not be called when AI has no recommendation")
	}
	if len(fc.byRoom[rid]) != 0 {
		t.Errorf("no AI message should be written on ai_no_recommendation")
	}
}

func TestProposeAiPool_AIUnavailable(t *testing.T) {
	ai := &fakeAI{enabled: true, err: errors.New("boom")}
	bet := &fakeBet{enabled: true}
	s, _, _, _ := newServerAI(ai, bet)
	tok := token(t, "userA")
	rid := makeRoomWithMember(t, s, tok)

	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"m"}`, tok)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d %s", w.Code, w.Body.String())
	}
	if bet.gotRoom != "" {
		t.Errorf("bet should not be called when AI errored")
	}
}

func TestProposeAiPool_BetUnavailable(t *testing.T) {
	ai := &fakeAI{enabled: true, recs: []clients.Recommendation{{Selection: "X", SuggestedOdds: 2.0}}}
	bet := &fakeBet{enabled: true, err: clients.ErrUnavailable}
	s, _, fc, _ := newServerAI(ai, bet)
	tok := token(t, "userA")
	rid := makeRoomWithMember(t, s, tok)

	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"m"}`, tok)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d %s", w.Code, w.Body.String())
	}
	// Pool creation failed => no AI chat message (avoid dangling proposal).
	if len(fc.byRoom[rid]) != 0 {
		t.Errorf("no AI message should be written when pool creation fails")
	}
}

func TestProposeAiPool_NotConfigured(t *testing.T) {
	ai := &fakeAI{enabled: false}
	bet := &fakeBet{enabled: false}
	s, _, _, _ := newServerAI(ai, bet)
	tok := token(t, "userA")
	rid := makeRoomWithMember(t, s, tok)

	w := post(t, s, "ProposeAiPool", `{"room_id":"`+rid+`","match_id":"m"}`, tok)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d %s", w.Code, w.Body.String())
	}
}

func TestProposeAiPool_RequiresAuth(t *testing.T) {
	s, _, _, _ := newServerAI(&fakeAI{enabled: true}, &fakeBet{enabled: true})
	if w := post(t, s, "ProposeAiPool", `{"room_id":"x"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestComplementOdds(t *testing.T) {
	cases := []struct{ yes, wantNo int64 }{
		{220, 183}, // 2.20 -> 1.833
		{200, 200}, // 2.00 -> 2.00
		{400, 133}, // 4.00 -> 1.333
		{101, 10100},
	}
	for _, c := range cases {
		if got := complementOddsX100(c.yes); got != c.wantNo {
			t.Errorf("complementOddsX100(%d) = %d, want %d", c.yes, got, c.wantNo)
		}
	}
}
