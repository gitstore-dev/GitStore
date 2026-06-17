# syntax=docker/dockerfile:1.7

# Multi-stage build for Controller Manager (Go)
FROM golang:1.25.4-alpine3.21 AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go modules manifests
COPY gitstore-controller-manager/go.mod gitstore-controller-manager/go.sum ./

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY gitstore-controller-manager/ ./

# Build application
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -o controller-manager ./cmd/controller

# Runtime stage
FROM alpine:3.23.3

RUN apk --no-cache add ca-certificates

WORKDIR /app

COPY --from=builder /build/controller-manager /app/controller-manager

EXPOSE 5001

ENV GITSTORE_CONTROLLER__PORT=5001
ENV GITSTORE_CONTROLLER__API_URI=http://api:4000/graphql
ENV GITSTORE_CONTROLLER__DEFAULT_MAX_ATTEMPTS=5
ENV GITSTORE_CONTROLLER__DEFAULT_STALL_THRESHOLD=5m
ENV GITSTORE_LOG__LEVEL=info
ENV GITSTORE_LOG__FORMAT=json

CMD ["/app/controller-manager"]
