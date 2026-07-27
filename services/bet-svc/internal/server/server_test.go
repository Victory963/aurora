package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dazn/aurora/services/bet-svc/internal/auth"
	"github.com/dazn/aurora/services/bet-svc/internal/clients"
	"github.com/dazn/aurora/services/bet-svc/internal/store"
)

const testSecret = "test-secret"

// ---------------- fakes ----------------

type fakeStore struct {
	pools map[string]*store.Pool
	bets  map[string]*store.Bet // by id
	byKey map[string]string     // idem key -> bet id
	seq   int
}

func newFakeStore() *fakeStore {
	return &fakeStore{pools: map[string]*store.Pool{}, bets: map[string]*store.Bet{}, byKey: map[string]string{}}
}

func (f *fakeStore) CreatePool(_ context.Context, p *store.Pool) error {
	for i := range p.Options {
		f.seq++
		p.Options[i].ID = fmt.Sprintf("00000000-0000-0000-0000-%012d", f.seq)
	}
	cp := *p
	f.pools[p.ID] = &cp
	return nil
}

func (f *fakeStore) GetPool(_ context.Context, id string) (*store.Pool, error) {
	p, ok := f.pools[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	// rebuild aggregates like the SQL does
	out := *p
	out.Options = append([]store.Option(nil), p.Options...)
	out.TotalStakeMinor = 0
	for i := range out.Options {
		out.Options[i].StakeMinor, out.Options[i].Bettors = 0, 0
	}
	for _, b := range f.bets {
		if b.PoolID != id {
			continue
		}
		for i := range out.Options {
			if out.Options[i].ID == b.OptionID {
				out.Options[i].StakeMinor += b.StakeMinor
				out.Options[i].Bettors++
			}
		}
		out.TotalStakeMinor += b.StakeMinor
	}
	return &out, nil
}

func (f *fakeStore) ListRoomPools(_ context.Context, roomID string) ([]*store.Pool, error) {
	var out []*store.Pool
	for id, p := range f.pools {
		if p.RoomID == roomID {
			got, _ := f.GetPool(context.Background(), id)
			out = append(out, got)
		}
	}
	return out, nil
}

func (f *fakeStore) GetBetByKey(_ context.Context, key string) (*store.Bet, error) {
	if id, ok := f.byKey[key]; ok {
		return f.bets[id], nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) InsertBetIfOpen(_ context.Context, b *store.Bet) (bool, error) {
	if _, dup := f.byKey[b.IdempotencyKey]; dup {
		return false, store.ErrDuplicateKey
	}
	p, ok := f.pools[b.PoolID]
	if !ok || p.Status != store.PoolOpen {
		return false, nil
	}
	cp := *b
	f.bets[b.ID] = &cp
	f.byKey[b.IdempotencyKey] = b.ID
	return true, nil
}

func (f *fakeStore) MarkSettling(_ context.Context, poolID, winning string) error {
	p := f.pools[poolID]
	if p.Status == store.PoolOpen || (p.Status == store.PoolSettling && p.WinningOptionID == winning) {
		p.Status, p.WinningOptionID = store.PoolSettling, winning
		return nil
	}
	return store.ErrBadTransition
}

func (f *fakeStore) ListPoolBets(_ context.Context, poolID string) ([]*store.Bet, error) {
	var out []*store.Bet
	for _, b := range f.bets {
		if b.PoolID == poolID {
			out = append(out, b)
		}
	}
	return out, nil
}

func (f *fakeStore) FinalizeSettlement(_ context.Context, poolID string,
	results map[string]store.BetOutcome, settledAt int64) error {
	for id, r := range results {
		f.bets[id].Status, f.bets[id].PayoutMinor = r.Status, r.PayoutMinor
	}
	p := f.pools[poolID]
	p.Status, p.SettledAtUnixMS = store.PoolSettled, settledAt
	return nil
}

func (f *fakeStore) PingDB(context.Context) error { return nil }

// walletOp mirrors the real wallet's recorded transaction: a replay must match
// user+amount+type exactly or it is an idempotency conflict (M3 store fix).
type walletOp struct {
	user   string
	amount int64
	kind   string // "debit" | "credit"
}

type fakeWallet struct {
	balances  map[string]int64
	ops       map[string]walletOp // by idempotency key
	credits   []string            // credit keys, in order
	creditErr error               // force the next Credit to fail (then clears)
}

func newFakeWallet() *fakeWallet {
	return &fakeWallet{balances: map[string]int64{}, ops: map[string]walletOp{}}
}

func (f *fakeWallet) Debit(_ context.Context, user string, amount int64, key, _ string) error {
	if op, ok := f.ops[key]; ok {
		if op.user != user || op.amount != amount || op.kind != "debit" {
			return clients.ErrIdempotencyConflict
		}
		return nil // idempotent replay, no money moves
	}
	if f.balances[user] < amount {
		return clients.ErrInsufficientFunds
	}
	f.ops[key] = walletOp{user, amount, "debit"}
	f.balances[user] -= amount
	return nil
}

func (f *fakeWallet) Credit(_ context.Context, user string, amount int64, key, _ string) error {
	if f.creditErr != nil {
		err := f.creditErr
		f.creditErr = nil
		return err
	}
	if op, ok := f.ops[key]; ok {
		if op.user != user || op.amount != amount || op.kind != "credit" {
			return clients.ErrIdempotencyConflict
		}
		return nil
	}
	f.ops[key] = walletOp{user, amount, "credit"}
	f.balances[user] += amount
	f.credits = append(f.credits, key)
	return nil
}

type fakeRooms struct{ members map[string]bool } // "roomID|sub" -> member

func (f *fakeRooms) CheckMember(_ context.Context, roomID, bearer string) error {
	claims, err := auth.Verify(bearer, []byte(testSecret), time.Now())
	if err != nil {
		return clients.ErrNotMember
	}
	if f.members[roomID+"|"+claims.Sub] {
		return nil
	}
	return clients.ErrNotMember
}

// ---------------- harness ----------------

const roomID = "11111111-1111-1111-1111-111111111111"

func newHarness() (*Server, *fakeStore, *fakeWallet, *fakeRooms) {
	fs, fw := newFakeStore(), newFakeWallet()
	fr := &fakeRooms{members: map[string]bool{roomID + "|userA": true, roomID + "|userB": true}}
	s := New(fs, fw, fr, nil, []byte(testSecret), 500, "test")
	return s, fs, fw, fr
}

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
	req := httptest.NewRequest("POST", "/aurora.bet.v1.BetService/"+method, bytes.NewReader([]byte(body)))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func createPool(t *testing.T, s *Server, tok string) poolJSON {
	t.Helper()
	w := post(t, s, "CreatePool",
		`{"room_id":"`+roomID+`","question":"home scores in 15m?","options":[{"label":"YES","odds_x100":220},{"label":"NO","odds_x100":183}]}`, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("create pool: %d %s", w.Code, w.Body.String())
	}
	var p poolJSON
	_ = json.NewDecoder(w.Body).Decode(&p)
	if len(p.Options) != 2 || p.Status != "OPEN" {
		t.Fatalf("bad pool: %+v", p)
	}
	return p
}

func optionID(p poolJSON, label string) string {
	for _, o := range p.Options {
		if o.Label == label {
			return o.ID
		}
	}
	return ""
}

// ---------------- tests ----------------

func TestAuthRequired(t *testing.T) {
	s, _, _, _ := newHarness()
	if w := post(t, s, "CreatePool", `{}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestFullFlow_PlaceReplaySettle(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokA, tokB := token(t, "userA"), token(t, "userB")
	fw.balances["userA"], fw.balances["userB"] = 5000, 5000

	p := createPool(t, s, tokA)
	yes, no := optionID(p, "YES"), optionID(p, "NO")

	// A bets 1000 on YES.
	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":1000,"idempotency_key":"kA"}`, tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("bet A: %d %s", w.Code, w.Body.String())
	}
	if fw.balances["userA"] != 4000 {
		t.Fatalf("A balance after bet = %d, want 4000", fw.balances["userA"])
	}

	// Idempotent replay: same key, no double debit, replayed=true.
	w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":1000,"idempotency_key":"kA"}`, tokA)
	var rb betJSON
	_ = json.NewDecoder(w.Body).Decode(&rb)
	if w.Code != http.StatusOK || !rb.Replayed || fw.balances["userA"] != 4000 {
		t.Fatalf("replay wrong: code=%d replayed=%v balance=%d", w.Code, rb.Replayed, fw.balances["userA"])
	}

	// Same key, different params => 409.
	w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+no+`","stake_minor":50,"idempotency_key":"kA"}`, tokA)
	if w.Code != http.StatusConflict {
		t.Fatalf("key conflict expected 409, got %d", w.Code)
	}

	// B bets 600 on NO.
	w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+no+`","stake_minor":600,"idempotency_key":"kB"}`, tokB)
	if w.Code != http.StatusOK {
		t.Fatalf("bet B: %d %s", w.Code, w.Body.String())
	}

	// Only the creator may settle.
	if w = post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+yes+`"}`, tokB); w.Code != http.StatusForbidden {
		t.Fatalf("non-creator settle expected 403, got %d", w.Code)
	}

	// Settle YES: A gets 1000 + (600 - 5% rake) = 1570 → balance 5570.
	w = post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+yes+`"}`, tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	var settled poolJSON
	_ = json.NewDecoder(w.Body).Decode(&settled)
	if settled.Status != "SETTLED" || settled.WinningOptionID != yes {
		t.Fatalf("pool after settle: %+v", settled)
	}
	if fw.balances["userA"] != 5570 {
		t.Fatalf("A after settle = %d, want 5570", fw.balances["userA"])
	}
	if fw.balances["userB"] != 4400 {
		t.Fatalf("B after settle = %d, want 4400", fw.balances["userB"])
	}

	// Settle replay: idempotent, no extra credits.
	credits := len(fw.credits)
	w = post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+yes+`"}`, tokA)
	if w.Code != http.StatusOK || len(fw.credits) != credits || fw.balances["userA"] != 5570 {
		t.Fatalf("settle replay changed money: code=%d credits=%d balance=%d", w.Code, len(fw.credits), fw.balances["userA"])
	}

	// Different outcome after settle => 409.
	if w = post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+no+`"}`, tokA); w.Code != http.StatusConflict {
		t.Fatalf("conflicting settle expected 409, got %d", w.Code)
	}

	// Betting into a settled pool => 409, and the debit is not kept.
	before := fw.balances["userB"]
	if w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+no+`","stake_minor":100,"idempotency_key":"late"}`, tokB); w.Code != http.StatusConflict {
		t.Fatalf("late bet expected 409, got %d", w.Code)
	}
	if fw.balances["userB"] != before {
		t.Fatalf("late bet moved money: %d -> %d", before, fw.balances["userB"])
	}
}

