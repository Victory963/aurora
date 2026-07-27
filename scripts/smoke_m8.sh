#!/usr/bin/env bash
# =============================================================================
# Aurora M8 — End-to-end smoke test (graph-svc: Identity Graph projection)
#
# Flow:
#   1. graph-svc healthy (Neo4j + projection enabled)
#   2. users A/B/C; A opens a room; B/C join; two pools; A bets first,
#      B FOLLOWS A (same selection) on both pools, C bets the opposite side
#   3. projection converges (poll ≤90s):
#        CoBettors(A)  ∋ B(shared=2) and C
#        Sybil(A,min=2) ∋ B (2 same-selection pools), ∌ C (opposite side)
#        Influence(A): followers ≥ 1, follow_events ≥ 2
#   4. PII boundary: GetUserGraph(A) has no email / display_name
#   5. GDPR: DeleteUser(B) → B vanishes from CoBettors(A)
#   6. queries without Bearer → 401
#
# Exit 0 = M8 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
WALLET="${WALLET_URL:-http://localhost:8083}"
ROOM="${ROOM_URL:-http://localhost:8084}"
BET="${BET_URL:-http://localhost:8086}"
GRAPH="${GRAPH_URL:-http://localhost:8087}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$(dirname "$SCRIPT_DIR")"

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }
step() { echo -e "\n${YELLOW}━━━ $1 ━━━${NC}"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

gbase="$GRAPH/aurora.graph.v1.GraphService"

signup_login() { # EMAIL -> "user_id access_token"
    local email="$1" pw="graph-pass-123" uid tok
    uid=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"display_name\":\"G\",\"kyc_country\":\"JP\",\"password\":\"$pw\"}" | jq -r .user.id)
    tok=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/Login" -H 'Content-Type: application/json' \
        -d "{\"email\":\"$email\",\"password\":\"$pw\",\"device\":{\"platform\":\"web\"}}" | jq -r .access_token)
    echo "$uid $tok"
}

wallet_wait_fund() { # USER_ID KEY
    for _ in $(seq 1 40); do
        if curl -fsS "$WALLET/aurora.wallet.v1.WalletService/GetBalance" -H 'Content-Type: application/json' \
            -d "{\"user_id\":\"$1\"}" >/dev/null 2>&1; then break; fi
        sleep 1
    done
    curl -fsS "$WALLET/aurora.wallet.v1.WalletService/Credit" -H 'Content-Type: application/json' \
        -d "{\"user_id\":\"$1\",\"amount_minor\":5000,\"idempotency_key\":\"$2\"}" >/dev/null
}

make_pool() { # TOKEN ROOM_ID QUESTION -> pool json
    curl -fsS "$BET/aurora.bet.v1.BetService/CreatePool" -H 'Content-Type: application/json' \
        -H "Authorization: Bearer $1" \
        -d "{\"room_id\":\"$2\",\"question\":\"$3\",\"options\":[{\"label\":\"YES\",\"odds_x100\":200},{\"label\":\"NO\",\"odds_x100\":200}]}"
}

place() { # TOKEN POOL OPTION STAKE KEY
    curl -fsS "$BET/aurora.bet.v1.BetService/PlaceBet" -H 'Content-Type: application/json' \
        -H "Authorization: Bearer $1" \
        -d "{\"pool_id\":\"$2\",\"option_id\":\"$3\",\"stake_minor\":$4,\"idempotency_key\":\"$5\"}" >/dev/null
}

gquery() { # TOKEN METHOD BODY
    curl -fsS "$gbase/$2" -H 'Content-Type: application/json' -H "Authorization: Bearer $1" -d "$3"
}

# -----------------------------------------------------------------------------
step "1/6 graph-svc health"
H=$(curl -fsS "$GRAPH/healthz")
[ "$(echo "$H" | jq -r .status)" = "ok" ] || fail "graph-svc not ok: $H"
[ "$(echo "$H" | jq -r .details.projection)" = "enabled" ] || fail "projection not enabled: $H"
pass "graph-svc ok (neo4j + projection enabled)"

# -----------------------------------------------------------------------------
step "2/6 users + room + two pools; B follows A twice, C opposes"
TS="$(date +%s)-$$-${RANDOM}"
read -r UA TOKA <<<"$(signup_login "m8-a-$TS@aurora.test")"
read -r UB TOKB <<<"$(signup_login "m8-b-$TS@aurora.test")"
read -r UC TOKC <<<"$(signup_login "m8-c-$TS@aurora.test")"
wallet_wait_fund "$UA" "m8-fund-a-$TS"; wallet_wait_fund "$UB" "m8-fund-b-$TS"; wallet_wait_fund "$UC" "m8-fund-c-$TS"

