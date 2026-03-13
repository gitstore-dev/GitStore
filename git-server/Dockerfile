# Rust git-server Dockerfile

# Build stage
FROM rust:1.94-slim AS builder

WORKDIR /app

# Install build dependencies
RUN apt-get update && \
    apt-get install -y pkg-config libssl-dev && \
    rm -rf /var/lib/apt/lists/*

# Copy manifests
COPY Cargo.toml Cargo.lock ./

# Create dummy main to cache dependencies
RUN mkdir src && \
    echo "fn main() {}" > src/main.rs && \
    cargo build --release && \
    rm -rf src

# Copy source code
COPY src ./src

# Build the application
RUN touch src/main.rs && \
    cargo build --release

# Runtime stage
FROM debian:bookworm-slim

WORKDIR /app

# Install runtime dependencies
RUN apt-get update && \
    apt-get install -y ca-certificates libssl3 && \
    rm -rf /var/lib/apt/lists/*

# Copy binary from builder
COPY --from=builder /app/target/release/gitstore-server /usr/local/bin/gitstore-server

# Create data directory
RUN mkdir -p /data/repos && \
    chmod 755 /data/repos

# Expose ports
EXPOSE 9418 8080

# Set environment variables
ENV RUST_LOG=info
ENV GIT_REPO_PATH=/data/repos
ENV WEBSOCKET_PORT=8080

# Health check
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/gitstore-server", "health"] || exit 1

# Run the binary
ENTRYPOINT ["/usr/local/bin/gitstore-server"]
CMD ["serve"]
