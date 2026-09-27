# Cross-compilation helpers (xx-apk, xx-go, xx-verify).
FROM --platform=$BUILDPLATFORM tonistiigi/xx:1.9.0 AS xx

# Build stage: runs natively on the build machine and cross-compiles for the
# target platform, so the arm64 image no longer compiles Go and SQLite's C code
# under QEMU emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.8-alpine AS builder

COPY --from=xx / /
RUN apk add --no-cache clang lld

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

# Target C library and compiler for cgo (go-sqlite3).
ARG TARGETPLATFORM
RUN xx-apk add --no-cache gcc musl-dev

COPY . .

# HTMX is vendored at static/htmx.min.js and embedded into the binary, so the
# build needs no network access. Run cmd/fetch_htmx.go manually to bump versions.
RUN CGO_ENABLED=1 xx-go build -ldflags="-s -w" -o secondbrain . \
    && xx-verify secondbrain

# Runtime stage
FROM alpine:3.24.1

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -s /sbin/nologin appuser

WORKDIR /app

COPY --from=builder /app/secondbrain .

RUN mkdir -p /app/data && chown appuser:appuser /app/data

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD sh -c "wget -qO /dev/null http://localhost:${PORT:-8080}/health || exit 1"

ENV PORT=8080
ENV DATA_DIR=/app/data

VOLUME ["/app/data"]

ENTRYPOINT ["./secondbrain"]
