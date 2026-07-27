#!/usr/bin/env bash
# =============================================================================
# Aurora M5 — End-to-end smoke test (ai-agent-svc: LangGraph + Claude API)
#
# Verifies:
#   1. health
#   2. Recommend returns 200 with 1-3 recs whose data_sources carry the TOOL
#      AUDIT TRAIL (tool:player_stats / tool:odds / tool:rg_check) + an engine
#      marker: "llm:<model>" (key present) or "fallback:template" (keyless).
#      Numbers are tool-grounded: 0 < confidence <= 1, odds > 1.
#   3. Deterministic replay on the fallback path (same match => same recs).
#   4. ROOM mode carries a room sentiment source.
#   5. RG hard gate: user_id containing "rgblock" => 200 with ZERO recs.
#
# Works WITHOUT ANTHROPIC_API_KEY (degraded path) — that is the point of the
# M5 degrade ladder. With a key set on ai-agent-svc, step 2 reports the llm
# marker instead.
#
# Exit 0 = M5 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
AI="${AI_URL:-http://localhost:8082}"
MATCH="j1.urawa-vs-fctokyo"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

recommend() { # USER_ID MODE ROOM_ID -> RESP
    curl -fsS "$AI/aurora.ai.v1.AIAgentService/Recommend" -H 'Content-Type: application/json' \
        -d "{\"user_id\":\"$1\",\"mode\":$2,\"match_id\":\"$MATCH\",\"room_id\":\"$3\"}"
}

# -----------------------------------------------------------------------------
step "1/5 health + create a real user"
[ "$(curl -fsS "$AI/healthz" | jq -r .status)" = "ok" ] || fail "ai-agent-svc not ok"
EMAIL="smoke-m5-$(date +%s)-$$@aurora.test"
USER_ID=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
    -d "{\"email\":\"$EMAIL\",\"display_name\":\"Smoke M5\",\"kyc_country\":\"GB\"}" | jq -r .user.id)
[ -n "$USER_ID" ] && [ "$USER_ID" != "null" ] || fail "create user failed"
pass "healthy; user $USER_ID"

# -----------------------------------------------------------------------------
step "2/5 Recommend (SOLO): tool audit + engine marker + grounded numbers"
RESP=$(recommend "$USER_ID" 1 "")
N=$(echo "$RESP" | jq '.recommendations | length')
[ "$N" -ge 1 ] && [ "$N" -le 3 ] || fail "expected 1-3 recs, got $N: $RESP"
echo "$RESP" | jq -e '.recommendations[0].data_sources | any(startswith("tool:player_stats:"))' >/dev/null \
    || fail "missing tool:player_stats audit"
echo "$RESP" | jq -e '.recommendations[0].data_sources | any(startswith("tool:odds:"))' >/dev/null \
    || fail "missing tool:odds audit"
echo "$RESP" | jq -e '.recommendations[0].data_sources | any(startswith("tool:rg_check:"))' >/dev/null \
    || fail "missing tool:rg_check audit"
ENGINE=$(echo "$RESP" | jq -r '.recommendations[0].data_sources[] | select(startswith("llm:") or . == "fallback:template")' | head -n1)
[ -n "$ENGINE" ] || fail "no engine marker (llm:* or fallback:template)"
echo "$RESP" | jq -e '.recommendations | all(.ai_confidence > 0 and .ai_confidence <= 1 and .suggested_odds > 1 and (.reasoning | length > 0))' >/dev/null \
    || fail "ungrounded numbers or empty reasoning: $RESP"
pass "got $N recs, engine=$ENGINE, audit trail present"
info "$(echo "$RESP" | jq -c '.recommendations[0] | {market_id, selection, suggested_odds, ai_confidence}')"

# -----------------------------------------------------------------------------
step "3/5 fallback determinism (only asserted when keyless)"
if [ "$ENGINE" = "fallback:template" ]; then
    R2=$(recommend "$USER_ID" 1 "")
    [ "$(echo "$RESP" | jq -S .recommendations)" = "$(echo "$R2" | jq -S .recommendations)" ] \
        || fail "fallback path not deterministic"
    pass "fallback path deterministic"
else
    info "llm engine active ($ENGINE) — determinism not asserted (LLM output varies)"
    pass "skipped by design"
fi

# -----------------------------------------------------------------------------
step "4/5 ROOM mode carries room sentiment source"
RROOM=$(recommend "$USER_ID" 2 "room-smoke-m5")
echo "$RROOM" | jq -e '.recommendations[0].data_sources | any(contains("room"))' >/dev/null \
    || fail "ROOM mode missing room source: $RROOM"
pass "room sentiment source present"

# -----------------------------------------------------------------------------
step "5/5 RG hard gate blocks (empty recs, still 200)"
RBLOCK=$(recommend "rgblock-$USER_ID" 1 "")
[ "$(echo "$RBLOCK" | jq '.recommendations | length')" = "0" ] || fail "rgblock user should get 0 recs"
pass "rgblock user => 0 recommendations"

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M5 SMOKE TEST PASSED  (engine: $ENGINE)${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
