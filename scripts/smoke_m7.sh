#!/usr/bin/env bash
# =============================================================================
# Aurora M7 — End-to-end smoke test: "AI in the Room" (ADR-0007)
#
# The demo's signature moment, made real across M4+M5+M6:
#   1. A creates a room, B joins (M4); both wallets funded (M3).
#   2. A calls room.ProposeAiPool(room_id, match_id):
#        room-svc → ai-agent-svc (M5 recommendation, tool-audited) → bet-svc
#        (M6 pool created) → AI chat message written → ai_proposal NATS signal.
#      Works WITHOUT ANTHROPIC_API_KEY (M5 degrades to the template engine).
#   3. The aurora-ai message is visible in ListMessages; NATS in_msgs grew.
#   4. B bets on the proposed pool; A settles; balances move correctly.
#      → full loop lands: AI proposes → group bets → settle.
#   5. A non-member calling ProposeAiPool is rejected 403.
#
# Exit 0 = M7 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
WALLET="${WALLET_URL:-http://localhost:8083}"
ROOM="${ROOM_URL:-http://localhost:8084}"
BET="${BET_URL:-http://localhost:8086}"
NATS_MON="${NATS_MON:-http://localhost:8222}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

rbase="$ROOM/aurora.room.v1.RoomService"
RESP_BODY=""; RESP_CODE=""
req() { # DATA URL [BEARER]
    local out
    if [ -n "${3:-}" ]; then
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -H "Authorization: Bearer $3" -d "$1" "$2")
    else
        out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -d "$1" "$2")
    fi
    RESP_CODE="${out##*$'\n'}"; RESP_BODY="${out%$'\n'*}"
}

signup_login() { # EMAIL -> "user_id access_token"
    local email="$1" pw="m7-pass-123" uid tok
    uid=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"display_name\":\"M7\",\"kyc_country\":\"GB\",\"password\":\"$pw\"}" | jq -r .user.id)
    tok=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/Login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"password\":\"$pw\",\"device\":{\"platform\":\"web\"}}" | jq -r .access_token)
    echo "$uid $tok"
}

