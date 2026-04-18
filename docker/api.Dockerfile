# Multi-stage build for GraphQL API (Go)
FROM golang:1.26.1-alpine3.23 AS builder

RUN apk add --no-cache git

WORKDIR /build

# Copy go modules manifests
COPY api/go.mod api/go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY api/ ./
COPY shared/schemas /build/shared/schemas

# Build application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o api ./cmd/server

# Runtime stage
FROM alpine:3.23.3

LABEL org.opencontainers.image.description="GitStore GraphQL API service for querying and publishing catalog data backed by the git service"

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
