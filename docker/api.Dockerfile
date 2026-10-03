# syntax=docker/dockerfile:1.7

# Multi-stage build for GraphQL API (Go)
FROM golang:1.26.1-alpine3.23 AS builder

RUN apk add --no-cache git

WORKDIR /build/gitstore-api
ENV GOWORK=off

# Copy go modules manifests
COPY gitstore-api/go.mod gitstore-api/go.sum ./
COPY shared/secretmaterial/go.mod /build/shared/secretmaterial/go.mod

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY gitstore-api/ ./
COPY shared/secretmaterial/ /build/shared/secretmaterial/
COPY shared/schemas /build/shared/schemas

# Build application
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /build/api ./cmd/server
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o /build/gitctl ./cmd/gitctl

# Runtime stage
FROM alpine:3.23.3

ARG GIT_REVISION=unknown
LABEL org.opencontainers.image.revision=${GIT_REVISION}

RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy binary and schemas
COPY --from=builder /build/api /app/api
COPY --from=builder /build/gitctl /app/gitctl
COPY --from=builder /build/shared/schemas /app/schemas

# Expose ports
EXPOSE 4000
EXPOSE 9000
EXPOSE 6000

CMD ["/app/api"]
