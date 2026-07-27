#!/usr/bin/env bash
# Block until all Aurora services report healthy.
# Used by CI / make wait-healthy / and the smoke test convenience.

set -euo pipefail

# Headroom for the slowest cold start: identity is gated on kafka (zookeeper→kafka),
# and room-svc is gated on ScyllaDB, which can take 1-2 min to accept CQL.
MAX_WAIT_SECONDS="${MAX_WAIT_SECONDS:-240}"

# service-name=health-url pairs.
SERVICES=(
    "identity=${IDENTITY_URL:-http://localhost:8081}/healthz"
    "ai-agent=${AI_URL:-http://localhost:8082}/healthz"
    "wallet=${WALLET_URL:-http://localhost:8083}/healthz"
    "room=${ROOM_URL:-http://localhost:8084}/healthz"
    "bet=${BET_URL:-http://localhost:8086}/healthz"
    "graph=${GRAPH_URL:-http://localhost:8087}/healthz"
)

echo "Waiting up to ${MAX_WAIT_SECONDS}s for services to be healthy..."

start=$(date +%s)
while true; do
    elapsed=$(( $(date +%s) - start ))
    if [ "$elapsed" -gt "$MAX_WAIT_SECONDS" ]; then
        echo ""
        echo "✗ Timeout after ${MAX_WAIT_SECONDS}s"
        docker compose ps 2>/dev/null || true
        exit 1
    fi

    all_ok="y"
    line=""
    for entry in "${SERVICES[@]}"; do
        name="${entry%%=*}"
        url="${entry#*=}"
        if curl -fsS "$url" >/dev/null 2>&1; then
            line+="  ${name}=y"
        else
            line+="  ${name}=n"
            all_ok="n"
        fi
    done

    if [ "$all_ok" = "y" ]; then
        echo "✓ All services healthy (${elapsed}s)"
        exit 0
    fi

    printf "%s  (%ds elapsed)\r" "$line" "$elapsed"
    sleep 2
done
