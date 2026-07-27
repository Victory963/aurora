"""M5 tools — the ONLY source of numbers the LLM may use (ADR-0001: no invented
odds/stats). Every call returns (data, audit_tag); audit tags end up in each
recommendation's data_sources.

MOCK PROVIDERS: deterministic pseudo-data seeded from sha256(match_id/user_id),
stable across calls so tests and the smoke can assert on them. Real providers
(Sportradar/Pragmatic feeds → M9, compliance-svc → M12) swap in behind the same
function signatures.
"""

from __future__ import annotations

import hashlib
from typing import Any

RG_PASSED = "PASSED"
RG_WARNING = "WARNING"
RG_BLOCKED = "BLOCKED"


def _seed(*parts: str) -> int:
    h = hashlib.sha256("|".join(parts).encode("utf-8")).digest()
    return int.from_bytes(h[:8], "big")


def _unit(seed: int, salt: int) -> float:
    """Deterministic float in [0, 1) derived from seed+salt."""
    return ((seed >> (salt % 48)) ^ (seed * (salt + 1))) % 10_000 / 10_000.0


def get_player_stats(match_id: str) -> tuple[dict[str, Any], str]:
    """Mock Sportradar-style live match stats."""
    s = _seed("stats", match_id)
    stats = {
        "match_id": match_id,
        "shots_home": 6 + int(_unit(s, 1) * 12),
        "shots_away": 2 + int(_unit(s, 2) * 8),
        "possession_home": round(0.40 + _unit(s, 3) * 0.30, 2),
        "xg_home": round(0.6 + _unit(s, 4) * 1.8, 2),
        "xg_away": round(0.2 + _unit(s, 5) * 1.2, 2),
        # momentum > 0.5 means the home side is pressing
        "momentum_home": round(0.30 + _unit(s, 6) * 0.55, 2),
    }
    return stats, f"tool:player_stats:{match_id}"


def get_current_odds(match_id: str) -> tuple[list[dict[str, Any]], str]:
    """Mock bookmaker odds for a small set of in-play markets."""
    s = _seed("odds", match_id)

    def odds(salt: int, lo: float, hi: float) -> float:
        return round(lo + _unit(s, salt) * (hi - lo), 2)

    markets = [
        {"market_id": f"{match_id}.next-goal-15m", "selection": "HOME", "odds": odds(1, 1.7, 3.2)},
        {"market_id": f"{match_id}.next-goal-15m", "selection": "AWAY", "odds": odds(2, 2.8, 5.5)},
        {"market_id": f"{match_id}.over-2.5", "selection": "OVER", "odds": odds(3, 1.5, 2.4)},
        {"market_id": f"{match_id}.home-minus-1", "selection": "HOME_-1", "odds": odds(4, 1.8, 2.6)},
    ]
    return markets, f"tool:odds:{match_id}"


def get_alt_odds(match_id: str) -> tuple[dict[str, Any], str]:
    """Mock internal pool-implied odds (our own rooms' aggregate view)."""
    markets, _ = get_current_odds(match_id)
    implied = {m["market_id"] + ":" + m["selection"]: round(m["odds"] * 0.93, 2) for m in markets}
    return {"pool_implied": implied, "rooms_sampled": 1247}, f"tool:alt_odds:{match_id}"


def rg_check(user_id: str) -> tuple[dict[str, Any], str]:
    """Mock responsible-gambling gate (real: compliance-svc, M12).

    Deterministic hook for tests/smoke: a user_id containing 'rgblock' is
    BLOCKED; 'rgwarn' is WARNING; everyone else PASSED with a stable low score.
    """
    if "rgblock" in user_id:
        status, score = RG_BLOCKED, 0.97
    elif "rgwarn" in user_id:
        status, score = RG_WARNING, 0.62
    else:
        status, score = RG_PASSED, round(0.05 + _unit(_seed("rg", user_id), 1) * 0.30, 2)
    return {"status": status, "score": score}, f"tool:rg_check:{status}"


def get_room_sentiment(room_id: str) -> tuple[dict[str, Any], str]:
    """Mock room-chat sentiment signal (real NLP over Scylla history: later)."""
    s = _seed("sentiment", room_id)
    label = ["excited", "nervous", "confident"][int(_unit(s, 1) * 3) % 3]
    return {"room_id": room_id, "sentiment": label, "intensity": round(0.4 + _unit(s, 2) * 0.5, 2)}, (
        f"room:sentiment:{room_id or 'none'}"
    )


def model_probability(stats: dict[str, Any], selection: str) -> float:
    """Tiny transparent 'model': maps momentum/xG to a probability. This is the
    number the FALLBACK path uses for EV; the LLM path reasons over the same
    stats. Bounded away from 0/1 so EV stays sane."""
    base = 0.30 + 0.35 * float(stats.get("momentum_home", 0.5))
    if selection in ("AWAY",):
        base = 1.0 - base
    if selection in ("OVER",):
        base = 0.35 + 0.25 * min(1.0, (stats.get("xg_home", 1.0) + stats.get("xg_away", 0.6)) / 3.0)
    if selection in ("HOME_-1",):
        base = base * 0.62
    return round(min(0.90, max(0.08, base)), 3)
