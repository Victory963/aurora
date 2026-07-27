# =============================================================================
# Aurora — Top-level Makefile
#
# Quick start:
#   make up            # Start everything via docker compose
#   make smoke         # Run M0 end-to-end happy path
#   make logs          # Tail logs
#   make down          # Stop everything
#   make clean         # Remove volumes (resets DB)
#
# Per-service:
#   make test-identity # Go tests for identity-svc
#   make test-ai       # Python tests for ai-agent-svc
#
# Show all targets:
#   make help
# =============================================================================

.DEFAULT_GOAL := help
SHELL := /bin/bash

# Detect docker compose v2 vs docker-compose v1
DOCKER_COMPOSE := $(shell docker compose version >/dev/null 2>&1 && echo "docker compose" || echo "docker-compose")

# ---------- Help ----------
.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-22s\033[0m %s\n", $$1, $$2}'

# ---------- Lifecycle ----------
.PHONY: up
up: ## Start all services (docker compose up -d)
	@echo "==> Building and starting Aurora M0 stack..."
	$(DOCKER_COMPOSE) up -d --build
	@echo ""
	@echo "✓ Stack started. Endpoints:"
	@echo "    identity-svc:    http://localhost:8081/healthz"
	@echo "    ai-agent-svc:    http://localhost:8082/healthz"
	@echo "    wallet-svc:      http://localhost:8083/healthz"
	@echo "    room-svc:        http://localhost:8084/healthz"
	@echo "    bet-svc:         http://localhost:8086/healthz"
	@echo "    redis:           localhost:6379  (M5 AI checkpoints)"
	@echo "    postgres:        localhost:5432  (user=aurora pass=aurora_dev_password)"
	@echo "    scylla:          localhost:9042  (cql)"
	@echo "    kafka:           localhost:29092 (in-network: kafka:9092)"
	@echo "    schema-registry: http://localhost:8085/subjects"
	@echo "    nats:            localhost:4222  (monitoring http://localhost:8222)"
	@echo ""
	@echo "Run \`make smoke\` (M0) or \`make smoke-m1\` (Event Mesh) to verify end-to-end."

.PHONY: down
down: ## Stop all services (preserves volumes)
	$(DOCKER_COMPOSE) down

.PHONY: clean
clean: ## Stop services AND remove all data volumes
	$(DOCKER_COMPOSE) down -v
	@echo "✓ Stopped and removed all volumes."

.PHONY: logs
logs: ## Tail logs from all services
	$(DOCKER_COMPOSE) logs -f --tail=100

.PHONY: ps
ps: ## Show status of all services
	$(DOCKER_COMPOSE) ps

.PHONY: restart
restart: down up ## Restart the stack

# ---------- Smoke test (the real M0 verification) ----------
.PHONY: smoke
smoke: ## Run the M0 end-to-end happy path (the real M0 verification)
	@bash scripts/smoke_m0.sh

.PHONY: smoke-m1
smoke-m1: ## Run the M1 Event Mesh end-to-end verification (waits for health first)
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m1.sh

.PHONY: smoke-m2
smoke-m2: ## Run the M2 identity login/session/device verification (waits for health first)
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m2.sh

.PHONY: smoke-m3
smoke-m3: ## Run the M3 wallet verification: credit/debit/balance/idempotency/outbox
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m3.sh

.PHONY: smoke-m4
smoke-m4: ## Run the M4 room verification: create/join/members/chat(scylla)/NATS signal
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m4.sh

.PHONY: smoke-m5
smoke-m5: ## Run the M5 AI verification: tool-audited recommendations (degrade-safe, no key needed)
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m5.sh

.PHONY: smoke-m6
smoke-m6: ## Run the M6 bet verification: pools/bets/parimutuel settlement/idempotency/Kafka
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m6.sh

.PHONY: smoke-m7
smoke-m7: ## Run the M7 AI-in-room verification: ProposeAiPool → pool → group bet → settle
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m7.sh

.PHONY: smoke-m8
smoke-m8: ## Run the M8 identity-graph verification: projection/co-occurrence/Sybil/influence/GDPR
	@bash scripts/wait_healthy.sh
	@bash scripts/smoke_m8.sh

.PHONY: register-schemas
register-schemas: ## Register libs/events/avro/*.avsc to Schema Registry
	@bash scripts/register_schemas.sh

.PHONY: wait-healthy
wait-healthy: ## Block until all services report healthy
	@bash scripts/wait_healthy.sh

# ---------- Per-service tests ----------
.PHONY: test-identity
test-identity: ## Run identity-svc Go unit tests (no DB required)
	# `go mod tidy` (not just `download`) so missing indirect requires + go.sum are
	# resolved on a fresh clone; the readonly `go test` then builds. Commit the result.
	cd services/identity-svc && go mod tidy && go test ./... -count=1

.PHONY: test-identity-integration
test-identity-integration: ## Run identity-svc integration tests (needs `make up`)
	cd services/identity-svc && AURORA_TEST_DB=1 go test ./... -count=1

.PHONY: test-ai
test-ai: ## Run ai-agent-svc Python tests (self-contained: builds a local venv)
	cd services/ai-agent-svc && python3 -m venv .venv \
		&& .venv/bin/python -m pip install -q --upgrade pip \
		&& .venv/bin/python -m pip install -q -e ".[dev]" \
		&& .venv/bin/python -m pytest tests/ -v

.PHONY: test-wallet
test-wallet: ## Run wallet-svc Go unit tests (no DB required)
	cd services/wallet-svc && go mod tidy && go test ./... -count=1

.PHONY: test-wallet-integration
test-wallet-integration: ## Run wallet-svc ledger integration tests (needs `make up`)
	cd services/wallet-svc && AURORA_TEST_DB=1 go test ./... -count=1

.PHONY: test-room
test-room: ## Run room-svc Go unit tests (no DB/Scylla required)
	cd services/room-svc && go mod tidy && go test ./... -count=1

.PHONY: test-bet
test-bet: ## Run bet-svc Go unit tests (poolmath + settlement flow, no DB required)
	cd services/bet-svc && go mod tidy && go test ./... -count=1

.PHONY: test-graph
test-graph: ## Run graph-svc Go unit tests (projection + queries, no Neo4j required)
	cd services/graph-svc && go mod tidy && go test ./... -count=1

.PHONY: test
test: test-identity test-ai test-wallet test-room test-bet test-graph ## Run all unit tests

# ---------- Code quality (M1+ will add more) ----------
.PHONY: tidy
tidy: tidy-identity tidy-wallet tidy-room tidy-bet tidy-graph ## Resolve Go deps and (re)write go.sum for all Go services (commit results)

.PHONY: tidy-identity
tidy-identity: ## go mod tidy for identity-svc
	cd services/identity-svc && go mod tidy

.PHONY: tidy-wallet
tidy-wallet: ## go mod tidy for wallet-svc
	cd services/wallet-svc && go mod tidy

.PHONY: tidy-room
tidy-room: ## go mod tidy for room-svc
	cd services/room-svc && go mod tidy

.PHONY: tidy-bet
tidy-bet: ## go mod tidy for bet-svc
	cd services/bet-svc && go mod tidy

.PHONY: tidy-graph
tidy-graph: ## go mod tidy for graph-svc
	cd services/graph-svc && go mod tidy

.PHONY: fmt
fmt: ## Format all code
	cd services/identity-svc && gofmt -w .
	cd services/wallet-svc && gofmt -w .
	cd services/room-svc && gofmt -w .
	cd services/bet-svc && gofmt -w .
	cd services/ai-agent-svc && python3 -m ruff format aurora_ai tests || true

.PHONY: lint
lint: ## Lint all code (warnings only)
	cd services/identity-svc && go vet ./... || true
	cd services/wallet-svc && go vet ./... || true
	cd services/room-svc && go vet ./... || true
	cd services/bet-svc && go vet ./... || true
	cd services/ai-agent-svc && python3 -m ruff check aurora_ai tests || true

# ---------- Convenience ----------
.PHONY: shell-pg
shell-pg: ## psql into Aurora postgres
	$(DOCKER_COMPOSE) exec postgres psql -U aurora -d aurora_identity

.PHONY: build
build: ## docker build only (no run)
	$(DOCKER_COMPOSE) build
