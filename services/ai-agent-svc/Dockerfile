# --------------------------------------------------------------------------
# ai-agent-svc Dockerfile (M0)
#
# Python 3.11 slim base, layered for fast rebuild during development.
# --------------------------------------------------------------------------

FROM python:3.11-slim

WORKDIR /app

# System deps (minimal: curl for healthcheck only)
RUN apt-get update && apt-get install -y --no-install-recommends \
        curl \
    && rm -rf /var/lib/apt/lists/*

# Copy project metadata + source, then install from pyproject.toml so the image
# uses the single source of truth for dependencies (no drift with hand-pinned
# copies that the previous inline `pip install` list would inevitably accrue).
COPY services/ai-agent-svc/pyproject.toml ./
COPY services/ai-agent-svc/aurora_ai/ ./aurora_ai/
RUN pip install --no-cache-dir .

EXPOSE 8082

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD curl -fsS http://localhost:8082/healthz || exit 1

# uvicorn with reasonable defaults for M0 (single worker;
# scale via replicas in K8s for M1+)
CMD ["uvicorn", "aurora_ai.server:app", \
     "--host", "0.0.0.0", \
     "--port", "8082", \
     "--log-level", "info"]
