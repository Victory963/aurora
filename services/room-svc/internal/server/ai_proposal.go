// M7 "AI in room" (ADR-0007): a room member triggers ProposeAiPool; room-svc
// orchestrates ai-agent-svc (recommendation) + bet-svc (pool creation), writes
// an AI-authored chat message, and emits an ai_proposal NATS signal. The
// triggering member's bearer is forwarded to bet-svc, so the member becomes the
// pool creator (settlement authority) — the AI only *proposes*.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dazn/aurora/services/room-svc/internal/clients"
)

const maxProposalQuestionLen = 280

type proposeAiPoolRequest struct {
	RoomID  string `json:"room_id"`
	MatchID string `json:"match_id"`
}

type proposeAiPoolResponse struct {
	Recommendation clients.Recommendation `json:"recommendation"`
	Pool           *clients.Pool          `json:"pool"`
	Message        *messageJSON           `json:"message"`
}

func (s *Server) handleProposeAiPool(w http.ResponseWriter, r *http.Request) {
	userID, bearer, ok := s.requireUserWithToken(w, r)
	if !ok {
		return
	}
	// AI (up to ~22s) + bet (up to ~8s) + chat/NATS overhead.
	ctx, cancel := ctxN(r, 30*time.Second)
	defer cancel()

	var req proposeAiPoolRequest
	if !decode(w, r, &req) {
		return
	}
	roomID, ok := validRoomID(w, req.RoomID)
	if !ok {
		return
	}
	// Only a room member may trigger an AI proposal.
	if !s.requireMember(ctx, w, roomID, userID) {
		return
	}
	if s.ai == nil || !s.ai.Enabled() || s.bet == nil || !s.bet.Enabled() {
		writeError(w, http.StatusServiceUnavailable, "not_configured",
			"AI proposal not available (ai-agent/bet URL unset)")
		return
	}

	// 1) Ask the AI for ROOM-mode recommendations for this caller/match/room.
	recs, err := s.ai.Recommend(ctx, userID, req.MatchID, roomID)
	if err != nil {
		slog.Error("proposeAiPool: ai recommend failed", "error", err)
		writeError(w, http.StatusBadGateway, "ai_unavailable", "ai-agent unavailable")
		return
	}
	if len(recs) == 0 {
		// No pool, no message — a half-completed proposal is the worst outcome.
		writeError(w, http.StatusBadGateway, "ai_no_recommendation", "ai produced no recommendation")
		return
	}
	top := recs[0]

	// 2) Turn the top recommendation into a YES/NO pool. YES carries the AI's
	//    suggested odds; NO is the fair complement B = A/(A-1) (indicative only —
	//    payout is parimutuel, ADR-0006).
	question := strings.TrimSpace(top.Selection)
	if question == "" {
		question = strings.TrimSpace(top.MarketID)
	}
	if question == "" {
		question = "AI proposed pool"
	}
	// Byte-bounded (bet-svc caps question at 300 bytes) but cut on a rune
	// boundary — a raw byte slice could split a multibyte character.
	question = truncateUTF8(question, maxProposalQuestionLen)
	yesX100 := clampOddsX100(int64(math.Round(top.SuggestedOdds * 100.0)))
	noX100 := complementOddsX100(yesX100)

	pool, err := s.bet.CreatePool(ctx, bearer, roomID, question, []clients.NewOption{
		{Label: "YES", OddsX100: yesX100},
		{Label: "NO", OddsX100: noX100},
	})
	if err != nil {
		switch {
		case errors.Is(err, clients.ErrNotMember):
			// Should not happen (we checked membership above), but surface it honestly.
			writeError(w, http.StatusForbidden, "forbidden", "not a member of this room")
		case errors.Is(err, clients.ErrNotConfigured):
			writeError(w, http.StatusServiceUnavailable, "not_configured", "bet service not configured")
		default:
			slog.Error("proposeAiPool: bet createpool failed", "error", err)
			writeError(w, http.StatusBadGateway, "bet_unavailable", "bet service unavailable")
		}
		return
	}

	// 3) Write the AI-authored chat message (server-side; AIUserID is not a member).
	//    Best-effort from here: the pool already exists and is the durable artifact.
	msgBody := aiMessageBody(top, question)
	var msgJSON *messageJSON
	if msg, err := s.chat.InsertMessage(ctx, roomID, AIUserID, msgBody, time.Now().UnixMilli()); err != nil {
		slog.Warn("proposeAiPool: ai chat insert failed (pool created)", "room", roomID, "pool", pool.ID, "error", err)
	} else {
		mj := messageToJSON(msg)
		msgJSON = &mj
		// Emit the AI line on the normal message channel so live clients render it.
		s.publish(roomID, "message", mj)
	}

	// 4) ai_proposal signal carries the pool card metadata (ephemeral; ADR-0007 §4).
	s.publish(roomID, "ai_proposal", map[string]any{
		"pool_id":   pool.ID,
		"question":  question,
		"selection": top.Selection,
		"yes_x100":  yesX100,
		"no_x100":   noX100,
	})

	writeJSON(w, http.StatusOK, proposeAiPoolResponse{
		Recommendation: top,
		Pool:           pool,
		Message:        msgJSON,
	})
}

func aiMessageBody(rec clients.Recommendation, question string) string {
	reasoning := strings.TrimSpace(rec.Reasoning)
	if truncated := truncateUTF8(reasoning, 400); truncated != reasoning {
		reasoning = truncated + "…"
	}
	return fmt.Sprintf("🤖 AI 提议(置信度 %.0f%%):%s。已为本房开启群体 Pool:「%s」",
		rec.AIConfidence*100, reasoning, question)
}

// truncateUTF8 cuts s to at most maxBytes without splitting a multibyte rune
// (Scylla rejects invalid UTF-8 text values; JSON replaces it with U+FFFD).
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// clampOddsX100 keeps odds within bet-svc's accepted 101..100000 (1.01x..1000x).
func clampOddsX100(v int64) int64 {
	if v < 101 {
		return 101
	}
	if v > 100_000 {
		return 100_000
	}
	return v
}

// complementOddsX100 returns the fair complementary decimal odds for the NO side:
// B = A/(A-1). With A >= 1.01 (post-clamp) the result is finite and in range.
func complementOddsX100(yesX100 int64) int64 {
	a := float64(yesX100) / 100.0
	if a <= 1.0 {
		return 100_000
	}
	b := a / (a - 1.0)
	return clampOddsX100(int64(math.Round(b * 100.0)))
}
