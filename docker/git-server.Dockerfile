# Multi-stage build for Git Server (Rust)
FROM rust:1.94-slim AS builder

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

# Build application.
# Refresh mtimes for all source files so Cargo invalidates dummy artifacts
# from the dependency-caching step and recompiles the real crate.
RUN find src -type f -name '*.rs' -exec touch {} + && \
    cargo build --release

# Runtime stage
# Keep runtime libc compatible with the builder output.
FROM rust:1.94-slim

RUN apt-get update && apt-get install -y \
    libssl3 \
    ca-certificates \
    libssh2-1 \
    netcat-openbsd \
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
