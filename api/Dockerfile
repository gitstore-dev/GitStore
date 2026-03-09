# Go API Dockerfile

# Build stage
FROM golang:1.21-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /app/api-server ./cmd/server

# Runtime stage
FROM alpine:latest

WORKDIR /app

# Install runtime dependencies
RUN apk --no-cache add ca-certificates git

# Copy binary from builder
COPY --from=builder /app/api-server /usr/local/bin/api-server

# Create cache directory
RUN mkdir -p /data/cache && \
    chmod 755 /data/cache

# Expose port
EXPOSE 8000

# Set environment variables
ENV PORT=8000
ENV GIT_REPO_PATH=/data/repos
ENV GIT_SERVER_WS=ws://git-server:8080/events
ENV CACHE_TTL=5m
ENV LOG_LEVEL=info

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:8000/health || exit 1

# Run the binary
ENTRYPOINT ["/usr/local/bin/api-server"]
