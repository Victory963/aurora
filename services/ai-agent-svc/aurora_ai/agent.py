"""aurora_ai.agent — M5: real recommendation engine.

Public contract (ADR-0001, unchanged since M0 so server.py needs zero edits):

    generate_recommendations(user_id, mode, match_id, room_id) -> list[dict]

M0 returned canned placeholders. M5 runs the LangGraph pipeline in
aurora_ai.pipeline (tools → optional Claude call → quality gate → audit); every
failure mode degrades to a deterministic template built from the same tool
data, so this function never raises and never needs the network to answer.
RG-blocked users get an empty list (server returns 200 with no recs).
"""

from __future__ import annotations

import logging
from typing import Any

from . import pipeline
from .models import Mode

logger = logging.getLogger(__name__)


def generate_recommendations(
    user_id: str,
    mode: Mode,
    match_id: str,
    room_id: str,
) -> list[dict[str, Any]]:
    if mode == Mode.UNSPECIFIED:
        mode = Mode.SOLO

    try:
        state = pipeline.run(user_id=user_id, mode=int(mode), match_id=match_id, room_id=room_id)
        return state.get("recommendations", [])
    except Exception:  # absolute backstop: the recommend endpoint must not 500
        logger.exception("pipeline hard failure; returning empty recommendations")
        return []