func TestPlaceBet_InsufficientFunds(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokA := token(t, "userA")
	fw.balances["userA"] = 10
	p := createPool(t, s, tokA)
	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+optionID(p, "YES")+`","stake_minor":1000,"idempotency_key":"k1"}`, tokA)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "insufficient_funds") {
		t.Fatalf("expected insufficient_funds code: %s", w.Body.String())
	}
}

func TestNonMemberForbidden(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokC := token(t, "userC") // not in fakeRooms.members
	fw.balances["userC"] = 5000
	tokA := token(t, "userA")
	p := createPool(t, s, tokA)
	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+optionID(p, "YES")+`","stake_minor":100,"idempotency_key":"kc"}`, tokC)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-member expected 403, got %d", w.Code)
	}
	if fw.balances["userC"] != 5000 {
		t.Fatalf("non-member was debited")
	}
}

func TestNoWinners_RefundsEveryone(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokA := token(t, "userA")
	fw.balances["userA"] = 1000
	p := createPool(t, s, tokA)
	yes, no := optionID(p, "YES"), optionID(p, "NO")
	post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+no+`","stake_minor":400,"idempotency_key":"r1"}`, tokA)
	if fw.balances["userA"] != 600 {
		t.Fatalf("debit missing")
	}
	w := post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+yes+`"}`, tokA)
	if w.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}
	if fw.balances["userA"] != 1000 {
		t.Fatalf("refund missing: %d", fw.balances["userA"])
	}
}

