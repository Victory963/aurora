"""M5 recommendation pipeline — LangGraph state machine (ADR-0001) with an
identical-semantics sequential fallback runner.

Degrade ladder (each level keeps the service returning 200):
  1. LangGraph graph + LLM            (full path)
  2. sequential runner + LLM          (langgraph unavailable/broken)
  3. either runner + template recs    (no API key / LLM error / quality gate)
  4. RG BLOCKED short-circuit         (empty recommendations, by design)

All numbers come from tools.py; the LLM only picks markets and writes the
reasoning. rank_and_quality re-attaches tool odds and recomputes EV, so model
output can never introduce invented odds.
"""

from __future__ import annotations

import logging
import uuid
from typing import Any, TypedDict

from . import config, llm, personas, tools
from .models import Mode

logger = logging.getLogger(__name__)

MAX_RECS = 3


class PState(TypedDict, total=False):
    user_id: str
    mode: int
    match_id: str
    room_id: str
    audit: list[str]
    rg: dict[str, Any]
    blocked: bool
    stats: dict[str, Any]
    odds: list[dict[str, Any]]
    alt: dict[str, Any]
    sentiment: dict[str, Any]
    persona: dict[str, Any]
    picks: list[dict[str, Any]]
    degrade: str
    recommendations: list[dict[str, Any]]


# ---------------------------------------------------------------- nodes ----

def ingest_context(state: PState) -> PState:
    match_id = state.get("match_id") or "j1.demo-match"
    return {"match_id": match_id, "audit": [], "degrade": ""}


def rg_check(state: PState) -> PState:
    rg, tag = tools.rg_check(state["user_id"])
    return {"rg": rg, "blocked": rg["status"] == tools.RG_BLOCKED, "audit": state["audit"] + [tag]}


def finalize_blocked(state: PState) -> PState:
    logger.info("rg blocked user_id=%s score=%s", state["user_id"], state["rg"].get("score"))
    return {"recommendations": [], "degrade": "rg_blocked"}


def retrieve(state: PState) -> PState:
    stats, t1 = tools.get_player_stats(state["match_id"])
    odds, t2 = tools.get_current_odds(state["match_id"])
    alt, t3 = tools.get_alt_odds(state["match_id"])
    return {"stats": stats, "odds": odds, "alt": alt, "audit": state["audit"] + [t1, t2, t3]}


def mode_context(state: PState) -> PState:
    update: PState = {}
    audit = list(state["audit"])
    if state["mode"] == int(Mode.ROOM):
        sentiment, tag = tools.get_room_sentiment(state.get("room_id", ""))
        update["sentiment"] = sentiment
        audit.append(tag)
    elif state["mode"] == int(Mode.KOL_PERSONA):
        persona, tag = personas.get_persona()
        update["persona"] = persona
        audit.append(tag)
    update["audit"] = audit
    return update


def llm_reason(state: PState) -> PState:
    context = {
        "match_stats": state["stats"],
        "markets": state["odds"],
        "internal_pool_implied": state["alt"],
        "rg": {"status": state["rg"]["status"]},
    }
    if state.get("sentiment"):
        context["room_sentiment"] = state["sentiment"]
    if state.get("persona"):
        context["persona_card"] = state["persona"]
    try:
        return {"picks": llm.reason(context)}
    except llm.LLMError as exc:
        logger.warning("llm degrade: %s", exc)
        return {"picks": [], "degrade": f"llm:{exc}"}
    except Exception as exc:  # defensive: ANY llm-layer surprise degrades, never 500s
        logger.warning("llm degrade (unexpected %s): %s", type(exc).__name__, exc)
        return {"picks": [], "degrade": f"llm_unexpected:{type(exc).__name__}"}


def _template_picks(state: PState) -> list[dict[str, Any]]:
    """Deterministic fallback built from the SAME tool data (never empty)."""
    scored = []
    for m in state["odds"]:
        prob = tools.model_probability(state["stats"], m["selection"])
        scored.append((prob * m["odds"] - 1.0, prob, m))
    scored.sort(key=lambda x: x[0], reverse=True)
    picks = []
    stats = state["stats"]
    for ev, prob, m in scored[:2]:
        why = (
            f"Template model: momentum_home={stats['momentum_home']}, "
            f"xG {stats['xg_home']}-{stats['xg_away']}, shots {stats['shots_home']}-"
            f"{stats['shots_away']} → p={prob} at odds {m['odds']} (EV {ev:+.3f})."
        )
        picks.append(
            {"market_id": m["market_id"], "selection": m["selection"], "ai_confidence": prob, "reasoning": why}
        )
    return picks


