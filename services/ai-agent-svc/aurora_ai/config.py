"""M5 runtime configuration for the AI pipeline (AURORA_AI_* env vars).

Read lazily (functions, not module constants) so tests can monkeypatch the
environment per-case.
"""

from __future__ import annotations

import os

DEFAULT_MODEL = "claude-opus-4-8"


def api_key() -> str:
    """Anthropic API key. AURORA_AI_ANTHROPIC_API_KEY wins over the standard
    ANTHROPIC_API_KEY. Empty string => LLM disabled, fallback path only."""
    return os.getenv("AURORA_AI_ANTHROPIC_API_KEY") or os.getenv("ANTHROPIC_API_KEY") or ""


def model() -> str:
    return os.getenv("AURORA_AI_MODEL") or DEFAULT_MODEL


def llm_timeout_s() -> float:
    try:
        return float(os.getenv("AURORA_AI_LLM_TIMEOUT_S", "20"))
    except ValueError:
        return 20.0


def llm_effort() -> str:
    """output_config.effort for the recommendation call. 'low' keeps latency
    sane for a live-betting surface; raise via env for quality experiments."""
    return os.getenv("AURORA_AI_LLM_EFFORT", "low")


def redis_url() -> str:
    """LangGraph checkpoint Redis. Empty => in-memory checkpointer."""
    return os.getenv("AURORA_AI_REDIS_URL", "")
