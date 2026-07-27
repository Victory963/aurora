#!/usr/bin/env bash
# =============================================================================
# Aurora M3 — End-to-end smoke test (wallet-svc: ledger / idempotency / outbox)
#
# Builds on M0-M2. Verifies:
#   1. health (identity + wallet)
#   2. register Avro schemas (incl. wallet.transaction.completed.v1)
#   3. create a user (identity) -> wallet-svc CONSUMES user.lifecycle.created.v1
#      and auto-opens a wallet (balance 0)
#   4. Credit 1000 -> 1000;  Debit 300 -> 700
#   5. idempotent replay of the Credit -> balance unchanged (no double-credit)
#   6. Debit 99999 -> insufficient_funds, balance unchanged
#   7. a wallet.transaction.completed.v1 event reaches Kafka via the outbox relay
#
# Exit 0 = M3 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

ID="${IDENTITY_URL:-http://localhost:8081}"
WALLET="${WALLET_URL:-http://localhost:8083}"
SR_URL="${SR_URL:-http://localhost:8085}"
WTOPIC="${WTOPIC:-aurora.wallet.transaction.v1}"
WSUBJECT="${WSUBJECT:-aurora.wallet.events.v1.WalletTransactionCompleted}"
CONSUME_MS="${CONSUME_MS:-12000}"
OPEN_WAIT="${OPEN_WAIT:-40}"

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

wbase="$WALLET/aurora.wallet.v1.WalletService"

RESP_BODY=""; RESP_CODE=""
req() {
    local data="$1" url="$2" out
    out=$(curl -s -w $'\n%{http_code}' -H "Content-Type: application/json" -d "$data" "$url")
    RESP_CODE="${out##*$'\n'}"
    RESP_BODY="${out%$'\n'*}"
}

balance() { # echoes balance or empty if 404
    req "{\"user_id\":\"$1\"}" "$wbase/GetBalance"
    if [ "$RESP_CODE" = "200" ]; then echo "$RESP_BODY" | jq -r .balance_minor; else echo ""; fi
}

# -----------------------------------------------------------------------------
step "1/7 health (identity + wallet)"
[ "$(curl -fsS "$ID/healthz" | jq -r .status)" = "ok" ] || fail "identity-svc not ok"
[ "$(curl -fsS "$WALLET/healthz" | jq -r .status)" = "ok" ] || fail "wallet-svc not ok"
pass "identity + wallet healthy"

# -----------------------------------------------------------------------------
step "2/7 register Avro schemas (incl. wallet event)"
SR_URL="$SR_URL" bash "$SCRIPT_DIR/register_schemas.sh" >/dev/null
SUBJECTS=$(curl -fsS "${SR_URL}/subjects")
echo "$SUBJECTS" | jq -e --arg s "$WSUBJECT" 'any(.[]; . == $s)' >/dev/null || fail "wallet subject ${WSUBJECT} not registered"
pass "wallet schema registered"

# -----------------------------------------------------------------------------
step "3/7 create user -> wallet auto-opens (consumes user.lifecycle.created.v1)"
EMAIL="smoke-m3-$(date +%s)@aurora.test"
CREATE=$(curl -fsS "$ID/aurora.identity.v1.IdentityService/CreateUser" -H 'Content-Type: application/json' \
    -d "{\"email\":\"${EMAIL}\",\"display_name\":\"Smoke M3\",\"kyc_country\":\"GB\"}")
USER_ID=$(echo "$CREATE" | jq -r .user.id)
[ -n "$USER_ID" ] && [ "$USER_ID" != "null" ] || fail "no user.id: $CREATE"
info "created user ${USER_ID}; waiting up to ${OPEN_WAIT}s for wallet to open..."
OPENED=""
for _ in $(seq 1 "$OPEN_WAIT"); do
    B=$(balance "$USER_ID")
    if [ -n "$B" ]; then OPENED="y"; break; fi
    sleep 1
done
[ "$OPENED" = "y" ] || fail "wallet not opened by consumer within ${OPEN_WAIT}s"
[ "$(balance "$USER_ID")" = "0" ] || fail "fresh wallet balance should be 0"
pass "wallet auto-opened with balance 0 (event mesh consumer works)"

