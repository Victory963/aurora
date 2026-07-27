# --------------------------------------------------------------------------
# wallet-svc Dockerfile (M3) — multi-stage; see identity-svc for the pattern.
# --------------------------------------------------------------------------

FROM golang:1.25-alpine AS builder
ENV GOTOOLCHAIN=auto

WORKDIR /workspace

COPY services/wallet-svc/go.mod services/wallet-svc/go.sum* ./services/wallet-svc/
WORKDIR /workspace/services/wallet-svc
RUN go mod download

# Full source, then resolve the complete module graph (adds indirect requires +
# go.sum) so the readonly build below works even before go.sum is committed
# (run `make tidy-wallet` locally to commit it for reproducible builds).
COPY services/wallet-svc/ ./
RUN go mod tidy

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/wallet-svc ./cmd

# ---- runtime ----
FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/wallet-svc /app/wallet-svc
EXPOSE 8083
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD wget --quiet --tries=1 --spider http://localhost:8083/healthz || exit 1
ENTRYPOINT ["/app/wallet-svc"]
