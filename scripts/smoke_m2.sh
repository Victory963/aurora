#!/usr/bin/env bash
# =============================================================================
# Aurora M2 — End-to-end smoke test (identity: login / sessions / devices / JWT)
#
# Builds on M0/M1. Verifies the login closed loop:
#   1. health
#   2. CreateUser with a password
#   3. Login -> access JWT + refresh token
#   4. GetMe: 401 without token, 200 (correct user) with token
#   5. ListDevices (authenticated) -> the login device
#   6. RefreshToken rotates; the old refresh token is rejected
#   7. Logout revokes the session; refresh then fails
#   8. DeleteUser (authenticated) -> 200, GetMe -> 404, and a
#      user.lifecycle.deleted.v1 event lands on Kafka (ties back to M1)
#
# Exit 0 = M2 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
AI_URL="${AI_URL:-http://localhost:8082}"
TOPIC="${TOPIC:-aurora.identity.user-lifecycle.v1}"
CONSUME_MS="${CONSUME_MS:-12000}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
cd "$ROOT"

DC="${DOCKER_COMPOSE:-}"
if [ -z "$DC" ]; then
    if docker compose version >/dev/null 2>&1; then DC="docker compose"; else DC="docker-compose"; fi
fi

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

base="$ID/aurora.identity.v1.IdentityService"

# req DATA URL [BEARER] -> sets RESP_BODY, RESP_CODE
RESP_BODY=""; RESP_CODE=""
req() {
    local data="$1" url="$2" bearer="${3:-}" out
    if [ -n "$bearer" ]; then
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" \
              -H "Authorization: Bearer ${bearer}" -d "$data" "$url")
    else
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -d "$data" "$url")
    fi
    RESP_CODE="${out##*$'\n'}"
    RESP_BODY="${out%$'\n'*}"
}

# -----------------------------------------------------------------------------
step "1/8 health"
[ "$(curl -fsS "$ID/healthz" | jq -r .status)" = "ok" ] || fail "identity-svc not ok"
curl -fsS "$AI_URL/healthz" >/dev/null || fail "ai-agent-svc not ok"
pass "services healthy"

# -----------------------------------------------------------------------------
step "2/8 CreateUser with password"
EMAIL="smoke-m2-$(date +%s)@aurora.test"
PASSWORD="smoke-pass-123"
req "{\"email\":\"${EMAIL}\",\"display_name\":\"Smoke M2\",\"kyc_country\":\"GB\",\"password\":\"${PASSWORD}\"}" "$base/CreateUser"
[ "$RESP_CODE" = "200" ] || fail "CreateUser code=$RESP_CODE body=$RESP_BODY"
USER_ID=$(echo "$RESP_BODY" | jq -r .user.id)
[ -n "$USER_ID" ] && [ "$USER_ID" != "null" ] || fail "no user.id: $RESP_BODY"
pass "created user ${USER_ID} (${EMAIL})"

# -----------------------------------------------------------------------------
step "3/8 Login"
req "{\"email\":\"${EMAIL}\",\"password\":\"${PASSWORD}\",\"device\":{\"platform\":\"web\",\"user_agent\":\"smoke-m2\"}}" "$base/Login"
[ "$RESP_CODE" = "200" ] || fail "Login code=$RESP_CODE body=$RESP_BODY"
ACCESS=$(echo "$RESP_BODY" | jq -r .access_token)
REFRESH=$(echo "$RESP_BODY" | jq -r .refresh_token)
[ -n "$ACCESS" ] && [ "$ACCESS" != "null" ]   || fail "no access_token"
[ -n "$REFRESH" ] && [ "$REFRESH" != "null" ] || fail "no refresh_token"
[ "$(echo "$RESP_BODY" | jq -r .user.id)" = "$USER_ID" ] || fail "login user mismatch"
pass "logged in (access + refresh issued)"

# Wrong password must fail.
req "{\"email\":\"${EMAIL}\",\"password\":\"wrong-password\",\"device\":{\"platform\":\"web\"}}" "$base/Login"
[ "$RESP_CODE" = "401" ] || fail "wrong password expected 401, got $RESP_CODE"
pass "wrong password rejected (401)"

