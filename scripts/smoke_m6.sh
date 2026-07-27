#!/usr/bin/env bash
# =============================================================================
# Aurora M6 — End-to-end smoke test (bet-svc: pools / bets / settlement)
#
# Flow (exact-money assertions, rake 5%):
#   1. health; two users sign up + log in; wallets auto-open (M3 consumer)
#   2. credit A and B 5000 each
#   3. A creates a room, B joins (M4); A creates a YES/NO pool in the room
#   4. A bets 1000 YES (balance 4000); same-key replay does NOT double-debit
#   5. B bets 600 NO (balance 4400); non-member bet => 403; oversize => 422
#   6. A settles YES: A = 4000 + 1000 + floor(600*0.95) = 5570; B stays 4400
#   7. settle replay is idempotent; Kafka carries bet.placed + pool.settled
#
# Exit 0 = M6 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
WALLET="${WALLET_URL:-http://localhost:8083}"
ROOM="${ROOM_URL:-http://localhost:8084}"
BET="${BET_URL:-http://localhost:8086}"
BTOPIC="${BTOPIC:-aurora.bet.lifecycle.v1}"
CONSUME_MS="${CONSUME_MS:-12000}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(dirname "$SCRIPT_DIR")"

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

bbase="$BET/aurora.bet.v1.BetService"
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
    local email="$1" pw="bet-pass-123" uid tok
    uid=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"display_name\":\"B\",\"kyc_country\":\"GB\",\"password\":\"$pw\"}" | jq -r .user.id)
    tok=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/Login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"password\":\"$pw\",\"device\":{\"platform\":\"web\"}}" | jq -r .access_token)
    echo "$uid $tok"
}

wallet_wait_open() { # USER_ID
    for _ in $(seq 1 40); do
        if curl -fsS "$WALLET/aurora.wallet.v1.WalletService/GetBalance" -H 'Content-Type: application/json' \
            -d "{\"user_id\":\"$1\"}" >/dev/null 2>&1; then return 0; fi
        sleep 1
    done
    return 1
}

balance() { curl -fsS "$WALLET/aurora.wallet.v1.WalletService/GetBalance" -H 'Content-Type: application/json' \
    -d "{\"user_id\":\"$1\"}" | jq -r .balance_minor; }

credit() { # USER AMOUNT KEY
    curl -fsS "$WALLET/aurora.wallet.v1.WalletService/Credit" -H 'Content-Type: application/json' \
        -d "{\"user_id\":\"$1\",\"amount_minor\":$2,\"idempotency_key\":\"$3\"}" >/dev/null
}

# -----------------------------------------------------------------------------
step "1/7 health + users + wallets"
[ "$(curl -fsS "$BET/healthz" | jq -r .status)" = "ok" ] || fail "bet-svc not ok"
TS="$(date +%s)-$$-${RANDOM}"
read -r UA TOKA <<<"$(signup_login "smoke-m6-a-$TS@aurora.test")"
read -r UB TOKB <<<"$(signup_login "smoke-m6-b-$TS@aurora.test")"
read -r UC TOKC <<<"$(signup_login "smoke-m6-c-$TS@aurora.test")"
[ -n "$TOKA" ] && [ "$TOKA" != "null" ] || fail "login A failed"
wallet_wait_open "$UA" || fail "wallet A not opened"
wallet_wait_open "$UB" || fail "wallet B not opened"
wallet_wait_open "$UC" || fail "wallet C not opened"
credit "$UA" 5000 "m6-fund-a-$TS"; credit "$UB" 5000 "m6-fund-b-$TS"; credit "$UC" 5000 "m6-fund-c-$TS"
[ "$(balance "$UA")" = "5000" ] || fail "A funding failed"
pass "3 users funded (A/B members, C outsider)"

# -----------------------------------------------------------------------------
step "2/7 room + pool"
RID=$(curl -fsS "$ROOM/aurora.room.v1.RoomService/CreateRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKA" -d '{"name":"m6-pool-room"}' | jq -r .id)
[ -n "$RID" ] && [ "$RID" != "null" ] || fail "create room failed"
curl -fsS "$ROOM/aurora.room.v1.RoomService/JoinRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKB" -d "{\"room_id\":\"$RID\"}" >/dev/null

req "{\"room_id\":\"$RID\",\"question\":\"home scores in 15m?\",\"options\":[{\"label\":\"YES\",\"odds_x100\":220},{\"label\":\"NO\",\"odds_x100\":183}]}" "$bbase/CreatePool" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "CreatePool code=$RESP_CODE body=$RESP_BODY"
POOL=$(echo "$RESP_BODY" | jq -r .id)
YES=$(echo "$RESP_BODY" | jq -r '.options[] | select(.label=="YES") | .id')
NO=$(echo "$RESP_BODY" | jq -r '.options[] | select(.label=="NO") | .id')
[ -n "$YES" ] && [ -n "$NO" ] || fail "options missing"
pass "pool $POOL created (rake $(echo "$RESP_BODY" | jq -r .rake_bps) bps)"

