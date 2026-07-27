-- =============================================================================
-- Aurora M0 — Postgres init script
--
-- Runs once on first container startup. identity-svc itself runs its own
-- migrations via internal/store/store.go::Migrate(), so this file is currently
-- a stub. Future use:
--   M3: create aurora_wallet database + role
--   M6: create aurora_bet database + role
--   M2: install pgcrypto extension for token hashing
-- =============================================================================

-- Pre-create databases for services that will join in later milestones.
-- Idempotent: doesn't fail if they already exist.

SELECT 'CREATE DATABASE aurora_wallet OWNER aurora'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'aurora_wallet')\gexec

SELECT 'CREATE DATABASE aurora_room OWNER aurora'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'aurora_room')\gexec

SELECT 'CREATE DATABASE aurora_bet OWNER aurora'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'aurora_bet')\gexec
