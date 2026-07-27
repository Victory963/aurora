package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazn/aurora/services/room-svc/internal/auth"
	"github.com/dazn/aurora/services/room-svc/internal/chat"
	"github.com/dazn/aurora/services/room-svc/internal/store"
)

const testSecret = "test-secret"

// ---- fakes ----

type fakeRooms struct {
	rooms   map[string]*store.Room
	members map[string]map[string]*store.Membership
	pingErr error
}

func newFakeRooms() *fakeRooms {
	return &fakeRooms{rooms: map[string]*store.Room{}, members: map[string]map[string]*store.Membership{}}
}

func (f *fakeRooms) CreateRoom(_ context.Context, id, name, owner string, now int64) (*store.Room, error) {
	r := &store.Room{ID: id, Name: name, OwnerUserID: owner, CreatedAtUnixMS: now}
	f.rooms[id] = r
	f.members[id] = map[string]*store.Membership{owner: {RoomID: id, UserID: owner, Role: store.RoleOwner, JoinedAtUnixMS: now}}
	return r, nil
}
func (f *fakeRooms) GetRoom(_ context.Context, id string) (*store.Room, error) {
	if r, ok := f.rooms[id]; ok {
		return r, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeRooms) AddMember(_ context.Context, roomID, userID, role string, now int64) error {
	if f.members[roomID] == nil {
		f.members[roomID] = map[string]*store.Membership{}
	}
	if _, ok := f.members[roomID][userID]; !ok {
		f.members[roomID][userID] = &store.Membership{RoomID: roomID, UserID: userID, Role: role, JoinedAtUnixMS: now}
	}
	return nil
}
func (f *fakeRooms) GetMembership(_ context.Context, roomID, userID string) (*store.Membership, error) {
	if m, ok := f.members[roomID][userID]; ok {
		return m, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeRooms) RemoveMember(_ context.Context, roomID, userID string) (bool, error) {
	if _, ok := f.members[roomID][userID]; ok {
		delete(f.members[roomID], userID)
		return true, nil
	}
	return false, nil
}
func (f *fakeRooms) IsMember(_ context.Context, roomID, userID string) (bool, error) {
	_, ok := f.members[roomID][userID]
	return ok, nil
}
func (f *fakeRooms) ListMembers(_ context.Context, roomID string) ([]*store.Membership, error) {
	var out []*store.Membership
	for _, m := range f.members[roomID] {
		out = append(out, m)
	}
	return out, nil
}
func (f *fakeRooms) ListRoomsForUser(_ context.Context, userID string) ([]*store.Room, error) {
	var out []*store.Room
	for rid, mm := range f.members {
		if _, ok := mm[userID]; ok {
			if r, ok := f.rooms[rid]; ok {
				out = append(out, r)
			}
		}
	}
	return out, nil
}
func (f *fakeRooms) PingDB(context.Context) error { return f.pingErr }

type fakeChat struct {
	byRoom    map[string][]*chat.Message
	healthErr error
	seq       int
}

func newFakeChat() *fakeChat { return &fakeChat{byRoom: map[string][]*chat.Message{}} }

func (f *fakeChat) InsertMessage(_ context.Context, roomID, userID, body string, now int64) (*chat.Message, error) {
	f.seq++
	m := &chat.Message{ID: fmt.Sprintf("msg-%d", f.seq), RoomID: roomID, UserID: userID, Body: body, CreatedAtUnixMS: now}
	f.byRoom[roomID] = append([]*chat.Message{m}, f.byRoom[roomID]...) // newest first
	return m, nil
}
func (f *fakeChat) ListMessages(_ context.Context, roomID string, limit int) ([]*chat.Message, error) {
	msgs := f.byRoom[roomID]
	if len(msgs) > limit {
		msgs = msgs[:limit]
	}
	return msgs, nil
}
func (f *fakeChat) Health(context.Context) error { return f.healthErr }

type fakeSignals struct {
	events   []string
	payloads map[string][]byte
}

func (f *fakeSignals) PublishRoom(_, event string, payload []byte) error {
	f.events = append(f.events, event)
	if f.payloads == nil {
		f.payloads = map[string][]byte{}
	}
	f.payloads[event] = payload
	return nil
}

// ---- harness ----

func newServer() (*Server, *fakeRooms, *fakeChat, *fakeSignals) {
	fr, fc, fs := newFakeRooms(), newFakeChat(), &fakeSignals{}
	return New(fr, fc, fs, &fakeAI{}, &fakeBet{}, []byte(testSecret), "test"), fr, fc, fs
}

func token(t *testing.T, userID string) string {
	t.Helper()
	tok, err := auth.Sign(auth.Claims{Sub: userID, Iss: auth.Issuer, Iat: time.Now().Unix(), Exp: time.Now().Add(time.Hour).Unix()}, []byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

const base = "/aurora.room.v1.RoomService/"

func post(t *testing.T, s *Server, method, body, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", base+method, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

// ---- tests ----

func TestCreateRoom_RequiresAuth(t *testing.T) {
	s, _, _, _ := newServer()
	if w := post(t, s, "CreateRoom", `{"name":"x"}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestRoomFlow(t *testing.T) {
	s, _, _, fs := newServer()
	tokA, tokB, tokC := token(t, "userA"), token(t, "userB"), token(t, "userC")

	// Create room (A is owner+member).
	w := post(t, s, "CreateRoom", `{"name":"saitama-stand"}`, tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("create room: %d %s", w.Code, w.Body.String())
	}
	var room roomJSON
	_ = json.NewDecoder(w.Body).Decode(&room)
	if room.ID == "" || room.OwnerUserID != "userA" {
		t.Fatalf("unexpected room: %+v", room)
	}
	rid := room.ID

	// B joins.
	w = post(t, s, "JoinRoom", `{"room_id":"`+rid+`"}`, tokB)
	if w.Code != http.StatusOK {
		t.Fatalf("join: %d %s", w.Code, w.Body.String())
	}
	var mem membershipJSON
	_ = json.NewDecoder(w.Body).Decode(&mem)
	if mem.Role != store.RoleMember {
		t.Errorf("expected MEMBER role, got %q", mem.Role)
	}

	// Members = 2.
	w = post(t, s, "ListMembers", `{"room_id":"`+rid+`"}`, tokA)
	var lm struct {
		Members []membershipJSON `json:"members"`
	}
	_ = json.NewDecoder(w.Body).Decode(&lm)
	if len(lm.Members) != 2 {
		t.Errorf("expected 2 members, got %d", len(lm.Members))
	}

	// Send + list message.
	w = post(t, s, "SendMessage", `{"room_id":"`+rid+`","body":"hello"}`, tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("send: %d %s", w.Code, w.Body.String())
	}
	w = post(t, s, "ListMessages", `{"room_id":"`+rid+`"}`, tokB)
	var lmsg struct {
		Messages []messageJSON `json:"messages"`
	}
	_ = json.NewDecoder(w.Body).Decode(&lmsg)
	if len(lmsg.Messages) != 1 || lmsg.Messages[0].Body != "hello" {
		t.Errorf("expected [hello], got %+v", lmsg.Messages)
	}

	// Non-member cannot send.
	if w := post(t, s, "SendMessage", `{"room_id":"`+rid+`","body":"hi"}`, tokC); w.Code != http.StatusForbidden {
		t.Fatalf("non-member send expected 403, got %d", w.Code)
	}

	// Typing publishes a NATS signal.
	if w := post(t, s, "Typing", `{"room_id":"`+rid+`"}`, tokA); w.Code != http.StatusOK {
		t.Fatalf("typing: %d", w.Code)
	}

	// Signals recorded: presence(join) + message + typing.
	var hasMessage, hasTyping bool
	for _, e := range fs.events {
		if e == "message" {
			hasMessage = true
		}
		if e == "typing" {
			hasTyping = true
		}
	}
	if !hasMessage || !hasTyping {
		t.Errorf("expected message+typing signals, got %v", fs.events)
	}
}

func TestJoin_RoomNotFound(t *testing.T) {
	s, _, _, _ := newServer()
	w := post(t, s, "JoinRoom", `{"room_id":"00000000-0000-0000-0000-0000000000ff"}`, token(t, "u1"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestSendMessage_InvalidBody(t *testing.T) {
	s, _, _, _ := newServer()
	tok := token(t, "u1")
	w := post(t, s, "CreateRoom", `{"name":"r"}`, tok)
	var room roomJSON
	_ = json.NewDecoder(w.Body).Decode(&room)
	if w := post(t, s, "SendMessage", `{"room_id":"`+room.ID+`","body":"   "}`, tok); w.Code != http.StatusBadRequest {
		t.Fatalf("empty body expected 400, got %d", w.Code)
	}
}

func TestInvalidRoomID(t *testing.T) {
	s, _, _, _ := newServer()
	if w := post(t, s, "JoinRoom", `{"room_id":"not-a-uuid"}`, token(t, "u1")); w.Code != http.StatusBadRequest {
		t.Fatalf("non-uuid room expected 400, got %d", w.Code)
	}
}

func TestHealthDegradedReturns503(t *testing.T) {
	fr, fc, fsig := newFakeRooms(), newFakeChat(), &fakeSignals{}
	fr.pingErr = context.DeadlineExceeded
	s := New(fr, fc, fsig, &fakeAI{}, &fakeBet{}, []byte(testSecret), "test")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}
}
