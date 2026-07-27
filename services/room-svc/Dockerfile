# --------------------------------------------------------------------------
# room-svc Dockerfile (M4) — multi-stage; see identity-svc for the pattern.
# gocql + nats.go are pure Go, so CGO can stay disabled.
# --------------------------------------------------------------------------

FROM golang:1.25-alpine AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

COPY services/room-svc/go.mod services/room-svc/go.sum* ./services/room-svc/
WORKDIR /workspace/services/room-svc
RUN go mod download

# Full source, then resolve the complete module graph (adds indirect requires +
# go.sum) so the readonly build works even before go.sum is committed
# (run `make tidy-room` locally to commit it for reproducible builds).
COPY services/room-svc/ ./
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/room-svc ./cmd

# ---- runtime ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/room-svc /app/room-svc
EXPOSE 8084
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=6 \
  CMD wget --quiet --tries=1 --spider http://localhost:8084/healthz || exit 1
ENTRYPOINT ["/app/room-svc"]