RID=$(curl -fsS "$ROOM/aurora.room.v1.RoomService/CreateRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKA" -d '{"name":"m8-graph-room"}' | jq -r .id)
curl -fsS "$ROOM/aurora.room.v1.RoomService/JoinRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKB" -d "{\"room_id\":\"$RID\"}" >/dev/null
curl -fsS "$ROOM/aurora.room.v1.RoomService/JoinRoom" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKC" -d "{\"room_id\":\"$RID\"}" >/dev/null

for i in 1 2; do
    P=$(make_pool "$TOKA" "$RID" "m8 pool $i")
    PID=$(echo "$P" | jq -r .id)
    YES=$(echo "$P" | jq -r '.options[] | select(.label=="YES") | .id')
    NO=$(echo "$P" | jq -r '.options[] | select(.label=="NO") | .id')
    place "$TOKA" "$PID" "$YES" 300 "m8-a$i-$TS"   # A leads YES
    sleep 1                                        # follower lag must be observable
    place "$TOKB" "$PID" "$YES" 200 "m8-b$i-$TS"   # B follows YES  (sybil-ish)
    place "$TOKC" "$PID" "$NO"  200 "m8-c$i-$TS"   # C opposes NO
done
pass "2 pools: A→YES first, B→YES after (×2), C→NO (×2)"

# -----------------------------------------------------------------------------
step "3/6 projection converges: co-occurrence / sybil / influence"
DEADLINE=$(( $(date +%s) + 90 ))
until CO=$(gquery "$TOKA" CoBettors "{\"user_id\":\"$UA\"}") \
   && [ "$(echo "$CO" | jq -r --arg b "$UB" '[.co_bettors[] | select(.user_id==$b)][0].shared_pools')" = "2" ] \
   && echo "$CO" | jq -e --arg c "$UC" '[.co_bettors[] | select(.user_id==$c)] | length == 1' >/dev/null; do
    [ "$(date +%s)" -gt "$DEADLINE" ] && fail "projection did not converge: $CO"
    sleep 3
done
pass "CoBettors(A): B shared=2, C present (co-occurrence)"

SY=$(gquery "$TOKA" SybilCandidates "{\"user_id\":\"$UA\",\"min_shared\":2}")
echo "$SY" | jq -e --arg b "$UB" '[.candidates[] | select(.user_id==$b)] | length == 1' >/dev/null \
    || fail "sybil should flag B: $SY"
echo "$SY" | jq -e --arg c "$UC" '[.candidates[] | select(.user_id==$c)] | length == 0' >/dev/null \
    || fail "sybil must NOT flag C (opposite side): $SY"
pass "Sybil(A,min=2): flags B, clears C"

INF=$(gquery "$TOKA" Influence "{\"user_id\":\"$UA\"}")
FOLLOWERS=$(echo "$INF" | jq -r .followers)
EVENTS=$(echo "$INF" | jq -r .follow_events)
[ "$FOLLOWERS" -ge 1 ] && [ "$EVENTS" -ge 2 ] || fail "influence too low: $INF"
pass "Influence(A): followers=$FOLLOWERS follow_events=$EVENTS (KOL signal)"

# -----------------------------------------------------------------------------
step "4/6 PII boundary"
UG=$(gquery "$TOKA" GetUserGraph "{\"user_id\":\"$UA\"}")
echo "$UG" | grep -qE '"email"|"display_name"' && fail "PII leaked into graph: $UG"
[ "$(echo "$UG" | jq -r .kyc_country)" = "JP" ] || fail "kyc_country missing: $UG"
[ "$(echo "$UG" | jq '.bets | length')" = "2" ] || fail "expected 2 bets: $UG"
pass "GetUserGraph: ids only — no email/display_name; 2 bets projected"

# -----------------------------------------------------------------------------
step "5/6 GDPR: delete B → B vanishes from the graph"
curl -fsS "$ID/aurora.identity.v1.IdentityService/DeleteUser" -H 'Content-Type: application/json' \
    -H "Authorization: Bearer $TOKB" -d '{}' >/dev/null
DEADLINE=$(( $(date +%s) + 60 ))
until CO=$(gquery "$TOKA" CoBettors "{\"user_id\":\"$UA\"}") \
   && echo "$CO" | jq -e --arg b "$UB" '[.co_bettors[] | select(.user_id==$b)] | length == 0' >/dev/null; do
    [ "$(date +%s)" -gt "$DEADLINE" ] && fail "B still in graph after DeleteUser: $CO"
    sleep 3
done
pass "user.lifecycle.deleted.v1 → DETACH DELETE (B gone from CoBettors)"

# -----------------------------------------------------------------------------
step "6/6 auth gate"
CODE=$(curl -s -o /dev/null -w "%{http_code}" "$gbase/CoBettors" -H 'Content-Type: application/json' -d "{\"user_id\":\"$UA\"}")
[ "$CODE" = "401" ] || fail "expected 401 without bearer, got $CODE"
pass "queries are Bearer-gated"

echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M8 SMOKE TEST PASSED — Identity Graph is real${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Kafka events (identity+bet) → Neo4j projection → co-occurrence / Sybil /"
echo "KOL-influence queries, PII-free, GDPR delete honored."
