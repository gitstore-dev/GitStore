# syntax=docker/dockerfile:1.7

# Multi-stage build for GraphQL API (Go)
FROM golang:1.26.1-alpine3.23 AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go modules manifests
COPY api/go.mod api/go.sum ./

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY api/ ./
COPY shared/schemas /build/shared/schemas

# Build application
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o api ./cmd/server

# Runtime stage
FROM alpine:3.23.3

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary and schemas
COPY --from=builder /build/api /app/api
COPY --from=builder /build/shared/schemas /app/schemas

# Expose GraphQL API port
EXPOSE 4000

ENV GITSTORE_API_PORT=4000
ENV GITSTORE_GIT_WS=ws://git-service:8080
ENV GITSTORE_CACHE_TTL=300
ENV GITSTORE_LOG_LEVEL=info

CMD ["/app/api"]
