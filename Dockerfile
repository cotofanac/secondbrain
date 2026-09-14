# Build stage
FROM golang:1.26.8-alpine AS builder

RUN apk add --no-cache gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# HTMX is vendored at static/htmx.min.js and embedded into the binary, so the
# build needs no network access. Run cmd/fetch_htmx.go manually to bump versions.
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o secondbrain .

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
