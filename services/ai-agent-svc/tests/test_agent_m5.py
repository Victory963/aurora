"""M5 pipeline unit tests — no network, no LLM (conftest strips API keys)."""

from __future__ import annotations

from aurora_ai import llm, pipeline, tools
from aurora_ai.agent import generate_recommendations
from aurora_ai.models import Mode

MATCH = "j1.urawa-vs-fctokyo"


def _recs(mode=Mode.SOLO, user="u-test-1", room=""):
    return generate_recommendations(user_id=user, mode=mode, match_id=MATCH, room_id=room)


def test_fallback_returns_valid_recs_with_tool_audit():
    recs = _recs()
    assert 1 <= len(recs) <= pipeline.MAX_RECS
    for r in recs:
        assert 0.0 < r["ai_confidence"] <= 1.0
        assert r["suggested_odds"] > 1.0
        assert r["reasoning"]
        sources = r["data_sources"]
        # tool-call audit trail must be present
        assert any(s.startswith("tool:player_stats:") for s in sources)
        assert any(s.startswith("tool:odds:") for s in sources)
        assert any(s.startswith("tool:rg_check:") for s in sources)
        # keyless env => template engine marker, never an llm marker
        assert "fallback:template" in sources
        assert not any(s.startswith("llm:") for s in sources)


def test_numbers_come_from_tools_and_ev_is_consistent():
    markets, _ = tools.get_current_odds(MATCH)
    by_key = {(m["market_id"], m["selection"]): m["odds"] for m in markets}
    for r in _recs():
        assert r["suggested_odds"] == by_key[(r["market_id"], r["selection"])]
        # expected_value is stored rounded to 3 decimals; allow the half-step.
        assert abs(r["expected_value"] - (r["ai_confidence"] * r["suggested_odds"] - 1.0)) <= 5.001e-4


def test_deterministic_for_same_match():
    assert _recs() == _recs()


def test_rg_blocked_user_gets_empty_recs():
    assert _recs(user="rgblock-user-9") == []


def test_room_mode_includes_room_sentiment_source():
    recs = _recs(mode=Mode.ROOM, room="room-42")
    assert recs
    assert any("room" in s for s in recs[0]["data_sources"])


def test_kol_mode_includes_persona_source():
    recs = _recs(mode=Mode.KOL_PERSONA)
    assert recs
    assert any(s.startswith("persona:") for s in recs[0]["data_sources"])


def test_quality_gate_drops_hallucinated_markets(monkeypatch):
    """LLM 'answers' with an invented market → gate rejects it → template path."""

    def fake_reason(_context):
        return [
            {"market_id": "made.up.market", "selection": "HOME", "ai_confidence": 0.9, "reasoning": "x"},
            {"market_id": f"{MATCH}.over-2.5", "selection": "OVER", "ai_confidence": 7.0, "reasoning": "bad conf"},
        ]

    monkeypatch.setattr(llm, "reason", fake_reason)
    recs = _recs()
    assert recs  # degraded to template, not empty
    assert all(r["market_id"] != "made.up.market" for r in recs)
    assert "fallback:template" in recs[0]["data_sources"]
    assert any(s.startswith("degrade:") for s in recs[0]["data_sources"])


def test_llm_picks_pass_gate_and_get_tool_odds(monkeypatch):
    """A valid LLM pick keeps its confidence but odds/EV come from the tool."""
    markets, _ = tools.get_current_odds(MATCH)
    target = markets[0]

    def fake_reason(_context):
        return [
            {
                "market_id": target["market_id"],
                "selection": target["selection"],
                "ai_confidence": 0.61,
                # LLM cannot inject odds: schema doesn't even carry them
                "reasoning": "pressure building",
            }
        ]

    monkeypatch.setattr(llm, "reason", fake_reason)
    recs = _recs()
    assert len(recs) == 1
    r = recs[0]
    assert r["suggested_odds"] == target["odds"]
    assert r["ai_confidence"] == 0.61
    assert any(s.startswith("llm:") for s in r["data_sources"])


def test_sequential_runner_matches_graph_semantics(monkeypatch):
    """Force the no-langgraph path; results must be identical to the graph."""
    graph_result = _recs()
    monkeypatch.setattr(pipeline, "_GRAPH_FAILED", True)
    assert _recs() == graph_result
