// Package server implements room-svc's HTTP handlers (Connect-RPC URL style).
// All endpoints except HealthCheck require an Authorization: Bearer access token
// (HS256, shared secret with identity-svc; ADR-0005 §3).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/dazn/aurora/services/room-svc/internal/auth"
	"github.com/dazn/aurora/services/room-svc/internal/chat"
	"github.com/dazn/aurora/services/room-svc/internal/clients"
	"github.com/dazn/aurora/services/room-svc/internal/store"
)

const (
	defaultMessageLimit = 50
	maxMessageLimit     = 200
	maxRoomNameLen      = 120
	maxBodyLen          = 4000

	// AIUserID is the reserved platform user_id for AI-authored chat messages
	// (ADR-0007). It is NOT registered in identity-svc and is not a room member;
	// clients render it with an AI badge.
	AIUserID = "aurora-ai"
)

// RoomStore is the room/membership persistence surface.
type RoomStore interface {
	CreateRoom(ctx context.Context, id, name, ownerID string, nowUnixMS int64) (*store.Room, error)
	GetRoom(ctx context.Context, id string) (*store.Room, error)
	AddMember(ctx context.Context, roomID, userID, role string, nowUnixMS int64) error
	GetMembership(ctx context.Context, roomID, userID string) (*store.Membership, error)
	RemoveMember(ctx context.Context, roomID, userID string) (bool, error)
	IsMember(ctx context.Context, roomID, userID string) (bool, error)
	ListMembers(ctx context.Context, roomID string) ([]*store.Membership, error)
	ListRoomsForUser(ctx context.Context, userID string) ([]*store.Room, error)
	PingDB(ctx context.Context) error
}

// ChatStore is the message persistence surface (ScyllaDB).
type ChatStore interface {
	InsertMessage(ctx context.Context, roomID, userID, body string, nowUnixMS int64) (*chat.Message, error)
	ListMessages(ctx context.Context, roomID string, limit int) ([]*chat.Message, error)
	Health(ctx context.Context) error
}

// Signals publishes ephemeral room signals (NATS).
type Signals interface {
	PublishRoom(roomID, event string, payload []byte) error
}

// AIRecommender calls ai-agent-svc for ROOM-mode recommendations (M7).
type AIRecommender interface {
	Recommend(ctx context.Context, userID, matchID, roomID string) ([]clients.Recommendation, error)
	Enabled() bool
}

// PoolCreator calls bet-svc to create a room-scoped pool as the caller (M7).
type PoolCreator interface {
	CreatePool(ctx context.Context, bearer, roomID, question string, options []clients.NewOption) (*clients.Pool, error)
	Enabled() bool
}

type Server struct {
	rooms     RoomStore
	chat      ChatStore
	signals   Signals
	ai        AIRecommender
	bet       PoolCreator
	jwtSecret []byte
	version   string
}

