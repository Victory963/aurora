package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dazn/aurora/services/identity-svc/internal/events"
)

func newAuthServer(opts ...Option) (*Server, *fakeStore) {
	fs := newFakeStore()
	base := []Option{WithAuth([]byte("test-secret"), 15*time.Minute, 720*time.Hour)}
	s := New(fs, "test", append(base, opts...)...)
	return s, fs
}

func postJSONAuth(t *testing.T, s *Server, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Routes().ServeHTTP(w, req)
	return w
}

func createUserWithPassword(t *testing.T, s *Server, email, password string) string {
	t.Helper()
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/CreateUser",
		`{"email":"`+email+`","display_name":"X","kyc_country":"GB","password":"`+password+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create user: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp createUserResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp.User.ID
}

type recordingPublisher struct {
	createdUserID string
	deletedUserID string
}

func (r *recordingPublisher) UserCreated(_ context.Context, e events.UserCreated) error {
	r.createdUserID = e.UserID
	return nil
}
func (r *recordingPublisher) UserDeleted(_ context.Context, e events.UserDeleted) error {
	r.deletedUserID = e.UserID
	return nil
}
func (r *recordingPublisher) Close() error { return nil }

func TestCreateUser_ShortPassword(t *testing.T) {
	s, _ := newAuthServer()
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/CreateUser",
		`{"email":"p@e.com","display_name":"P","kyc_country":"GB","password":"short"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for short password, got %d", w.Code)
	}
}

func TestAuthFlow_LoginGetMeRefreshLogout(t *testing.T) {
	s, _ := newAuthServer()
	uid := createUserWithPassword(t, s, "auth@example.com", "supersecret")

	// Login.
	login := postJSON(t, s, "/aurora.identity.v1.IdentityService/Login",
		`{"email":"auth@example.com","password":"supersecret","device":{"platform":"web","user_agent":"smoke"}}`)
	if login.Code != http.StatusOK {
		t.Fatalf("login expected 200, got %d: %s", login.Code, login.Body.String())
	}
	var lr loginResponse
	if err := json.NewDecoder(login.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}
	if lr.AccessToken == "" || lr.RefreshToken == "" {
		t.Fatal("login did not return both tokens")
	}
	if lr.User.ID != uid {
		t.Errorf("login user id = %q, want %q", lr.User.ID, uid)
	}

	// GetMe without a token → 401.
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/GetMe", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("GetMe without token expected 401, got %d", w.Code)
	}
	// GetMe with the access token → 200 and the right user.
	me := postJSONAuth(t, s, "/aurora.identity.v1.IdentityService/GetMe", `{}`, lr.AccessToken)
	if me.Code != http.StatusOK {
		t.Fatalf("GetMe expected 200, got %d: %s", me.Code, me.Body.String())
	}
	var mr getUserResponse
	_ = json.NewDecoder(me.Body).Decode(&mr)
	if mr.User.ID != uid {
		t.Errorf("GetMe returned user %q, want %q", mr.User.ID, uid)
	}

	// ListDevices → the login device.
	dl := postJSONAuth(t, s, "/aurora.identity.v1.IdentityService/ListDevices", `{}`, lr.AccessToken)
	if dl.Code != http.StatusOK {
		t.Fatalf("ListDevices expected 200, got %d", dl.Code)
	}
	var dr listDevicesResponse
	_ = json.NewDecoder(dl.Body).Decode(&dr)
	if len(dr.Devices) < 1 || dr.Devices[0].Platform != "web" {
		t.Errorf("expected a 'web' device, got %+v", dr.Devices)
	}

	// Refresh → new (rotated) tokens.
	rf := postJSON(t, s, "/aurora.identity.v1.IdentityService/RefreshToken",
		`{"refresh_token":"`+lr.RefreshToken+`"}`)
	if rf.Code != http.StatusOK {
		t.Fatalf("refresh expected 200, got %d: %s", rf.Code, rf.Body.String())
	}
	var tb tokenBundle
	_ = json.NewDecoder(rf.Body).Decode(&tb)
	if tb.AccessToken == "" || tb.RefreshToken == "" {
		t.Fatal("refresh did not return tokens")
	}
	if tb.RefreshToken == lr.RefreshToken {
		t.Error("refresh token was not rotated")
	}

	// Old refresh token is now invalid (rotation defeats replay).
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/RefreshToken",
		`{"refresh_token":"`+lr.RefreshToken+`"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("old refresh expected 401, got %d", w.Code)
	}

	// Logout the current refresh token, then refresh is rejected.
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/Logout",
		`{"refresh_token":"`+tb.RefreshToken+`"}`); w.Code != http.StatusOK {
		t.Fatalf("logout expected 200, got %d", w.Code)
	}
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/RefreshToken",
		`{"refresh_token":"`+tb.RefreshToken+`"}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout expected 401, got %d", w.Code)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	s, _ := newAuthServer()
	createUserWithPassword(t, s, "wp@example.com", "supersecret")
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/Login",
		`{"email":"wp@example.com","password":"WRONGWRONG","device":{"platform":"web"}}`)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password expected 401, got %d", w.Code)
	}
}

func TestLogin_UnknownUser_NoEnumeration(t *testing.T) {
	s, _ := newAuthServer()
	w := postJSON(t, s, "/aurora.identity.v1.IdentityService/Login",
		`{"email":"ghost@example.com","password":"whatever1","device":{}}`)
	// 401 invalid_credentials (NOT 404) so callers can't probe which emails exist.
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user expected 401, got %d", w.Code)
	}
}

func TestDeleteUser_RequiresAuthAndPublishesEvent(t *testing.T) {
	rec := &recordingPublisher{}
	s, _ := newAuthServer(WithPublisher(rec))
	uid := createUserWithPassword(t, s, "del@example.com", "supersecret")

	login := postJSON(t, s, "/aurora.identity.v1.IdentityService/Login",
		`{"email":"del@example.com","password":"supersecret","device":{"platform":"web"}}`)
	var lr loginResponse
	_ = json.NewDecoder(login.Body).Decode(&lr)

	// Without auth → 401.
	if w := postJSON(t, s, "/aurora.identity.v1.IdentityService/DeleteUser", `{}`); w.Code != http.StatusUnauthorized {
		t.Fatalf("delete without token expected 401, got %d", w.Code)
	}
	// With auth → 200 and a deleted event for this user.
	if w := postJSONAuth(t, s, "/aurora.identity.v1.IdentityService/DeleteUser", `{}`, lr.AccessToken); w.Code != http.StatusOK {
		t.Fatalf("delete expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if rec.deletedUserID != uid {
		t.Errorf("expected user.deleted event for %q, got %q", uid, rec.deletedUserID)
	}
	// GetMe now 404 (user is gone; token still cryptographically valid).
	if w := postJSONAuth(t, s, "/aurora.identity.v1.IdentityService/GetMe", `{}`, lr.AccessToken); w.Code != http.StatusNotFound {
		t.Fatalf("GetMe after delete expected 404, got %d", w.Code)
	}
}
