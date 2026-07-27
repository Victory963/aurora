#!/usr/bin/env bash
# =============================================================================
# Aurora M4 — End-to-end smoke test (room-svc: rooms / membership / chat / NATS)
#
# Builds on M0-M3. Verifies:
#   1. health (identity + room)
#   2. two users sign up + log in (reuse M2)
#   3. user A creates a room (owner+member); user B joins; members = 2
#   4. A sends a message (ScyllaDB) and a typing signal (NATS)
#   5. B lists messages and sees it
#   6. NATS /varz in_msgs increases -> the first NATS producer works
#   7. a non-member is forbidden (403); no token is unauthorized (401)
#
# Exit 0 = M4 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
ROOM="${ROOM_URL:-http://localhost:8084}"
NATS_MON="${NATS_MON_URL:-http://localhost:8222}"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

rbase="$ROOM/aurora.room.v1.RoomService"

RESP_BODY=""; RESP_CODE=""
reqb() { # DATA URL [BEARER]
    local out
    if [ -n "${3:-}" ]; then
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -H "Authorization: Bearer $3" -d "$1" "$2")
    else
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -d "$1" "$2")
    fi
    RESP_CODE="${out##*$'\n'}"; RESP_BODY="${out%$'\n'*}"
}

signup_login() { # EMAIL -> echoes access token
    local email="$1" pw="room-pass-123"
    curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"display_name\":\"R\",\"kyc_country\":\"GB\",\"password\":\"$pw\"}" >/dev/null
    curl -fsS "$ID/aurora.identity.v1.IdentityService/Login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"password\":\"$pw\",\"device\":{\"platform\":\"web\"}}" | jq -r .access_token
}

nats_in() { curl -fsS "$NATS_MON/varz" | jq -r .in_msgs; }

# -----------------------------------------------------------------------------
step "1/7 health (identity + room)"
[ "$(curl -fsS "$ID/healthz" | jq -r .status)" = "ok" ] || fail "identity-svc not ok"
[ "$(curl -fsS "$ROOM/healthz" | jq -r .status)" = "ok" ] || fail "room-svc not ok"
pass "identity + room healthy"

# -----------------------------------------------------------------------------
step "2/7 two users sign up + log in"
TS="$(date +%s)-$$-${RANDOM}"   # unique across rapid re-runs / reused DB volume
TOKA=$(signup_login "smoke-m4-a-$TS@aurora.test")
TOKB=$(signup_login "smoke-m4-b-$TS@aurora.test")
TOKC=$(signup_login "smoke-m4-c-$TS@aurora.test")
[ -n "$TOKA" ] && [ "$TOKA" != "null" ] || fail "user A login failed"
[ -n "$TOKB" ] && [ "$TOKB" != "null" ] || fail "user B login failed"
pass "three users logged in"

# unauthenticated must be rejected
reqb '{"name":"x"}' "$rbase/CreateRoom"
[ "$RESP_CODE" = "401" ] || fail "CreateRoom without token expected 401, got $RESP_CODE"
pass "unauthenticated CreateRoom rejected (401)"

# -----------------------------------------------------------------------------
step "3/7 create room, B joins, members = 2"
reqb '{"name":"saitama-stand"}' "$rbase/CreateRoom" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "CreateRoom code=$RESP_CODE body=$RESP_BODY"
ROOM_ID=$(echo "$RESP_BODY" | jq -r .id)
[ -n "$ROOM_ID" ] && [ "$ROOM_ID" != "null" ] || fail "no room id"
pass "room created: $ROOM_ID"

reqb "{\"room_id\":\"$ROOM_ID\"}" "$rbase/JoinRoom" "$TOKB"
[ "$RESP_CODE" = "200" ] || fail "JoinRoom code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .role)" = "MEMBER" ] || fail "B role should be MEMBER"

reqb "{\"room_id\":\"$ROOM_ID\"}" "$rbase/ListMembers" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "ListMembers code=$RESP_CODE"
MCOUNT=$(echo "$RESP_BODY" | jq '.members | length')
[ "$MCOUNT" = "2" ] || fail "expected 2 members, got $MCOUNT"
pass "B joined; 2 members"

# -----------------------------------------------------------------------------
step "4/7 send message (ScyllaDB) + typing (NATS); track NATS in_msgs"
BEFORE=$(nats_in)
reqb "{\"room_id\":\"$ROOM_ID\",\"body\":\"hello room\"}" "$rbase/SendMessage" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "SendMessage code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .body)" = "hello room" ] || fail "message body mismatch"
reqb "{\"room_id\":\"$ROOM_ID\"}" "$rbase/Typing" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "Typing code=$RESP_CODE"
pass "message persisted; typing signalled"

# -----------------------------------------------------------------------------
step "5/7 B lists messages and sees it (read from ScyllaDB)"
reqb "{\"room_id\":\"$ROOM_ID\",\"limit\":50}" "$rbase/ListMessages" "$TOKB"
[ "$RESP_CODE" = "200" ] || fail "ListMessages code=$RESP_CODE body=$RESP_BODY"
echo "$RESP_BODY" | jq -e '.messages | map(.body) | index("hello room") != null' >/dev/null \
    || fail "message 'hello room' not found in ListMessages"
pass "message round-trips through ScyllaDB"

# -----------------------------------------------------------------------------
step "6/7 NATS producer verified via in_msgs growth"
sleep 1
AFTER=$(nats_in)
info "NATS in_msgs: before=$BEFORE after=$AFTER"
[ "$AFTER" -gt "$BEFORE" ] || fail "NATS in_msgs did not grow — room-svc not publishing to NATS"
pass "room-svc published signals to NATS (first NATS producer works)"

# -----------------------------------------------------------------------------
step "7/7 non-member is forbidden"
reqb "{\"room_id\":\"$ROOM_ID\",\"body\":\"intruder\"}" "$rbase/SendMessage" "$TOKC"
[ "$RESP_CODE" = "403" ] || fail "non-member send expected 403, got $RESP_CODE"
pass "non-member send rejected (403)"

# -----------------------------------------------------------------------------
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M4 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Rooms + chat + realtime work:"
echo "  login → CreateRoom → Join → ListMembers → SendMessage(Scylla)+Typing(NATS) → ListMessages"
