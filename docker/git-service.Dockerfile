# syntax=docker/dockerfile:1.7

# Multi-stage build for Git Server (Rust)
# rust:1.94-alpine targets aarch64-unknown-linux-musl on ARM64 by default,
# producing a fully musl-linked binary that runs on Alpine without glibc.
FROM rust:1.94-alpine AS builder

# pkgconf   – pkg-config shim for openssl-sys
# openssl-dev / openssl-libs-static – headers + static .a for OPENSSL_STATIC=1
# cmake + make – required by libgit2-sys bundled build (no system libgit2)
# musl-dev  – C headers required by cc/cmake toolchain
RUN apk add --no-cache \
    pkgconf \
    openssl-dev \
    openssl-libs-static \
    cmake \
    make \
    musl-dev

WORKDIR /build

# Tell openssl-sys to link the static Alpine openssl instead of dynamic.
ENV OPENSSL_STATIC=1

# Copy manifests
COPY git-server/Cargo.toml git-server/Cargo.lock* ./

# Create dummy src to build dependencies
RUN mkdir src && \
    echo "fn main() {}" > src/main.rs && \
    echo "pub fn lib() {}" > src/lib.rs

# Build dependencies
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/build/target \
    cargo build --release && \
    rm -rf src

# Copy actual source code
COPY git-server/src ./src

# Build application.
# Refresh mtimes for all source files so Cargo invalidates dummy artifacts
# from the dependency-caching step and recompiles the real crate.
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    --mount=type=cache,target=/build/target \
    find src -type f -name '*.rs' -exec touch {} + && \
    cargo build --release && \
    cp /build/target/release/gitstore-server /build/gitstore-server && \
    strip /build/gitstore-server

# Runtime stage
# Alpine git has no perl dependency (unlike Debian), saving ~50 MB.
# The musl binary produced above is ABI-compatible with Alpine's musl libc.
# busybox nc (already in Alpine) replaces netcat-openbsd for healthchecks.
# openssl, zlib, and libssh2 are statically linked into the binary so only
# libgcc (for Rust/GCC stack unwinding) is needed alongside git.
FROM alpine:3

RUN apk add --no-cache \
    git \
    ca-certificates \
    libgcc

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/gitstore-server /app/gitstore-server

# Allow libgit2 to open repositories in mounted volumes regardless of ownership.
# libgit2 (used by the Rust git2 crate) enforces the same safe.directory check
# as git >= 2.35.2; writing to /etc/gitconfig satisfies it without requiring
# the git binary at runtime.
RUN printf '[safe]\n\tdirectory = *\n' > /etc/gitconfig && \
    mkdir -p /data/repos

# Expose git protocol and websocket ports
EXPOSE 9418 8080

ENV GITSTORE_GIT_PORT=9418
ENV GITSTORE_WS_PORT=8080
ENV GITSTORE_DATA_DIR=/data/repos
ENV GITSTORE_LOG_LEVEL=info

CMD ["/app/gitstore-server"]
