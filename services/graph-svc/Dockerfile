# --------------------------------------------------------------------------
# graph-svc Dockerfile (M8) — multi-stage; same pattern as the other Go services.
# --------------------------------------------------------------------------

FROM golang:1.25-alpine AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

COPY services/graph-svc/go.mod services/graph-svc/go.sum* ./services/graph-svc/
WORKDIR /workspace/services/graph-svc
RUN go mod download || true

# Full source, then resolve the complete module graph (adds indirect requires +
# go.sum) so the build works even before go.sum is committed
# (run `make tidy-graph` locally to commit it for reproducible builds).
COPY services/graph-svc/ ./
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/graph-svc ./cmd

# ---- runtime ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/graph-svc /app/graph-svc
EXPOSE 8087
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=12 \
  CMD wget --quiet --tries=1 --spider http://localhost:8087/healthz || exit 1
ENTRYPOINT ["/app/graph-svc"]
