#!/usr/bin/env bash
# =============================================================================
# Aurora M1 — Register Avro schemas to Schema Registry
#
# For each libs/events/avro/*.avsc:
#   subject  = <namespace>.<RecordName>   (RecordNameStrategy; see ADR-0002 §3)
#   - set BACKWARD compatibility on the subject
#   - register the schema (idempotent: same schema returns the existing id)
#
# Usage:  bash scripts/register_schemas.sh
# Env:    SR_URL (default http://localhost:8085)
# =============================================================================

set -euo pipefail

SR_URL="${SR_URL:-http://localhost:8085}"
SR_WAIT_SECONDS="${SR_WAIT_SECONDS:-60}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
AVRO_DIR="$ROOT/libs/events/avro"

GREEN='\033[0;32m'; RED='\033[0;31m'; BLUE='\033[0;34m'; NC='\033[0m'
pass() { echo -e "${GREEN}✓${NC} $1"; }
fail() { echo -e "${RED}✗${NC} $1"; exit 1; }
info() { echo -e "${BLUE}→${NC} $1"; }

command -v curl >/dev/null || fail "curl required"
command -v jq   >/dev/null || fail "jq required"

# ---- wait for Schema Registry ----
info "Waiting up to ${SR_WAIT_SECONDS}s for Schema Registry at ${SR_URL} ..."
start=$(date +%s)
until curl -fsS "${SR_URL}/subjects" >/dev/null 2>&1; do
    if [ $(( $(date +%s) - start )) -gt "$SR_WAIT_SECONDS" ]; then
        fail "Schema Registry not ready after ${SR_WAIT_SECONDS}s"
    fi
    sleep 2
done
pass "Schema Registry is ready"

CT='Content-Type: application/vnd.schemaregistry.v1+json'
shopt -s nullglob
files=("$AVRO_DIR"/*.avsc)
[ ${#files[@]} -gt 0 ] || fail "no .avsc files found in $AVRO_DIR"

for f in "${files[@]}"; do
    subject="$(jq -r '.namespace + "." + .name' "$f")"
    [ -n "$subject" ] && [ "$subject" != "null" ] || fail "could not derive subject from $f"

    # Build the registration payload (schema must be a JSON-encoded string).
    payload="$(jq -n --arg s "$(cat "$f")" '{schema: $s, schemaType: "AVRO"}')"

    # Set BACKWARD compatibility (idempotent; allowed before the subject exists).
    curl -fsS -X PUT "${SR_URL}/config/${subject}" \
        -H "$CT" -d '{"compatibility":"BACKWARD"}' >/dev/null \
        || fail "failed to set compatibility for ${subject}"

    resp="$(curl -fsS -X POST "${SR_URL}/subjects/${subject}/versions" -H "$CT" -d "$payload")" \
        || fail "failed to register ${subject}: ${resp:-<no response>}"
    sid="$(echo "$resp" | jq -r '.id')"
    pass "registered ${subject}  (schema id=${sid})  [$(basename "$f")]"
done

echo ""
pass "All ${#files[@]} schema(s) registered to ${SR_URL}"
