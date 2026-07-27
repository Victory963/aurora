// Package clients holds room-svc's outbound HTTP clients for the M7 "AI in
// room" flow (ADR-0007): ai-agent-svc (recommendations) and bet-svc (pool
// creation). Both are constructed from a base URL; an empty base URL yields a
// disabled client whose calls return ErrNotConfigured, so room-svc's core M4
// features keep working when the AI/bet dependencies are absent.
package clients

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrNotConfigured is returned when the client has no base URL (dependency
	// intentionally disabled). Handlers map this to 503 not_configured.
	ErrNotConfigured = errors.New("clients: dependency not configured")
	// ErrUnavailable is a transport/5xx failure talking to the dependency.
	ErrUnavailable = errors.New("clients: dependency unavailable")
	// ErrNotMember is returned by Bet.CreatePool when bet-svc rejects the
	// forwarded token's room membership (403).
	ErrNotMember = errors.New("clients: caller not a room member")
)

// Recommendation mirrors one entry of ai-agent-svc's RecommendResponse.
type Recommendation struct {
	MarketID      string   `json:"market_id"`
	Selection     string   `json:"selection"`
	SuggestedOdds float64  `json:"suggested_odds"`
	AIConfidence  float64  `json:"ai_confidence"`
	ExpectedValue float64  `json:"expected_value"`
	Reasoning     string   `json:"reasoning"`
	DataSources   []string `json:"data_sources"`
}

// NewOption is one option to create on a pool.
type NewOption struct {
	Label    string
	OddsX100 int64
}

// PoolOption mirrors bet-svc's option JSON.
type PoolOption struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	OddsX100   int64  `json:"odds_x100"`
	StakeMinor int64  `json:"stake_minor"`
	Bettors    int32  `json:"bettors"`
}

// Pool mirrors bet-svc's pool JSON (subset room-svc echoes back).
type Pool struct {
	ID              string       `json:"id"`
	RoomID          string       `json:"room_id"`
	Question        string       `json:"question"`
	Status          string       `json:"status"`
	CreatedBy       string       `json:"created_by"`
	RakeBps         int32        `json:"rake_bps"`
	Options         []PoolOption `json:"options"`
	TotalStakeMinor int64        `json:"total_stake_minor"`
	CreatedAtUnixMS int64        `json:"created_at_unix_ms"`
}

// -------------------- ai-agent-svc --------------------

// AI calls ai-agent-svc's Recommend endpoint (no auth; internal service call).
type AI struct {
	base string
	http *http.Client
}

// NewAI builds an AI client. base "" => disabled (Recommend => ErrNotConfigured).
func NewAI(base string) *AI {
	return &AI{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 22 * time.Second}}
}

// Enabled reports whether a base URL was configured.
func (c *AI) Enabled() bool { return c.base != "" }

// Recommend asks ai-agent-svc for ROOM-mode recommendations for the caller.
// mode=2 is aurora.ai.v1.Mode.MODE_ROOM.
func (c *AI) Recommend(ctx context.Context, userID, matchID, roomID string) ([]Recommendation, error) {
	if c.base == "" {
		return nil, ErrNotConfigured
	}
	body, _ := json.Marshal(map[string]any{
		"user_id":  userID,
		"mode":     2, // MODE_ROOM
		"match_id": matchID,
		"room_id":  roomID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/aurora.ai.v1.AIAgentService/Recommend", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: ai recommend: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: ai recommend status %d", ErrUnavailable, resp.StatusCode)
	}
	var out struct {
		Recommendations []Recommendation `json:"recommendations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("%w: ai recommend decode: %v", ErrUnavailable, err)
	}
	return out.Recommendations, nil
}

// -------------------- bet-svc --------------------

// Bet calls bet-svc's CreatePool endpoint forwarding the caller's bearer token
// (ADR-0007: the triggering member becomes the pool creator).
type Bet struct {
	base string
	http *http.Client
}

// NewBet builds a Bet client. base "" => disabled (CreatePool => ErrNotConfigured).
func NewBet(base string) *Bet {
	return &Bet{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 8 * time.Second}}
}

// Enabled reports whether a base URL was configured.
func (c *Bet) Enabled() bool { return c.base != "" }

// CreatePool creates a room-scoped pool as the caller (token forwarded).
func (c *Bet) CreatePool(ctx context.Context, bearer, roomID, question string, options []NewOption) (*Pool, error) {
	if c.base == "" {
		return nil, ErrNotConfigured
	}
	optJSON := make([]map[string]any, 0, len(options))
	for _, o := range options {
		optJSON = append(optJSON, map[string]any{"label": o.Label, "odds_x100": o.OddsX100})
	}
	body, _ := json.Marshal(map[string]any{
		"room_id":  roomID,
		"question": question,
		"options":  optJSON,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.base+"/aurora.bet.v1.BetService/CreatePool", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: bet createpool: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		var p Pool
		if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
			return nil, fmt.Errorf("%w: bet createpool decode: %v", ErrUnavailable, err)
		}
		return &p, nil
	case http.StatusForbidden:
		return nil, ErrNotMember
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("%w: bet createpool status %d: %s", ErrUnavailable, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
}
