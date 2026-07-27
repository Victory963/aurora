# --------------------------------------------------------------------------
# bet-svc Dockerfile (M6) — multi-stage; same pattern as the other Go services.
# --------------------------------------------------------------------------

FROM golang:1.25-alpine AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

COPY services/bet-svc/go.mod services/bet-svc/go.sum* ./services/bet-svc/
WORKDIR /workspace/services/bet-svc
RUN go mod download

# Full source, then resolve the complete module graph (adds indirect requires +
# go.sum) so the readonly build works even before go.sum is committed
# (run `make tidy-bet` locally to commit it for reproducible builds).
COPY services/bet-svc/ ./
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/bet-svc ./cmd

# ---- runtime ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/bet-svc /app/bet-svc
EXPOSE 8086
HEALTHCHECK --interval=10s --timeout=3s --start-period=8s --retries=6 \
  CMD wget --quiet --tries=1 --spider http://localhost:8086/healthz || exit 1
ENTRYPOINT ["/app/bet-svc"]