# -----------------------------------------------------------------------------
step "3/7 A bets 1000 YES; same-key replay must not double-debit"
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$YES\",\"stake_minor\":1000,\"idempotency_key\":\"m6-a-$TS\"}" "$bbase/PlaceBet" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "bet A code=$RESP_CODE body=$RESP_BODY"
[ "$(balance "$UA")" = "4000" ] || fail "A balance after bet != 4000"
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$YES\",\"stake_minor\":1000,\"idempotency_key\":\"m6-a-$TS\"}" "$bbase/PlaceBet" "$TOKA"
[ "$RESP_CODE" = "200" ] && [ "$(echo "$RESP_BODY" | jq -r .replayed)" = "true" ] || fail "replay not idempotent"
[ "$(balance "$UA")" = "4000" ] || fail "replay double-debited"
pass "A debited once (4000); replay flagged replayed=true"

# -----------------------------------------------------------------------------
step "4/7 B bets 600 NO; guards: non-member 403, oversize 422"
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$NO\",\"stake_minor\":600,\"idempotency_key\":\"m6-b-$TS\"}" "$bbase/PlaceBet" "$TOKB"
[ "$RESP_CODE" = "200" ] || fail "bet B code=$RESP_CODE body=$RESP_BODY"
[ "$(balance "$UB")" = "4400" ] || fail "B balance != 4400"
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$YES\",\"stake_minor\":100,\"idempotency_key\":\"m6-c-$TS\"}" "$bbase/PlaceBet" "$TOKC"
[ "$RESP_CODE" = "403" ] || fail "non-member bet expected 403, got $RESP_CODE"
[ "$(balance "$UC")" = "5000" ] || fail "non-member was debited"
req "{\"pool_id\":\"$POOL\",\"option_id\":\"$YES\",\"stake_minor\":999999,\"idempotency_key\":\"m6-big-$TS\"}" "$bbase/PlaceBet" "$TOKA"
[ "$RESP_CODE" = "422" ] || fail "oversize bet expected 422, got $RESP_CODE"
pass "B in (4400); C blocked (403); insufficient funds (422)"

# -----------------------------------------------------------------------------
step "5/7 settle YES → exact payouts (parimutuel, 5% rake)"
req "{\"pool_id\":\"$POOL\",\"winning_option_id\":\"$NO\"}" "$bbase/SettlePool" "$TOKB"
[ "$RESP_CODE" = "403" ] || fail "non-creator settle expected 403, got $RESP_CODE"
req "{\"pool_id\":\"$POOL\",\"winning_option_id\":\"$YES\"}" "$bbase/SettlePool" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "settle code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .status)" = "SETTLED" ] || fail "pool not SETTLED"
[ "$(balance "$UA")" = "5570" ] || fail "A settle payout wrong: $(balance "$UA") != 5570"
[ "$(balance "$UB")" = "4400" ] || fail "B changed on settle: $(balance "$UB")"
pass "A=5570 (stake back + 570 net), B=4400, house keeps 30 rake"

# -----------------------------------------------------------------------------
step "6/7 settle replay is idempotent"
req "{\"pool_id\":\"$POOL\",\"winning_option_id\":\"$YES\"}" "$bbase/SettlePool" "$TOKA"
[ "$RESP_CODE" = "200" ] || fail "settle replay code=$RESP_CODE"
[ "$(balance "$UA")" = "5570" ] || fail "settle replay changed money"
req "{\"pool_id\":\"$POOL\",\"winning_option_id\":\"$NO\"}" "$bbase/SettlePool" "$TOKA"
[ "$RESP_CODE" = "409" ] || fail "conflicting settle expected 409, got $RESP_CODE"
pass "replay stable; conflicting outcome rejected (409)"

# -----------------------------------------------------------------------------
step "7/7 Kafka carries bet.placed + pool.settled"
sleep 2
RAW=$($DC exec -T kafka kafka-console-consumer --bootstrap-server kafka:9092 \
    --topic "$BTOPIC" --from-beginning --timeout-ms "$CONSUME_MS" 2>/dev/null || true)
echo "$RAW" | grep -F "$POOL" | grep -Fq '"bet.placed.v1"' || fail "bet.placed.v1 not found on $BTOPIC"
echo "$RAW" | grep -F "$POOL" | grep -Fq '"bet.pool.settled.v1"' || fail "bet.pool.settled.v1 not found on $BTOPIC"
pass "events on $BTOPIC"

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M6 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Group pools work end-to-end: pool → member bets (wallet debits, idempotent)"
echo "→ parimutuel settle (wallet credits, exact) → Kafka lifecycle events"
