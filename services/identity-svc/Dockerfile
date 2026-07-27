# --------------------------------------------------------------------------
# identity-svc Dockerfile (M0)
#
# Multi-stage build:
#   stage 1 (builder):  golang:1.22-alpine compiles the binary
#   stage 2 (runtime):  alpine:3.19 with only the binary + ca-certs
#
# Resulting image: ~15 MB
#
# Build:    docker build -t aurora/identity-svc:m0 -f services/identity-svc/Dockerfile .
# Run:      docker run --rm -p 8081:8081 \
#             -e AURORA_IDENTITY_DB_HOST=host.docker.internal \
#             aurora/identity-svc:m0
# --------------------------------------------------------------------------

FROM golang:1.25-alpine AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

# Copy go.mod / go.sum first for better layer caching; warm the module cache.
COPY services/identity-svc/go.mod services/identity-svc/go.sum* ./services/identity-svc/

WORKDIR /workspace/services/identity-svc
RUN go mod download

# Now copy the rest of the source, then resolve the complete module graph. tidy
# adds the indirect requires that kafka-go/pgx pull in and writes go.sum, so the
# readonly `go build` below succeeds even when go.sum / indirect-requires have not
# been committed yet (run `make tidy` locally to commit them for reproducibility).
COPY services/identity-svc/ ./
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/identity-svc ./cmd

# ---- runtime ----
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app
COPY --from=builder /out/identity-svc /app/identity-svc

EXPOSE 8081

# Default healthcheck (Docker level). docker-compose can override.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD wget --quiet --tries=1 --spider http://localhost:8081/healthz || exit 1

ENTRYPOINT ["/app/identity-svc"]