# -----------------------------------------------------------------------------
step "4/8 GetMe (401 without token, 200 with token)"
req '{}' "$base/GetMe"
[ "$RESP_CODE" = "401" ] || fail "GetMe without token expected 401, got $RESP_CODE"
req '{}' "$base/GetMe" "$ACCESS"
[ "$RESP_CODE" = "200" ] || fail "GetMe code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .user.id)" = "$USER_ID" ] || fail "GetMe returned wrong user"
pass "GetMe enforces auth and returns the right user"

# -----------------------------------------------------------------------------
step "5/8 ListDevices (authenticated)"
req '{}' "$base/ListDevices" "$ACCESS"
[ "$RESP_CODE" = "200" ] || fail "ListDevices code=$RESP_CODE"
DCOUNT=$(echo "$RESP_BODY" | jq '.devices | length')
[ "$DCOUNT" -ge 1 ] || fail "expected >=1 device, got $DCOUNT"
[ "$(echo "$RESP_BODY" | jq -r '.devices[0].platform')" = "web" ] || fail "device platform mismatch"
pass "device tracked from login (${DCOUNT} device)"

# -----------------------------------------------------------------------------
step "6/8 RefreshToken rotates; old refresh rejected"
req "{\"refresh_token\":\"${REFRESH}\"}" "$base/RefreshToken"
[ "$RESP_CODE" = "200" ] || fail "Refresh code=$RESP_CODE body=$RESP_BODY"
REFRESH2=$(echo "$RESP_BODY" | jq -r .refresh_token)
ACCESS2=$(echo "$RESP_BODY" | jq -r .access_token)
[ -n "$REFRESH2" ] && [ "$REFRESH2" != "null" ] || fail "no rotated refresh token"
[ "$REFRESH2" != "$REFRESH" ] || fail "refresh token was not rotated"
[ -n "$ACCESS2" ] && [ "$ACCESS2" != "null" ]   || fail "no new access token"
req "{\"refresh_token\":\"${REFRESH}\"}" "$base/RefreshToken"
[ "$RESP_CODE" = "401" ] || fail "old refresh expected 401, got $RESP_CODE"
pass "refresh rotated; old token invalidated"

# -----------------------------------------------------------------------------
step "7/8 Logout revokes the session"
req "{\"refresh_token\":\"${REFRESH2}\"}" "$base/Logout"
[ "$RESP_CODE" = "200" ] || fail "Logout code=$RESP_CODE"
req "{\"refresh_token\":\"${REFRESH2}\"}" "$base/RefreshToken"
[ "$RESP_CODE" = "401" ] || fail "refresh after logout expected 401, got $RESP_CODE"
pass "logout revoked the session"

# -----------------------------------------------------------------------------
step "8/8 DeleteUser + user.lifecycle.deleted.v1 event"
req '{}' "$base/DeleteUser" "$ACCESS"
[ "$RESP_CODE" = "200" ] || fail "DeleteUser code=$RESP_CODE body=$RESP_BODY"
req '{}' "$base/GetMe" "$ACCESS"
[ "$RESP_CODE" = "404" ] || fail "GetMe after delete expected 404, got $RESP_CODE"
pass "user deleted; GetMe now 404"

sleep 2
RAW=$($DC exec -T kafka kafka-console-consumer \
    --bootstrap-server kafka:9092 --topic "$TOPIC" \
    --from-beginning --timeout-ms "$CONSUME_MS" 2>/dev/null || true)
DEL_EVENT=$(echo "$RAW" | grep -F "$USER_ID" | grep -F 'user.lifecycle.deleted.v1' | tail -n1 || true)
[ -n "$DEL_EVENT" ] || fail "no user.lifecycle.deleted.v1 event for ${USER_ID} on ${TOPIC}"
[ "$(echo "$DEL_EVENT" | jq -r .event_type)" = "user.lifecycle.deleted.v1" ] || fail "bad event_type"
[ "$(echo "$DEL_EVENT" | jq -r .user_id)" = "$USER_ID" ] || fail "event user_id mismatch"
for field in event_id event_type occurred_at_unix_ms user_id reason; do
    [ "$(echo "$DEL_EVENT" | jq -r --arg k "$field" 'has($k)')" = "true" ] || fail "deleted event missing field: $field"
done
pass "deleted event published and matches the Avro contract"

# -----------------------------------------------------------------------------
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M2 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Login closed loop works:"
echo "  CreateUser(pwd) → Login(JWT+refresh) → GetMe/ListDevices → Refresh(rotate) → Logout → Delete(+event)"
