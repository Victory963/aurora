// Package server tests use a real Postgres if AURORA_TEST_DB is set,
// otherwise skip integration tests. This keeps unit tests fast on
// developer laptops without docker.
//
// Run with:
//   go test ./internal/server/...                    # skips DB tests
//   AURORA_TEST_DB=1 go test ./internal/server/...   # requires `make up`
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/dazn/aurora/services/identity-svc/internal/store"
)

func newTestServer(t *testing.T) (*Server, func()) {
	t.Helper()
	if os.Getenv("AURORA_TEST_DB") == "" {
		t.Skip("set AURORA_TEST_DB=1 to run integration tests (requires `make up`)")
	}

	// Read DB config from env so tests work in both `make up` and CI
	dsn := os.Getenv("AURORA_TEST_DSN")
	if dsn == "" {
		dsn = "host=localhost port=5432 user=aurora password=aurora_dev_password dbname=aurora_identity sslmode=disable"
	}

	ctx := context.Background()
	st, err := store.NewPostgres(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Clean slate
	if err := st.TruncateUsers(ctx); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	s := New(st, "test")
	return s, st.Close
}

func TestCreateUser_HappyPath(t *testing.T) {
	s, cleanup := newTestServer(t)
	defer cleanup()

	body := []byte(`{"email":"alice@example.com","display_name":"Alice","kyc_country":"GB"}`)
	req := httptest.NewRequest("POST", "/aurora.identity.v1.IdentityService/CreateUser", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp createUserResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.ID == "" {
		t.Fatal("expected user.id to be set")
	}
	if resp.User.Email != "alice@example.com" {
		t.Errorf("expected email alice@example.com, got %q", resp.User.Email)
	}
	if resp.User.KYCCountry != "GB" {
		t.Errorf("expected country GB, got %q", resp.User.KYCCountry)
	}
}

func TestCreateUser_InvalidEmail(t *testing.T) {
	// This test does not need a DB
	s := New(nil, "test")

	body := []byte(`{"email":"not-an-email","display_name":"X","kyc_country":"GB"}`)
	req := httptest.NewRequest("POST", "/aurora.identity.v1.IdentityService/CreateUser", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestCreateUser_InvalidJSON(t *testing.T) {
	s := New(nil, "test")
	req := httptest.NewRequest("POST", "/aurora.identity.v1.IdentityService/CreateUser",
		bytes.NewReader([]byte("{not json")))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// ---------- In-memory fake store: lets us unit-test handler + error mapping
// without a DB, so `make test` (no AURORA_TEST_DB) exercises real behavior. ----------

type fakeStore struct {
	byID      map[string]*store.User
	byEmail   map[string]string         // email -> id
	creds     map[string]string         // userID -> bcrypt hash
	devices   map[string]*store.Device  // deviceID -> device
	deviceKey map[string]string         // userID|platform|ua -> deviceID
	sessions  map[string]*store.Session // sessionID -> session
	byRefresh map[string]string         // refresh-hash -> sessionID
	pingErr   error
	seq       int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		byID: map[string]*store.User{}, byEmail: map[string]string{},
		creds: map[string]string{}, devices: map[string]*store.Device{},
		deviceKey: map[string]string{}, sessions: map[string]*store.Session{},
		byRefresh: map[string]string{},
	}
}

func (f *fakeStore) CreateUser(_ context.Context, email, displayName, kycCountry string) (*store.User, error) {
	if _, ok := f.byEmail[email]; ok {
		return nil, store.ErrEmailExists
	}
	f.seq++
	u := &store.User{
		ID:              fmt.Sprintf("00000000-0000-0000-0000-%012d", f.seq),
		Email:           email,
		DisplayName:     displayName,
		KYCCountry:      kycCountry,
		CreatedAtUnixMS: 1,
	}
	f.byID[u.ID] = u
	f.byEmail[email] = u.ID
	return u, nil
}

func (f *fakeStore) GetUser(_ context.Context, id string) (*store.User, error) {
	if u, ok := f.byID[id]; ok {
		return u, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) PingDB(context.Context) error            { return f.pingErr }
func (f *fakeStore) CountUsers(context.Context) (int, error) { return len(f.byID), nil }

// ---- AuthStore ----

func (f *fakeStore) SetPassword(_ context.Context, userID, hash string, _ int64) error {
	f.creds[userID] = hash
	return nil
}

func (f *fakeStore) GetPasswordHash(_ context.Context, userID string) (string, error) {
	if h, ok := f.creds[userID]; ok {
		return h, nil
	}
	return "", store.ErrNotFound
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	if id, ok := f.byEmail[email]; ok {
		return f.byID[id], nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) DeleteUser(_ context.Context, userID string) (bool, error) {
	u, ok := f.byID[userID]
	if !ok {
		return false, nil
	}
	delete(f.byID, userID)
	delete(f.byEmail, u.Email)
	delete(f.creds, userID)
	for did, d := range f.devices {
		if d.UserID == userID {
			delete(f.devices, did)
		}
	}
	for k := range f.deviceKey {
		if strings.HasPrefix(k, userID+"|") {
			delete(f.deviceKey, k)
		}
	}
	for sid, sess := range f.sessions {
		if sess.UserID == userID {
			delete(f.byRefresh, sess.RefreshTokenHash)
			delete(f.sessions, sid)
		}
	}
	return true, nil
}

func (f *fakeStore) UpsertDevice(_ context.Context, userID, platform, userAgent string, nowMS int64) (*store.Device, error) {
	key := userID + "|" + platform + "|" + userAgent
	if did, ok := f.deviceKey[key]; ok {
		d := f.devices[did]
		d.LastSeenUnixMS = nowMS
		return d, nil
	}
	f.seq++
	d := &store.Device{
		ID:              fmt.Sprintf("d0000000-0000-0000-0000-%012d", f.seq),
		UserID:          userID,
		Platform:        platform,
		UserAgent:       userAgent,
		CreatedAtUnixMS: nowMS,
		LastSeenUnixMS:  nowMS,
	}
	f.devices[d.ID] = d
	f.deviceKey[key] = d.ID
	return d, nil
}

func (f *fakeStore) ListDevices(_ context.Context, userID string) ([]*store.Device, error) {
	var out []*store.Device
	for _, d := range f.devices {
		if d.UserID == userID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeStore) CreateSession(_ context.Context, sess *store.Session) error {
	f.sessions[sess.ID] = sess
	f.byRefresh[sess.RefreshTokenHash] = sess.ID
	return nil
}

func (f *fakeStore) GetSessionByRefreshHash(_ context.Context, hash string) (*store.Session, error) {
	if sid, ok := f.byRefresh[hash]; ok {
		return f.sessions[sid], nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) RotateSessionRefresh(_ context.Context, sessionID, oldHash, newHash string, lastUsed int64) error {
	sess, ok := f.sessions[sessionID]
	if !ok || sess.RevokedAtUnixMS != 0 || sess.RefreshTokenHash != oldHash {
		return store.ErrNotFound
	}
	delete(f.byRefresh, sess.RefreshTokenHash)
	sess.RefreshTokenHash = newHash
	sess.LastUsedAtUnixMS = lastUsed
	f.byRefresh[newHash] = sessionID
	return nil
}

func (f *fakeStore) RevokeSession(_ context.Context, sessionID string, revokedAt int64) error {
	sess, ok := f.sessions[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	sess.RevokedAtUnixMS = revokedAt
	return nil
}

func postJSON(t *testing.T, s *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func TestCreateUser_HappyPath_NoDB(t *testing.T) {
	s := New(newFakeStore(), "test")
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/CreateUser",
		`{"email":"Alice@Example.com","display_name":"Alice","kyc_country":"gb"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp createUserResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.User.ID == "" {
		t.Fatal("expected user.id to be set")
	}
	// Handler normalizes email to lowercase and country to uppercase.
	if resp.User.Email != "alice@example.com" {
		t.Errorf("email not normalized: got %q", resp.User.Email)
	}
	if resp.User.KYCCountry != "GB" {
		t.Errorf("country not normalized: got %q", resp.User.KYCCountry)
	}
}

func TestCreateUser_DuplicateEmail_NoDB(t *testing.T) {
	s := New(newFakeStore(), "test")
	body := `{"email":"dup@example.com","display_name":"Dup","kyc_country":"GB"}`
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/CreateUser", body); w.Code != http.StatusOK {
		t.Fatalf("first create expected 200, got %d", w.Code)
	}
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/CreateUser", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate create expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var er errorResponse
	_ = json.NewDecoder(w.Body).Decode(&er)
	if er.Code != "email_exists" {
		t.Errorf("expected code email_exists, got %q", er.Code)
	}
}

func TestGetUser_NotFound_NoDB(t *testing.T) {
	s := New(newFakeStore(), "test")
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/GetUser",
		`{"id":"00000000-0000-0000-0000-000000000999"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestGetUser_InvalidUUID_NoDB(t *testing.T) {
	s := New(newFakeStore(), "test")
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/GetUser", `{"id":"not-a-uuid"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-UUID id, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHealthCheck_DegradedReturns503_NoLeak_NoDB(t *testing.T) {
	fs := newFakeStore()
	fs.pingErr = errors.New("failed to connect to host=secret-db port=5432 user=aurora")
	s := New(fs, "test")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when DB down, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "secret-db") {
		t.Errorf("health body leaked raw DB error: %s", w.Body.String())
	}
}

func TestHealthCheck_OK_NoDB(t *testing.T) {
	s := New(newFakeStore(), "test")
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}
