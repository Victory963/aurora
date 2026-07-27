"""
Unit tests for ai-agent-svc.

These run without a live identity-svc by using FastAPI TestClient.
Integration tests (which require docker compose up) live in scripts/.
"""

from __future__ import annotations

import pytest
from fastapi.testclient import TestClient

from aurora_ai import server as server_module
from aurora_ai.agent import generate_recommendations
from aurora_ai.models import Mode


@pytest.fixture
def client(monkeypatch):
    # Patch identity verification to always succeed for these unit tests
    async def _fake_verify(_http, user_id):
        return {"ok": True}

    monkeypatch.setattr(server_module, "_verify_user", _fake_verify)
    with TestClient(server_module.app) as c:
        yield c


def test_health_endpoint(client):
    r = client.get("/healthz")
    assert r.status_code == 200
    data = r.json()
    assert data["status"] == "ok"
    assert "version" in data


def test_recommend_solo_returns_recommendations(client):
    r = client.post(
        "/aurora.ai.v1.AIAgentService/Recommend",
        json={
            "user_id": "test-user-1",
            "mode": Mode.SOLO.value,
            "match_id": "j1.urawa-vs-fctokyo",
            "room_id": "",
        },
    )
    assert r.status_code == 200, r.text
    data = r.json()
    assert "recommendations" in data
    assert len(data["recommendations"]) > 0
    rec = data["recommendations"][0]
    assert "market_id" in rec
    assert "ai_confidence" in rec
    assert 0.0 <= rec["ai_confidence"] <= 1.0
    assert "correlation_id" in data


def test_recommend_room_uses_different_data_sources(client):
    r = client.post(
        "/aurora.ai.v1.AIAgentService/Recommend",
        json={
            "user_id": "test-user-1",
            "mode": Mode.ROOM.value,
            "match_id": "j1.urawa-vs-fctokyo",
            "room_id": "room-saitama-stand",
        },
    )
    assert r.status_code == 200
    data = r.json()
    assert len(data["recommendations"]) > 0
    # Room mode should include simulated room signal
    sources = data["recommendations"][0]["data_sources"]
    assert any("room" in s for s in sources)


def test_generate_recommendations_function_directly():
    """Unit test the pure function without HTTP."""
    recs = generate_recommendations(
        user_id="u1",
        mode=Mode.SOLO,
        match_id="m1",
        room_id="",
    )
    assert len(recs) >= 1
    assert all("market_id" in r for r in recs)


def test_unspecified_mode_defaults_to_solo():
    recs = generate_recommendations("u1", Mode.UNSPECIFIED, "m1", "")
    # Should not be empty (defaults to SOLO scenario)
    assert len(recs) >= 1


def test_recommend_degraded_when_user_missing(monkeypatch):
    """When identity-svc says the user does not exist, Recommend must still
    return a well-formed 200 with an empty list (documented M0 demo behavior)."""

    async def _fake_verify(_http, user_id):
        return {"ok": False, "reason": "user_not_found"}

    monkeypatch.setattr(server_module, "_verify_user", _fake_verify)
    with TestClient(server_module.app) as c:
        r = c.post(
            "/aurora.ai.v1.AIAgentService/Recommend",
            json={"user_id": "ghost", "mode": 1, "match_id": "m", "room_id": ""},
        )
    assert r.status_code == 200, r.text
    data = r.json()
    assert data["recommendations"] == []
    assert data["correlation_id"]
    assert "latency_ms" in data


def test_mode_accepts_proto_style_name(client):
    """M1 proto<->JSON clients serialize the enum by name (MODE_ROOM)."""
    r = client.post(
        "/aurora.ai.v1.AIAgentService/Recommend",
        json={"user_id": "u", "mode": "MODE_ROOM", "match_id": "m", "room_id": "r"},
    )
    assert r.status_code == 200, r.text
    sources = r.json()["recommendations"][0]["data_sources"]
    assert any("room" in s for s in sources)


def test_mode_accepts_plain_name(client):
    r = client.post(
        "/aurora.ai.v1.AIAgentService/Recommend",
        json={"user_id": "u", "mode": "SOLO", "match_id": "m", "room_id": ""},
    )
    assert r.status_code == 200, r.text


def test_mode_rejects_garbage(client):
    r = client.post(
        "/aurora.ai.v1.AIAgentService/Recommend",
        json={"user_id": "u", "mode": "NONSENSE", "match_id": "m", "room_id": ""},
    )
    assert r.status_code == 422, r.text


async def test_verify_user_maps_status_codes():
    """Directly exercise the cross-service mapping the happy-path fixture stubs out."""
    import httpx

    from aurora_ai.server import _verify_user

    def handler(request: httpx.Request) -> httpx.Response:
        body = request.content.decode()
        if "good" in body:
            return httpx.Response(200, json={"user": {"id": "good"}})
        if "ghost" in body:
            return httpx.Response(404, json={"code": "not_found"})
        return httpx.Response(500, json={"code": "internal"})

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport) as ac:
        ok = await _verify_user(ac, "good")
        missing = await _verify_user(ac, "ghost")
        boom = await _verify_user(ac, "other")

    assert ok["ok"] is True
    assert missing == {"ok": False, "reason": "user_not_found"}
    assert boom["ok"] is False
    assert boom["reason"].startswith("http_")