# -----------------------------------------------------------------------------
step "4/7 Credit 1000 then Debit 300"
# Pre-create the wallet event topic so the relay's publish (and the step-7
# consume) don't race Kafka topic auto-creation.
$DC exec -T kafka kafka-topics --create --if-not-exists \
    --bootstrap-server kafka:9092 --topic "$WTOPIC" --partitions 1 --replication-factor 1 >/dev/null 2>&1 || true
CREDIT_KEY="m3-credit-$(date +%s)-$$"
req "{\"user_id\":\"${USER_ID}\",\"amount_minor\":1000,\"idempotency_key\":\"${CREDIT_KEY}\"}" "$wbase/Credit"
[ "$RESP_CODE" = "200" ] || fail "Credit code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .resulting_balance_minor)" = "1000" ] || fail "credit balance != 1000"
req "{\"user_id\":\"${USER_ID}\",\"amount_minor\":300,\"idempotency_key\":\"m3-debit-$(date +%s)-$$\"}" "$wbase/Debit"
[ "$RESP_CODE" = "200" ] || fail "Debit code=$RESP_CODE body=$RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .resulting_balance_minor)" = "700" ] || fail "debit balance != 700"
pass "balance is 700 after credit 1000 / debit 300"

# -----------------------------------------------------------------------------
step "5/7 idempotent replay (same Credit key) must NOT double-credit"
req "{\"user_id\":\"${USER_ID}\",\"amount_minor\":1000,\"idempotency_key\":\"${CREDIT_KEY}\"}" "$wbase/Credit"
[ "$RESP_CODE" = "200" ] || fail "replay code=$RESP_CODE"
[ "$(echo "$RESP_BODY" | jq -r .replayed)" = "true" ] || fail "replay should set replayed=true"
[ "$(balance "$USER_ID")" = "700" ] || fail "replay double-credited (balance != 700)"
pass "replay returned replayed=true; balance still 700"

# -----------------------------------------------------------------------------
step "6/7 insufficient funds is rejected"
req "{\"user_id\":\"${USER_ID}\",\"amount_minor\":99999,\"idempotency_key\":\"m3-big-$(date +%s)-$$\"}" "$wbase/Debit"
[ "$RESP_CODE" = "422" ] || fail "over-debit expected 422, got $RESP_CODE: $RESP_BODY"
[ "$(echo "$RESP_BODY" | jq -r .code)" = "insufficient_funds" ] || fail "expected insufficient_funds"
[ "$(balance "$USER_ID")" = "700" ] || fail "balance changed after rejected debit"
pass "insufficient debit rejected (422); balance unchanged"

# -----------------------------------------------------------------------------
step "7/7 wallet.transaction.completed.v1 event reached Kafka (via outbox relay)"
sleep 3
RAW=$($DC exec -T kafka kafka-console-consumer \
    --bootstrap-server kafka:9092 --topic "$WTOPIC" \
    --from-beginning --timeout-ms "$CONSUME_MS" 2>/dev/null || true)
EVENT=$(echo "$RAW" | grep -F "$USER_ID" | tail -n1 || true)
[ -n "$EVENT" ] || fail "no wallet event for ${USER_ID} on ${WTOPIC} (outbox relay?)"
echo "$EVENT" | jq -e . >/dev/null 2>&1 || fail "event not valid JSON"
for field in event_id event_type occurred_at_unix_ms transaction_id idempotency_key user_id tx_type amount_minor currency resulting_balance_minor; do
    [ "$(echo "$EVENT" | jq -r --arg k "$field" 'has($k)')" = "true" ] || fail "event missing Avro field: $field"
done
[ "$(echo "$EVENT" | jq -r .event_type)" = "wallet.transaction.completed.v1" ] || fail "bad event_type"
[ "$(echo "$EVENT" | jq -r .user_id)" = "$USER_ID" ] || fail "event user_id mismatch"
pass "outbox-relayed event matches the Avro contract"

# -----------------------------------------------------------------------------
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M3 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Wallet works:"
echo "  user.created → consumer opens wallet → Credit/Debit (idempotent, double-entry)"
echo "  → balance → outbox → Kafka(${WTOPIC})"
