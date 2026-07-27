"""Test bootstrap: force the deterministic fallback path.

Unit tests must never hit the network — even on a developer machine that has
ANTHROPIC_API_KEY exported. Removing the keys makes llm.reason raise LLMError
("no API key configured") and the pipeline take the template path.
"""

from __future__ import annotations

import pytest


@pytest.fixture(autouse=True)
def _no_llm_env(monkeypatch):
    monkeypatch.delenv("ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("AURORA_AI_ANTHROPIC_API_KEY", raising=False)
    monkeypatch.delenv("AURORA_AI_REDIS_URL", raising=False)
