"""
Pydantic models mirroring proto messages in libs/proto/aurora/ai/v1/ai.proto.

M0 keeps these hand-written for simplicity. M1+ will generate from proto.
"""

from __future__ import annotations

from enum import IntEnum
from typing import Optional

from pydantic import BaseModel, Field, field_validator


class Mode(IntEnum):
    """Mirrors proto enum aurora.ai.v1.Mode."""

    UNSPECIFIED = 0
    SOLO = 1
    ROOM = 2
    KOL_PERSONA = 3


class RecommendRequest(BaseModel):
    user_id: str = Field(..., min_length=1)
    mode: Mode = Mode.SOLO
    match_id: str = ""
    room_id: str = ""

    @field_validator("mode", mode="before")
    @classmethod
    def _coerce_mode(cls, v: object) -> object:
        """Accept enum names as well as the integer value.

        M0 callers send the integer (e.g. ``1``). M1's proto<->JSON clients
        serialize enums by name (e.g. ``"MODE_SOLO"``). Tolerate ``"SOLO"``,
        ``"MODE_SOLO"`` and numeric strings so both wire forms map to the
        right :class:`Mode`; anything else raises a clear validation error.
        """
        if isinstance(v, str):
            s = v.strip().upper()
            if s.isdigit():
                return int(s)
            name = s.removeprefix("MODE_")
            try:
                return Mode[name]
            except KeyError as exc:
                raise ValueError(f"unknown mode: {v!r}") from exc
        return v


class Recommendation(BaseModel):
    market_id: str
    selection: str
    suggested_odds: float
    ai_confidence: float = Field(..., ge=0.0, le=1.0)
    expected_value: float
    reasoning: str
    data_sources: list[str] = Field(default_factory=list)


class RecommendResponse(BaseModel):
    recommendations: list[Recommendation]
    correlation_id: str
    latency_ms: int


class HealthCheckResponse(BaseModel):
    status: str
    version: str
    details: dict[str, str] = Field(default_factory=dict)
