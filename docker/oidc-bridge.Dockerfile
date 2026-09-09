# syntax=docker/dockerfile:1.7

# Multi-stage build for gitstore-oidc-bridge (Go)
FROM golang:1.26.1-alpine3.23 AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go modules manifests
COPY gitstore-oidc-bridge/go.mod gitstore-oidc-bridge/go.sum ./

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY gitstore-oidc-bridge/ ./

# Build application
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o bridge ./cmd/bridge

# Runtime stage
FROM alpine:3.23.3

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /build/bridge /app/bridge

# Bridge listen port (/login, /consent, /healthz)
EXPOSE 4445

CMD ["/app/bridge"]
