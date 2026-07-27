#!/usr/bin/env bash
# =============================================================================
# Aurora M1 — End-to-end smoke test (Event Mesh)
#
# Builds on M0. Verifies, in addition to the M0 happy path:
#   A. Schema Registry is up and the Avro contracts register
#   B. NATS is up (infra provisioned for later milestones)
#   C. CreateUser publishes a user.lifecycle.created.v1 event to Kafka
#   D. The published event conforms to the Avro field contract and matches
#      the user we just created
#
# Exit 0 = M1 verified.  Run after `make up && make wait-healthy`.
# =============================================================================

set -euo pipefail

IDENTITY_URL="${IDENTITY_URL:-http://localhost:8081}"
AI_URL="${AI_URL:-http://localhost:8082}"
SR_URL="${SR_URL:-http://localhost:8085}"
NATS_MON_URL="${NATS_MON_URL:-http://localhost:8222}"
TOPIC="${TOPIC:-aurora.identity.user-lifecycle.v1}"
SUBJECT="${SUBJECT:-aurora.identity.events.v1.UserLifecycleCreated}"
CONSUME_MS="${CONSUME_MS:-12000}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
cd "$ROOT"

# docker compose v2 vs v1
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
command -v jq   >/dev/null || fail "jq required (sudo apt install jq / brew install jq)"

# -----------------------------------------------------------------------------
step "1/7 identity-svc + ai-agent-svc health (M0 baseline)"
IH=$(curl -fsS "${IDENTITY_URL}/healthz") || fail "identity-svc /healthz unreachable"
[ "$(echo "$IH" | jq -r .status)" = "ok" ] || fail "identity-svc not ok: $IH"
AH=$(curl -fsS "${AI_URL}/aurora.ai.v1.AIAgentService/HealthCheck" -H 'Content-Type: application/json' -d '{}') \
    || fail "ai-agent-svc unreachable"
[ "$(echo "$AH" | jq -r .status)" = "ok" ] || fail "ai-agent-svc not ok: $AH"
pass "both services report status=ok"

# -----------------------------------------------------------------------------
step "2/7 NATS monitoring is up (infra provisioned)"
curl -fsS "${NATS_MON_URL}/healthz" >/dev/null || fail "NATS monitoring /healthz unreachable at ${NATS_MON_URL}"
pass "NATS is up"

# -----------------------------------------------------------------------------
step "3/7 Register Avro schemas to Schema Registry"
SR_URL="$SR_URL" bash "$SCRIPT_DIR/register_schemas.sh"
SUBJECTS=$(curl -fsS "${SR_URL}/subjects") || fail "could not list SR subjects"
echo "$SUBJECTS" | jq -e --arg s "$SUBJECT" 'any(.[]; . == $s)' >/dev/null \
    || fail "subject ${SUBJECT} not registered (got: $SUBJECTS)"
pass "subject ${SUBJECT} is registered"

# -----------------------------------------------------------------------------
step "4/7 Ensure Kafka topic exists (deterministic publish)"
$DC exec -T kafka kafka-topics --create --if-not-exists \
    --bootstrap-server kafka:9092 --topic "$TOPIC" \
    --partitions 1 --replication-factor 1 >/dev/null 2>&1 \
    || fail "could not ensure topic ${TOPIC} (is the kafka container up?)"
pass "topic ${TOPIC} ready"

# -----------------------------------------------------------------------------
step "5/7 Create a user (this should publish user.lifecycle.created.v1)"
EMAIL="smoke-m1-$(date +%s)@aurora.test"
CREATE=$(curl -fsS "${IDENTITY_URL}/aurora.identity.v1.IdentityService/CreateUser" \
    -H "Content-Type: application/json" \
    -d "{\"email\":\"${EMAIL}\",\"display_name\":\"Smoke M1\",\"kyc_country\":\"GB\"}")
USER_ID=$(echo "$CREATE" | jq -r .user.id)
[ -n "$USER_ID" ] && [ "$USER_ID" != "null" ] || fail "no user.id: $CREATE"
pass "created user ${USER_ID} (${EMAIL})"
sleep 2  # give the producer a moment to deliver

# -----------------------------------------------------------------------------
step "6/7 Consume the event from Kafka"
RAW=$($DC exec -T kafka kafka-console-consumer \
    --bootstrap-server kafka:9092 --topic "$TOPIC" \
    --from-beginning --timeout-ms "$CONSUME_MS" 2>/dev/null || true)
EVENT=$(echo "$RAW" | grep -F "$USER_ID" | tail -n1 || true)
[ -n "$EVENT" ] || fail "no event containing user_id ${USER_ID} found on ${TOPIC}"
pass "found the event on Kafka"
echo "  $EVENT" | jq -c . 2>/dev/null || echo "  $EVENT"

# -----------------------------------------------------------------------------
step "7/7 Validate event conforms to the Avro contract"
echo "$EVENT" | jq -e . >/dev/null 2>&1 || fail "event is not valid JSON"
for field in event_id event_type occurred_at_unix_ms user_id email display_name kyc_country created_at_unix_ms; do
    [ "$(echo "$EVENT" | jq -r --arg k "$field" 'has($k)')" = "true" ] \
        || fail "event missing Avro field: ${field}"
done
[ "$(echo "$EVENT" | jq -r .event_type)" = "user.lifecycle.created.v1" ] || fail "wrong event_type"
[ "$(echo "$EVENT" | jq -r .user_id)" = "$USER_ID" ] || fail "event user_id mismatch"
[ "$(echo "$EVENT" | jq -r .email)" = "$EMAIL" ]     || fail "event email mismatch"
pass "event matches the Avro contract and the created user"

# -----------------------------------------------------------------------------
echo ""
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo -e "${GREEN}  M1 SMOKE TEST PASSED${NC}"
echo -e "${GREEN}━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━${NC}"
echo ""
echo "Event mesh works:"
echo "  CreateUser → Postgres → Kafka(${TOPIC}) → user.lifecycle.created.v1"
echo "  contract governed by Schema Registry subject ${SUBJECT}"
