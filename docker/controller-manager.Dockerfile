# syntax=docker/dockerfile:1.7

# Multi-stage build for Controller Manager (Go)
FROM golang:1.26.1-alpine3.23 AS builder

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

CMD ["/app/controller-manager"]