def rank_and_quality(state: PState) -> PState:
    """Quality gate: keep only picks that reference a real tool market; attach
    the TOOL odds; recompute EV; dedupe; cap at MAX_RECS. Falls back to the
    template picks when the LLM produced nothing usable."""
    by_key = {(m["market_id"], m["selection"]): m for m in state["odds"]}
    engine = f"llm:{config.model()}"
    degrade = state.get("degrade", "")

    picks = state.get("picks") or []
    valid: list[dict[str, Any]] = []
    seen: set[tuple[str, str]] = set()
    for p in picks:
        key = (str(p.get("market_id")), str(p.get("selection")))
        market = by_key.get(key)
        conf = p.get("ai_confidence")
        if market is None or key in seen or not isinstance(conf, (int, float)) or not (0 < conf <= 1):
            continue
        seen.add(key)
        valid.append(
            {
                "market_id": market["market_id"],
                "selection": market["selection"],
                "suggested_odds": market["odds"],
                "ai_confidence": round(float(conf), 3),
                "expected_value": round(float(conf) * market["odds"] - 1.0, 3),
                "reasoning": str(p.get("reasoning", ""))[:600],
            }
        )

    if not valid:
        if picks:  # LLM answered but nothing survived the gate
            degrade = degrade or "quality_gate"
        engine = "fallback:template"
        for p in _template_picks(state):
            market = by_key[(p["market_id"], p["selection"])]
            valid.append(
                {
                    "market_id": p["market_id"],
                    "selection": p["selection"],
                    "suggested_odds": market["odds"],
                    "ai_confidence": p["ai_confidence"],
                    "expected_value": round(p["ai_confidence"] * market["odds"] - 1.0, 3),
                    "reasoning": p["reasoning"],
                }
            )

    valid.sort(key=lambda r: r["expected_value"], reverse=True)
    return {
        "recommendations": valid[:MAX_RECS],
        "degrade": degrade,
        "audit": state["audit"] + [engine],
    }


def audit_log(state: PState) -> PState:
    """Attach the tool-call audit trail to every recommendation and log it."""
    sources = list(state["audit"])
    if state.get("degrade"):
        sources.append(f"degrade:{state['degrade'].split(':', 1)[0]}")
    recs = [dict(r, data_sources=sources) for r in state["recommendations"]]
    logger.info(
        "pipeline done user=%s mode=%s recs=%d degrade=%r sources=%s",
        state["user_id"], state["mode"], len(recs), state.get("degrade", ""), sources,
    )
    return {"recommendations": recs}


_NODE_ORDER = [ingest_context, rg_check, retrieve, mode_context, llm_reason, rank_and_quality, audit_log]


# ------------------------------------------------------------- runners ----

_GRAPH = None
_GRAPH_FAILED = False


_REDIS_CM = None  # keeps the RedisSaver context manager alive (GC would close its client)


def _checkpointer():
    """Redis checkpointer when configured, else in-memory. Never raises."""
    global _REDIS_CM
    url = config.redis_url()
    if url:
        try:
            from langgraph.checkpoint.redis import RedisSaver

            cm = RedisSaver.from_conn_string(url)
            if hasattr(cm, "__enter__"):
                saver = cm.__enter__()
                _REDIS_CM = cm
            else:
                saver = cm
            if hasattr(saver, "setup"):
                saver.setup()
            logger.info("checkpointer: redis (%s)", url)
            return saver
        except Exception as exc:
            logger.warning("redis checkpointer unavailable (%s); using memory", exc)
    from langgraph.checkpoint.memory import MemorySaver

    return MemorySaver()


def _build_graph():
    from langgraph.graph import END, START, StateGraph

    g = StateGraph(PState)
    g.add_node("ingest_context", ingest_context)
    g.add_node("rg_check", rg_check)
    g.add_node("finalize_blocked", finalize_blocked)
    g.add_node("retrieve", retrieve)
    g.add_node("mode_context", mode_context)
    g.add_node("llm_reason", llm_reason)
    g.add_node("rank_and_quality", rank_and_quality)
    g.add_node("audit_log", audit_log)

    g.add_edge(START, "ingest_context")
    g.add_edge("ingest_context", "rg_check")
    g.add_conditional_edges(
        "rg_check",
        lambda s: "blocked" if s.get("blocked") else "ok",
        {"blocked": "finalize_blocked", "ok": "retrieve"},
    )
    g.add_edge("finalize_blocked", END)
    g.add_edge("retrieve", "mode_context")
    g.add_edge("mode_context", "llm_reason")
    g.add_edge("llm_reason", "rank_and_quality")
    g.add_edge("rank_and_quality", "audit_log")
    g.add_edge("audit_log", END)
    return g.compile(checkpointer=_checkpointer())


def _run_sequential(state: PState) -> PState:
    """Same nodes, plain Python — used when LangGraph itself is unavailable."""
    for node in _NODE_ORDER:
        state = {**state, **node(state)}
        if node is rg_check and state.get("blocked"):
            return {**state, **finalize_blocked(state)}
    return state


def run(user_id: str, mode: int, match_id: str, room_id: str) -> PState:
    global _GRAPH, _GRAPH_FAILED
    initial: PState = {"user_id": user_id, "mode": mode, "match_id": match_id, "room_id": room_id}

    if not _GRAPH_FAILED:
        try:
            if _GRAPH is None:
                _GRAPH = _build_graph()
            cfg = {"configurable": {"thread_id": str(uuid.uuid4())}}
            return _GRAPH.invoke(initial, config=cfg)
        except Exception as exc:
            _GRAPH_FAILED = True
            logger.warning("langgraph unavailable (%s: %s); sequential runner", type(exc).__name__, exc)

    try:
        return _run_sequential(initial)
    except Exception as exc:  # last-resort: never let a node exception escape to the transport
        logger.error("sequential runner failed (%s: %s)", type(exc).__name__, exc)
        return {**initial, "recommendations": [], "degrade": "pipeline_error"}
