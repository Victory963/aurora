"""Anthropic (Claude API) wrapper for the M5 recommendation pipeline.

Design constraints (see docs/m5/PLAN.md):
- The LLM PICKS markets and explains WHY; it never invents numbers. It may only
  reference the markets provided by the odds tool, and the pipeline re-attaches
  the tool odds + recomputes EV, so a hallucinated number cannot leak out.
- Structured output via output_config.format (json_schema) => response text is
  guaranteed schema-valid JSON.
- Model default claude-opus-4-8. NOTE: Opus 4.7+ rejects temperature/top_p and
  budget_tokens with a 400 — we send neither; `thinking` is omitted on purpose.
- Every failure raises LLMError; callers degrade to the template path.
"""

from __future__ import annotations

import json
import logging
from typing import Any

from . import config

logger = logging.getLogger(__name__)


class LLMError(Exception):
    """Any condition that means 'no usable LLM output' (missing key, SDK missing,
    API error, refusal, truncation, malformed JSON)."""


_OUTPUT_SCHEMA: dict[str, Any] = {
    "type": "object",
    "properties": {
        "recommendations": {
            "type": "array",
            "items": {
                "type": "object",
                "properties": {
                    "market_id": {"type": "string"},
                    "selection": {"type": "string"},
                    "ai_confidence": {"type": "number"},
                    "reasoning": {"type": "string"},
                },
                "required": ["market_id", "selection", "ai_confidence", "reasoning"],
                "additionalProperties": False,
            },
        }
    },
    "required": ["recommendations"],
    "additionalProperties": False,
}

_SYSTEM = (
    "You are Aurora's in-play sports betting recommendation engine. You will be "
    "given live match statistics, the bookmaker markets currently available, our "
    "internal pool-implied odds, and optionally room sentiment or a KOL persona "
    "card.\n"
    "Rules:\n"
    "- Recommend 1-3 selections, ONLY from the provided markets list, copying "
    "market_id and selection EXACTLY as given. Never invent markets or odds.\n"
    "- ai_confidence is your probability (0..1) that the selection wins; be "
    "calibrated, not promotional.\n"
    "- reasoning: 1-3 sentences grounded in the provided statistics (cite the "
    "actual numbers). If a persona card is present, write in that persona's "
    "voice. If room sentiment is present, you may reference the room's mood.\n"
    "- If no market offers positive expected value, still return the least-bad "
    "single option with honest low confidence."
)


def reason(context: dict[str, Any]) -> list[dict[str, Any]]:
    """One structured-output call to Claude. Returns the raw recommendation
    picks (market_id/selection/ai_confidence/reasoning). Raises LLMError."""
    key = config.api_key()
    if not key:
        raise LLMError("no API key configured")

    try:
        import anthropic
    except Exception as exc:  # pragma: no cover - env without the SDK
        raise LLMError(f"anthropic SDK unavailable: {type(exc).__name__}") from exc

    client = anthropic.Anthropic(api_key=key, timeout=config.llm_timeout_s(), max_retries=1)
    prompt = json.dumps(context, ensure_ascii=False, sort_keys=True)

    try:
        response = client.messages.create(
            model=config.model(),
            max_tokens=1500,
            system=_SYSTEM,
            output_config={
                "format": {"type": "json_schema", "schema": _OUTPUT_SCHEMA},
                "effort": config.llm_effort(),
            },
            messages=[{"role": "user", "content": prompt}],
        )
    except anthropic.APIConnectionError as exc:
        raise LLMError(f"connection error: {exc}") from exc
    except anthropic.APIStatusError as exc:
        raise LLMError(f"api error {exc.status_code}: {getattr(exc, 'type', '')}") from exc
    except anthropic.APIError as exc:  # any other SDK error
        raise LLMError(f"api error: {exc}") from exc

    if response.stop_reason == "refusal":
        raise LLMError("model refused")
    if response.stop_reason == "max_tokens":
        raise LLMError("output truncated")

    text = "".join(b.text for b in response.content if getattr(b, "type", "") == "text")
    try:
        parsed = json.loads(text)
    except json.JSONDecodeError as exc:
        raise LLMError("non-JSON model output") from exc
    if not isinstance(parsed, dict):
        raise LLMError("model output is not a JSON object")

    recs = parsed.get("recommendations")
    if not isinstance(recs, list) or not recs:
        raise LLMError("empty recommendations")
    logger.info("llm ok model=%s picks=%d", config.model(), len(recs))
    return recs
