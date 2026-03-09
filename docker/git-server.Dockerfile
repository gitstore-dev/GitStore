# Multi-stage build for Git Server (Rust)
FROM rust:1.75-slim as builder

# Install dependencies
RUN apt-get update && apt-get install -y \
    pkg-config \
    libssl-dev \
    cmake \
    libssh2-1-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /build

# Copy manifests
COPY git-server/Cargo.toml git-server/Cargo.lock* ./

# Create dummy src to build dependencies
RUN mkdir src && \
    echo "fn main() {}" > src/main.rs && \
    echo "pub fn lib() {}" > src/lib.rs

# Build dependencies
RUN cargo build --release && \
    rm -rf src

# Copy actual source code
COPY git-server/src ./src

# Build application
RUN touch src/main.rs && \
    cargo build --release

# Runtime stage
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y \
    libssl3 \
    ca-certificates \
    libssh2-1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/target/release/gitstore-server /app/gitstore-server

# Create data directory
RUN mkdir -p /data/repos

# Expose git protocol and websocket ports
EXPOSE 9418 8080

ENV GITSTORE_GIT_PORT=9418
ENV GITSTORE_WS_PORT=8080
ENV GITSTORE_DATA_DIR=/data/repos
ENV GITSTORE_LOG_LEVEL=info

CMD ["/app/gitstore-server"]
