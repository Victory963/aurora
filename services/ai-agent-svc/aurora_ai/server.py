"""
aurora_ai.server — FastAPI app for ai-agent-svc.

M0 scope:
  - POST /aurora.ai.v1.AIAgentService/Recommend → hard-coded recommendations
  - POST/GET /aurora.ai.v1.AIAgentService/HealthCheck → service status
  - URL paths follow Connect-RPC convention so M1+ can swap in typed clients
    without changing routes.

M5 will replace the hard-coded recommend() with a LangGraph state machine
that calls real tools (player stats, odds, RG check) and a real LLM.

This file is intentionally small: complexity belongs in agent.py once we add
the state machine, leaving server.py as a thin transport layer.
"""

from __future__ import annotations

import logging
import os
import time
import uuid
from contextlib import asynccontextmanager
from typing import Any

import httpx
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse

from . import agent
from .models import (
    HealthCheckResponse,
    Recommendation,
    RecommendRequest,
    RecommendResponse,
)

VERSION = "0.2.0-m5"
IDENTITY_SVC_URL = os.getenv(
    "AURORA_AI_IDENTITY_URL",
    "http://localhost:8081",
)

logger = logging.getLogger("aurora_ai")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")


# ---------- Lifespan: shared HTTP client ----------
@asynccontextmanager
async def lifespan(app: FastAPI):
    app.state.http = httpx.AsyncClient(timeout=5.0)
    logger.info("ai-agent-svc starting version=%s identity_url=%s", VERSION, IDENTITY_SVC_URL)
    yield
    await app.state.http.aclose()
    logger.info("ai-agent-svc shut down cleanly")


app = FastAPI(title="aurora-ai-agent-svc", version=VERSION, lifespan=lifespan)


# ---------- Middleware: request log ----------
@app.middleware("http")
async def log_requests(request: Request, call_next):
    start = time.time()
    response = await call_next(request)
    duration_ms = int((time.time() - start) * 1000)
    logger.info(
        "http method=%s path=%s status=%d duration_ms=%d",
        request.method,
        request.url.path,
        response.status_code,
        duration_ms,
    )
    return response


# ---------- Endpoints ----------
@app.post("/aurora.ai.v1.AIAgentService/Recommend")
async def recommend(req: RecommendRequest, request: Request) -> RecommendResponse:
    """
    Generate recommendations for a user. M0: hard-coded; M5: LangGraph.

    Even in M0 we still verify the user exists via identity-svc to prove
    that the cross-service plumbing works.
    """
    started = time.time()
    correlation_id = str(uuid.uuid4())

    # Verify user exists by calling identity-svc.
    # This is the core "end-to-end" proof for M0.
    http: httpx.AsyncClient = request.app.state.http
    user_check = await _verify_user(http, req.user_id)
    if not user_check["ok"]:
        # Still return a degraded response rather than 4xx, so M0 demo
        # surfaces the failure mode clearly.
        return RecommendResponse(
            recommendations=[],
            correlation_id=correlation_id,
            latency_ms=int((time.time() - started) * 1000),
        )

    recs = agent.generate_recommendations(
        user_id=req.user_id,
        mode=req.mode,
        match_id=req.match_id,
        room_id=req.room_id,
    )

    return RecommendResponse(
        recommendations=[Recommendation(**r) for r in recs],
        correlation_id=correlation_id,
        latency_ms=int((time.time() - started) * 1000),
    )


@app.get("/aurora.ai.v1.AIAgentService/HealthCheck")
@app.post("/aurora.ai.v1.AIAgentService/HealthCheck")
async def health_check(request: Request) -> HealthCheckResponse:
    details: dict[str, str] = {}

    # Probe identity-svc
    http: httpx.AsyncClient = request.app.state.http
    try:
        r = await http.get(f"{IDENTITY_SVC_URL}/healthz")
        details["identity_svc"] = "ok" if r.status_code == 200 else f"http_{r.status_code}"
    except Exception as e:
        details["identity_svc"] = f"unreachable: {type(e).__name__}"

    overall = "ok" if details.get("identity_svc") == "ok" else "degraded"

    return HealthCheckResponse(status=overall, version=VERSION, details=details)


@app.get("/healthz")
async def healthz() -> dict[str, Any]:
    return {"status": "ok", "version": VERSION}


# ---------- Helpers ----------
async def _verify_user(http: httpx.AsyncClient, user_id: str) -> dict[str, Any]:
    """Call identity-svc GetUser. Returns {ok: bool, reason: str}."""
    try:
        r = await http.post(
            f"{IDENTITY_SVC_URL}/aurora.identity.v1.IdentityService/GetUser",
            json={"id": user_id},
        )
        if r.status_code == 200:
            return {"ok": True}
        if r.status_code == 404:
            return {"ok": False, "reason": "user_not_found"}
        return {"ok": False, "reason": f"http_{r.status_code}"}
    except httpx.HTTPError as e:
        logger.warning("identity-svc unreachable: %s", e)
        return {"ok": False, "reason": "identity_unreachable"}


# ---------- Error handler (so we always return JSON) ----------
@app.exception_handler(Exception)
async def handle_exception(_: Request, exc: Exception) -> JSONResponse:
    logger.exception("unhandled error: %s", exc)
    # Keep the real cause in logs only (above); return a generic message so we
    # don't leak internal details to unauthenticated callers (mirrors the Go side).
    return JSONResponse(
        status_code=500,
        content={"code": "internal", "message": "internal error"},
    )
