#!/usr/bin/env bash
# =============================================================================
# Aurora M0 — End-to-end smoke test
#
# Verifies the M0 happy path:
#   1. identity-svc health is ok
#   2. ai-agent-svc health is ok and reports identity-svc reachable
#   3. Create a user via identity-svc
#   4. Get that user back
#   5. Call ai-agent-svc Recommend with the user_id
#   6. Verify recommendations are returned with expected schema
#
# Exit code 0 = M0 verified.
# Any other code = something is broken; output shows what.
#
# Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

IDENTITY_URL="${IDENTITY_URL:-http://localhost:8081}"
AI_URL="${AI_URL:-http://localhost:8082}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

# Tool check
command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required (sudo apt install jq / brew install jq)"

# -----------------------------------------------------------------------------
step "1/6 identity-svc health"
HEALTH_RAW=$(curl -fsS "${IDENTITY_URL}/healthz" || echo "")
[ -n "$HEALTH_RAW" ] || fail "identity-svc /healthz returned nothing (is the container up?)"
HEALTH_STATUS=$(echo "$HEALTH_RAW" | jq -r .status)
[ "$HEALTH_STATUS" = "ok" ] || fail "identity-svc health status = $HEALTH_STATUS"
pass "identity-svc reports status=ok"
echo "  $HEALTH_RAW" | jq -c .

# -----------------------------------------------------------------------------
step "2/6 ai-agent-svc health + identity dependency"
AI_HEALTH=$(curl -fsS "${AI_URL}/aurora.ai.v1.AIAgentService/HealthCheck" \
              -H 'Content-Type: application/json' -d '{}' || echo "")
[ -n "$AI_HEALTH" ] || fail "ai-agent-svc returned nothing"
AI_STATUS=$(echo "$AI_HEALTH" | jq -r .status)
AI_IDENTITY=$(echo "$AI_HEALTH" | jq -r '.details.identity_svc')

[ "$AI_STATUS" = "ok" ]      || fail "ai-agent-svc status=$AI_STATUS (expected ok)"
[ "$AI_IDENTITY" = "ok" ]    || fail "ai-agent-svc → identity-svc check = $AI_IDENTITY"
pass "ai-agent-svc reports status=ok, identity-svc reachable"
echo "  $AI_HEALTH" | jq -c .

# -----------------------------------------------------------------------------
step "3/6 Create a user via identity-svc"
EMAIL="smoke-$(date +%s)@aurora.test"
CREATE_RESP=$(curl -fsS \
    "${IDENTITY_URL}/aurora.identity.v1.IdentityService/CreateUser" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${EMAIL}\",\"display_name\":\"Smoke Test\",\"kyc_country\":\"GB\"}")

USER_ID=$(echo "$CREATE_RESP" | jq -r .user.id)
[ -n "$USER_ID" ] && [ "$USER_ID" != "null" ] || fail "no user.id in response: $CREATE_RESP"
pass "Created user: ${USER_ID}"
echo "  $CREATE_RESP" | jq -c .

# -----------------------------------------------------------------------------
step "4/6 Fetch user back by ID"
GET_RESP=$(curl -fsS \
    "${IDENTITY_URL}/aurora.identity.v1.IdentityService/GetUser" \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"${USER_ID}\"}")

GOT_EMAIL=$(echo "$GET_RESP" | jq -r .user.email)
[ "$GOT_EMAIL" = "$EMAIL" ] || fail "GetUser returned email=$GOT_EMAIL, expected $EMAIL"
pass "GetUser returned the right user"

# -----------------------------------------------------------------------------
step "5/6 Ask ai-agent-svc for recommendations (SOLO mode)"
RECOMMEND_RESP=$(curl -fsS \
    "${AI_URL}/aurora.ai.v1.AIAgentService/Recommend" \
    -H "Content-Type: application/json" \
    -d "{\"user_id\":\"${USER_ID}\",\"mode\":1,\"match_id\":\"j1.urawa-vs-fctokyo\",\"room_id\":\"\"}")

REC_COUNT=$(echo "$RECOMMEND_RESP" | jq '.recommendations | length')
CORR_ID=$(echo "$RECOMMEND_RESP" | jq -r .correlation_id)
LATENCY=$(echo "$RECOMMEND_RESP" | jq -r .latency_ms)

[ "$REC_COUNT" -gt 0 ] || fail "got 0 recommendations"
[ -n "$CORR_ID" ] && [ "$CORR_ID" != "null" ] || fail "no correlation_id"
pass "Got ${REC_COUNT} recommendation(s), correlation_id=${CORR_ID}, latency=${LATENCY}ms"

# -----------------------------------------------------------------------------
step "6/6 Verify recommendation schema"
FIRST=$(echo "$RECOMMEND_RESP" | jq '.recommendations[0]')
MARKET=$(echo "$FIRST" | jq -r .market_id)
CONFIDENCE=$(echo "$FIRST" | jq -r .ai_confidence)
REASONING=$(echo "$FIRST" | jq -r .reasoning)

[ -n "$MARKET" ]      || fail "no market_id"
[ -n "$REASONING" ]   || fail "no reasoning"
# Confidence is a number in [0, 1]
echo "$CONFIDENCE" | awk '{exit !($1 >= 0 && $1 <= 1)}' || fail "confidence out of [0,1]: $CONFIDENCE"
pass "Recommendation schema valid: market=$MARKET confidence=$CONFIDENCE"

# -----------------------------------------------------------------------------
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M0 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Cross-service plumbing works:"
echo "  client → identity-svc (Postgres) → ai-agent-svc → response"
echo ""
echo "Next: read docs/M0_NEXT_STEPS.md to start M1."