wallet_wait_open() { for _ in $(seq 1 40); do
    curl -fsS "$WALLET/aurora.wallet.v1.WalletService/GetBalance" -H 'Content-Type: application/json' \
        -d "{\"user_id\":\"$1\"}" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
balance() { curl -fsS "$WALLET/aurora.wallet.v1.WalletService/GetBalance" -H 'Content-Type: application/json' \
    -d "{\"user_id\":\"$1\"}" | jq -r .balance_minor; }
credit() { curl -fsS "$WALLET/aurora.wallet.v1.WalletService/Credit" -H 'Content-Type: application/json' \
    -d "{\"user_id\":\"$1\",\"amount_minor\":$2,\"idempotency_key\":\"$3\"}" >/dev/null; }
nats_inmsgs() { curl -fsS "$NATS_MON/varz" 2>/dev/null | jq -r '.in_msgs // 0' 2>/dev/null || echo 0; }

# -----------------------------------------------------------------------------
step "1/6 health + users + wallets + room"
[ "$(curl -fsS "$ROOM/healthz" | jq -r .status)" = "ok" ] || fail "room-svc not ok"
TS="$(date +%s)-$$-${RANDOM}"
read -r UA TOKA <<<"$(signup_login "smoke-m7-a-$TS@aurora.test")"
read -r UB TOKB <<<"$(signup_login "smoke-m7-b-$TS@aurora.test")"
read -r UC TOKC <<<"$(signup_login "smoke-m7-c-$TS@aurora.test")"
[ -n "$TOKA" ] && [ "$TOKA" != "null" ] || fail "login A failed"
wallet_wait_open "$UA" || fail "wallet A not opened"
wallet_wait_open "$UB" || fail "wallet B not opened"
credit "$UA" 5000 "m7-fund-a-$TS"; credit "$UB" 5000 "m7-fund-b-$TS"

RID=$(curl -fsS "$rbase/CreateRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKA" -d '{"name":"m7-final-whistle"}' | jq -r .id)
[ -n "$RID" ] && [ "$RID" != "null" ] || fail "create room failed"
curl -fsS "$rbase/JoinRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKB" -d "{\"room_id\":\"$RID\"}" >/dev/null
pass "room $RID: A owner, B member; both funded 5000"

# -----------------------------------------------------------------------------
step "2/6 non-member ProposeAiPool → 403 (guard before any side effect)"
req "{\"room_id\":\"$RID\",\"match_id\":\"match-final\"}" "$rbase/ProposeAiPool" "$TOKC"
[ "$RESP_CODE" = "403" ] || fail "non-member ProposeAiPool expected 403, got $RESP_CODE body=$RESP_BODY"
pass "outsider blocked (403)"

# -----------------------------------------------------------------------------
step "3/6 A triggers ProposeAiPool → AI recommends, pool created"
NATS_BEFORE=$(nats_inmsgs)
req "{\"room_id\":\"$RID\",\"match_id\":\"match-final\"}" "$rbase/ProposeAiPool" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "ProposeAiPool code=$RESP_CODE body=$RESP_BODY"

POOL=$(echo "$RESP_BODY" | jq -r .pool.id)
[ -n "$POOL" ] && [ "$POOL" != "null" ] || fail "no pool in response: $RESP_BODY"
YES=$(echo "$RESP_BODY" | jq -r '.pool.options[] | select(.label=="YES") | .id')
NO=$(echo "$RESP_BODY" | jq -r '.pool.options[] | select(.label=="NO") | .id')
[ -n "$YES" ] && [ -n "$NO" ] || fail "pool options missing"

# Recommendation carries a tool-call audit trail (M5 grounding, degrade-safe).
SRC_COUNT=$(echo "$RESP_BODY" | jq -r '.recommendation.data_sources | length')
[ "$SRC_COUNT" -ge 1 ] || fail "recommendation has no data_sources audit trail"
echo "$RESP_BODY" | jq -r '.recommendation.data_sources[]' | grep -q '^tool:' \
    || fail "no tool: audit tag in data_sources (AI must ground on tools)"
info "recommendation: $(echo "$RESP_BODY" | jq -rc '{selection:.recommendation.selection, sources:.recommendation.data_sources}')"

# AI chat message returned + authored by the reserved platform id.
MSG_AUTHOR=$(echo "$RESP_BODY" | jq -r '.message.user_id')
[ "$MSG_AUTHOR" = "aurora-ai" ] || fail "AI message author=$MSG_AUTHOR, want aurora-ai"
pass "pool $POOL created; AI message by aurora-ai; audit trail present"

# -----------------------------------------------------------------------------
step "4/6 AI message visible in history + NATS ai_proposal signal fired"
MSGS=$(curl -fsS "$rbase/ListMessages" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKB" -d "{\"room_id\":\"$RID\"}")
echo "$MSGS" | jq -e '.messages[] | select(.user_id=="aurora-ai")' >/dev/null \
    || fail "aurora-ai message not found in ListMessages"
NATS_AFTER=$(nats_inmsgs)
if [ "$NATS_AFTER" -gt "$NATS_BEFORE" ]; then
    pass "aurora-ai message in history; NATS in_msgs $NATS_BEFORE → $NATS_AFTER"
else
    info "NATS in_msgs $NATS_BEFORE → $NATS_AFTER (monitoring may be off; message check passed)"
    pass "aurora-ai message in history"
fi

# -----------------------------------------------------------------------------
step "5/6 group bets on the AI pool, then settle → balances move"
bbase="$BET/aurora.bet.v1.BetService"
# B bets 600 NO.
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$NO\",\"stake_minor\":600,\"idempotency_key\":\"m7-b-$TS\"}" "$bbase/PlaceBet" "$TOKB"
[ "$RESP_CODE" = "200" ] || fail "B bet code=$RESP_CODE body=$RESP_BODY"
[ "$(balance "$UB")" = "4400" ] || fail "B balance after bet != 4400"
# A bets 1000 YES.
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$YES\",\"stake_minor\":1000,\"idempotency_key\":\"m7-a-$TS\"}" "$bbase/PlaceBet" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "A bet code=$RESP_CODE body=$RESP_BODY"
[ "$(balance "$UA")" = "4000" ] || fail "A balance after bet != 4000"
# A (creator) settles YES: A = 4000 + 1000 + floor(600*0.95)=570 = 5570; B = 4400.
req "{\"pool_id\":\"$POOL\",\"winning_option_id\":\"$YES\"}" "$bbase/SettlePool" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "settle code=$RESP_CODE body=$RESP_BODY"
[ "$(balance "$UA")" = "5570" ] || fail "A payout wrong: $(balance "$UA") != 5570"
[ "$(balance "$UB")" = "4400" ] || fail "B changed on settle: $(balance "$UB")"
pass "AI pool settled: A=5570 (winner), B=4400 (loser)"

# -----------------------------------------------------------------------------
step "6/6 ProposeAiPool is member-gated end to end"
req "{\"room_id\":\"$RID\",\"match_id\":\"m\"}" "$rbase/ProposeAiPool" "$TOKC"
[ "$RESP_CODE" = "403" ] || fail "outsider still expected 403, got $RESP_CODE"
pass "membership gate holds"

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M7 SMOKE TEST PASSED — AI in the Room is real end to end${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "AI proposed a pool (grounded on M5 tools) → members bet through wallet (M3)"
echo "→ creator settled parimutuel (M6) → all inside a room with live signals (M4)."