func New(rooms RoomStore, chatStore ChatStore, signals Signals, ai AIRecommender, bet PoolCreator, jwtSecret []byte, version string) *Server {
	return &Server{rooms: rooms, chat: chatStore, signals: signals, ai: ai, bet: bet, jwtSecret: jwtSecret, version: version}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	p := "/aurora.room.v1.RoomService/"
	mux.HandleFunc("POST "+p+"CreateRoom", s.handleCreateRoom)
	mux.HandleFunc("POST "+p+"ListRooms", s.handleListRooms)
	mux.HandleFunc("POST "+p+"JoinRoom", s.handleJoinRoom)
	mux.HandleFunc("POST "+p+"LeaveRoom", s.handleLeaveRoom)
	mux.HandleFunc("POST "+p+"ListMembers", s.handleListMembers)
	mux.HandleFunc("POST "+p+"SendMessage", s.handleSendMessage)
	mux.HandleFunc("POST "+p+"ListMessages", s.handleListMessages)
	mux.HandleFunc("POST "+p+"Typing", s.handleTyping)
	mux.HandleFunc("POST "+p+"ProposeAiPool", s.handleProposeAiPool)
	mux.HandleFunc("POST "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET "+p+"HealthCheck", s.handleHealthCheck)
	mux.HandleFunc("GET /healthz", s.handleHealthCheck)
	return logMiddleware(mux)
}

// ---------- JSON types ----------

type roomJSON struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	OwnerUserID     string `json:"owner_user_id"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

type membershipJSON struct {
	RoomID         string `json:"room_id"`
	UserID         string `json:"user_id"`
	Role           string `json:"role"`
	JoinedAtUnixMS int64  `json:"joined_at_unix_ms"`
}

type messageJSON struct {
	ID              string `json:"id"`
	RoomID          string `json:"room_id"`
	UserID          string `json:"user_id"`
	Body            string `json:"body"`
	CreatedAtUnixMS int64  `json:"created_at_unix_ms"`
}

type createRoomRequest struct {
	Name string `json:"name"`
}
type roomIDRequest struct {
	RoomID string `json:"room_id"`
}
type sendMessageRequest struct {
	RoomID string `json:"room_id"`
	Body   string `json:"body"`
}
type listMessagesRequest struct {
	RoomID string `json:"room_id"`
	Limit  int    `json:"limit"`
}
type okResponse struct {
	OK bool `json:"ok"`
}
type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ---------- handlers ----------

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req createRoomRequest
	if !decode(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > maxRoomNameLen {
		writeError(w, http.StatusBadRequest, "invalid_name", "name required (1-120 chars)")
		return
	}
	room, err := s.rooms.CreateRoom(ctx, uuid.NewString(), name, userID, time.Now().UnixMilli())
	if err != nil {
		slog.Error("createRoom failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create room")
		return
	}
	writeJSON(w, http.StatusOK, roomToJSON(room))
}

func (s *Server) handleListRooms(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	rooms, err := s.rooms.ListRoomsForUser(ctx, userID)
	if err != nil {
		slog.Error("listRooms failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list rooms")
		return
	}
	out := make([]roomJSON, 0, len(rooms))
	for _, rm := range rooms {
		out = append(out, roomToJSON(rm))
	}
	writeJSON(w, http.StatusOK, map[string]any{"rooms": out})
}

func (s *Server) handleJoinRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req roomIDRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	if _, err := s.rooms.GetRoom(ctx, roomID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "room_not_found", "room not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "join failed")
		return
	}
	if err := s.rooms.AddMember(ctx, roomID, userID, store.RoleMember, time.Now().UnixMilli()); err != nil {
		slog.Error("joinRoom failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "join failed")
		return
	}
	m, err := s.rooms.GetMembership(ctx, roomID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "join failed")
		return
	}
	s.publish(roomID, "presence", map[string]string{"user_id": userID, "action": "join"})
	writeJSON(w, http.StatusOK, membershipToJSON(m))
}

func (s *Server) handleLeaveRoom(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req roomIDRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	removed, err := s.rooms.RemoveMember(ctx, roomID, userID)
	if err != nil {
		slog.Error("leaveRoom failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "leave failed")
		return
	}
	if removed {
		s.publish(roomID, "presence", map[string]string{"user_id": userID, "action": "leave"})
	}
	writeJSON(w, http.StatusOK, okResponse{OK: removed})
}

func (s *Server) handleListMembers(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req roomIDRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	if !s.requireMember(ctx, w, roomID, userID) {
		return
	}
	members, err := s.rooms.ListMembers(ctx, roomID)
	if err != nil {
		slog.Error("listMembers failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list members")
		return
	}
	out := make([]membershipJSON, 0, len(members))
	for _, m := range members {
		out = append(out, membershipToJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": out})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req sendMessageRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	if strings.TrimSpace(req.Body) == "" || len(req.Body) > maxBodyLen {
		writeError(w, http.StatusBadRequest, "invalid_body", "body required (1-4000 chars)")
		return
	}
	if !s.requireMember(ctx, w, roomID, userID) {
		return
	}
	msg, err := s.chat.InsertMessage(ctx, roomID, userID, req.Body, time.Now().UnixMilli())
	if err != nil {
		slog.Error("sendMessage failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not send message")
		return
	}
	mj := messageToJSON(msg)
	s.publish(roomID, "message", mj)
	writeJSON(w, http.StatusOK, mj)
}

func (s *Server) handleListMessages(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req listMessagesRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	if !s.requireMember(ctx, w, roomID, userID) {
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultMessageLimit
	}
	if limit > maxMessageLimit {
		limit = maxMessageLimit
	}
	msgs, err := s.chat.ListMessages(ctx, roomID, limit)
	if err != nil {
		slog.Error("listMessages failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list messages")
		return
	}
	out := make([]messageJSON, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageToJSON(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": out})
}

func (s *Server) handleTyping(w http.ResponseWriter, r *http.Request) {
	userID, ok := s.requireUser(w, r)
	if !ok {
		return
	}
	ctx, cancel := ctx5(r)
	defer cancel()

	var req roomIDRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	if !s.requireMember(ctx, w, roomID, userID) {
		return
	}
	s.publish(roomID, "typing", map[string]string{"user_id": userID})
	writeJSON(w, http.StatusOK, okResponse{OK: true})
}

func (s *Server) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	status, httpStatus := "ok", http.StatusOK
	details := map[string]string{}
	if err := s.rooms.PingDB(ctx); err != nil {
		slog.Error("healthcheck db ping failed", "error", err)
		status, httpStatus = "degraded", http.StatusServiceUnavailable
		details["db"] = "unreachable"
	} else {
		details["db"] = "ok"
	}
	if err := s.chat.Health(ctx); err != nil {
		slog.Error("healthcheck scylla failed", "error", err)
		status, httpStatus = "degraded", http.StatusServiceUnavailable
		details["scylla"] = "unreachable"
	} else {
		details["scylla"] = "ok"
	}
	writeJSON(w, httpStatus, map[string]any{"status": status, "version": s.version, "details": details})
}

// ---------- helpers ----------

func (s *Server) requireUser(w http.ResponseWriter, r *http.Request) (string, bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", false
	}
	claims, err := auth.Verify(strings.TrimSpace(h[len(prefix):]), s.jwtSecret, time.Now())
	if err != nil || claims.Sub == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", false
	}
	return claims.Sub, true
}

// requireUserWithToken is requireUser but also returns the raw bearer token, so
// the caller can forward it to another service (M7 ProposeAiPool → bet-svc).
func (s *Server) requireUserWithToken(w http.ResponseWriter, r *http.Request) (userID, token string, ok bool) {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	claims, err := auth.Verify(tok, s.jwtSecret, time.Now())
	if err != nil || claims.Sub == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid access token")
		return "", "", false
	}
	return claims.Sub, tok, true
}

func (s *Server) requireMember(ctx context.Context, w http.ResponseWriter, roomID, userID string) bool {
	ok, err := s.rooms.IsMember(ctx, roomID, userID)
	if err != nil {
		slog.Error("membership check failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "membership check failed")
		return false
	}
	if !ok {
		writeError(w, http.StatusForbidden, "forbidden", "not a member of this room")
		return false
	}
	return true
}

func (s *Server) publish(roomID, event string, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		slog.Warn("signal marshal failed", "event", event, "error", err)
		return
	}
	if err := s.signals.PublishRoom(roomID, event, payload); err != nil {
		slog.Warn("signal publish failed", "room", roomID, "event", event, "error", err)
	}
}

func validRoomID(w http.ResponseWriter, raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		writeError(w, http.StatusBadRequest, "invalid_room", "room_id required")
		return "", false
	}
	if _, err := uuid.Parse(raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_room", "room_id must be a UUID")
		return "", false
	}
	return raw, true
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

func ctx5(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func ctxN(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func roomToJSON(r *store.Room) roomJSON {
	return roomJSON{ID: r.ID, Name: r.Name, OwnerUserID: r.OwnerUserID, CreatedAtUnixMS: r.CreatedAtUnixMS}
}
func membershipToJSON(m *store.Membership) membershipJSON {
	return membershipJSON{RoomID: m.RoomID, UserID: m.UserID, Role: m.Role, JoinedAtUnixMS: m.JoinedAtUnixMS}
}
func messageToJSON(m *chat.Message) messageJSON {
	return messageJSON{ID: m.ID, RoomID: m.RoomID, UserID: m.UserID, Body: m.Body, CreatedAtUnixMS: m.CreatedAtUnixMS}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorResponse{Code: code, Message: msg})
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"duration_ms", time.Since(start).Milliseconds())
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