func TestInvalidPoolID(t *testing.T) {
	s, _, _, _ := newHarness()
	w := post(t, s, "GetPool", `{"pool_id":"not-a-uuid"}`, token(t, "userA"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// Regression (review finding): a debit that succeeded while its bet insert
// crashed must be refunded when the retry arrives after the pool settled —
// previously the pre-debit OPEN gate short-circuited and the stake was lost.
func TestPlaceBet_OrphanedDebitRefundedAfterPoolCloses(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokA, tokB := token(t, "userA"), token(t, "userB")
	fw.balances["userA"], fw.balances["userB"] = 5000, 5000

	p := createPool(t, s, tokA)
	yes := optionID(p, "YES")

	// Simulate attempt #1: wallet debit landed, bet insert crashed (no bet row).
	if err := fw.Debit(context.Background(), "userB", 300, "orphan", "bet:"+p.ID); err != nil {
		t.Fatal(err)
	}
	if fw.balances["userB"] != 4700 {
		t.Fatalf("setup debit missing")
	}

	// Pool settles before the client retries.
	post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":100,"idempotency_key":"kA"}`, tokA)
	if w := post(t, s, "SettlePool", `{"pool_id":"`+p.ID+`","winning_option_id":"`+yes+`"}`, tokA); w.Code != http.StatusOK {
		t.Fatalf("settle: %d %s", w.Code, w.Body.String())
	}

	// Retry with the same key: 409 pool_closed AND the orphaned stake comes back.
	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":300,"idempotency_key":"orphan"}`, tokB)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d %s", w.Code, w.Body.String())
	}
	if fw.balances["userB"] != 5000 {
		t.Fatalf("orphaned debit not refunded: balance %d, want 5000", fw.balances["userB"])
	}

	// The recovery is itself idempotent: another retry moves no more money.
	w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":300,"idempotency_key":"orphan"}`, tokB)
	if w.Code != http.StatusConflict || fw.balances["userB"] != 5000 {
		t.Fatalf("recovery not idempotent: code=%d balance=%d", w.Code, fw.balances["userB"])
	}
}

// Regression (review finding): the pool-closed void refund must not be
// fire-and-forget — a failed credit returns a retryable error, and the retry
// completes the refund instead of claiming it already happened.
func TestPlaceBet_VoidCreditFailureIsRetryable(t *testing.T) {
	s, fs, fw, _ := newHarness()
	tokA := token(t, "userA")
	fw.balances["userA"] = 5000

	p := createPool(t, s, tokA)
	yes := optionID(p, "YES")

	// Force the pool to close between the handler's status check and the
	// insert: the fake store flips status via MarkSettling after GetPool ran.
	// Simplest deterministic simulation: mark the pool SETTLING now and let the
	// closed-pool recovery path run with a failing first credit.
	if err := fs.MarkSettling(context.Background(), p.ID, yes); err != nil {
		t.Fatal(err)
	}
	fw.creditErr = errors.New("wallet momentarily down")

	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":200,"idempotency_key":"vk"}`, tokA)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("failed void expected 502 refund_pending, got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "refund_pending") {
		t.Fatalf("expected refund_pending code: %s", w.Body.String())
	}
	if fw.balances["userA"] != 4800 {
		t.Fatalf("debit should be held pending refund: %d", fw.balances["userA"])
	}

	// Retry with the same key completes the refund.
	w = post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":200,"idempotency_key":"vk"}`, tokA)
	if w.Code != http.StatusConflict {
		t.Fatalf("retry expected 409 pool_closed, got %d %s", w.Code, w.Body.String())
	}
	if fw.balances["userA"] != 5000 {
		t.Fatalf("refund not completed on retry: %d", fw.balances["userA"])
	}
}

// Regression (review finding): reusing an idempotency key with a different
// amount in the debit-succeeded-but-not-inserted window must be rejected by
// the wallet's parameter check, not silently accepted.
func TestPlaceBet_KeyReuseDifferentAmountPreInsertWindow(t *testing.T) {
	s, _, fw, _ := newHarness()
	tokA := token(t, "userA")
	fw.balances["userA"] = 5000

	p := createPool(t, s, tokA)
	yes := optionID(p, "YES")

	// Attempt #1 debited 100 with key kx but never inserted a bet.
	if err := fw.Debit(context.Background(), "userA", 100, "kx", "bet:"+p.ID); err != nil {
		t.Fatal(err)
	}

	// Attempt #2 reuses kx with stake 200: wallet rejects the mismatched replay.
	w := post(t, s, "PlaceBet", `{"pool_id":"`+p.ID+`","option_id":"`+yes+`","stake_minor":200,"idempotency_key":"kx"}`, tokA)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 idempotency_conflict, got %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "idempotency_conflict") {
		t.Fatalf("expected idempotency_conflict code: %s", w.Body.String())
	}
	if fw.balances["userA"] != 4900 {
		t.Fatalf("no extra money should move: %d", fw.balances["userA"])
	}
}
