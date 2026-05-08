# syntax=docker/dockerfile:1.7

# Multi-stage build for Git Server (Rust)
# rust:1.94-alpine targets aarch64-unknown-linux-musl on ARM64 by default,
# producing a fully musl-linked binary that runs on Alpine without glibc.
FROM rust:1.94-alpine AS builder

# Expose the BuildKit-provided TARGETARCH so we can key per-architecture caches.
ARG TARGETARCH

# pkgconf   – pkg-config shim for openssl-sys
# openssl-dev / openssl-libs-static – headers + static .a for OPENSSL_STATIC=1
# cmake + make – required by libgit2-sys bundled build (no system libgit2)
# musl-dev  – C headers required by cc/cmake toolchain
RUN apk add --no-cache \
    pkgconf \
    openssl-dev \
    openssl-libs-static \
    zlib-dev \
    zlib-static \
    cmake \
    make \
    musl-dev \
    protobuf-dev

WORKDIR /build

# Tell openssl-sys to link the static Alpine openssl instead of dynamic.
ENV OPENSSL_STATIC=1

# Copy manifests and build script
COPY gitstore-git-service/Cargo.toml gitstore-git-service/Cargo.lock* ./
COPY gitstore-git-service/build.rs ./build.rs
# Copy proto to the path build.rs expects: ../shared/proto relative to /build → /shared/proto
COPY shared/proto /shared/proto

# Create dummy src to build dependencies
RUN mkdir src && \
    echo "fn main() {}" > src/main.rs && \
    echo "pub fn lib() {}" > src/lib.rs

# Build dependencies (and regenerate proto stubs for the current tonic version)
RUN --mount=type=cache,id=cargo-registry-$TARGETARCH,target=/usr/local/cargo/registry \
    --mount=type=cache,id=cargo-git-$TARGETARCH,target=/usr/local/cargo/git \
    --mount=type=cache,id=cargo-target-$TARGETARCH,target=/build/target \
    cargo build --release && \
    rm -rf src

# Copy actual source code
COPY gitstore-git-service/src ./src

# Build application.
# Refresh mtimes for all source files and build.rs so Cargo invalidates dummy
# artifacts from the dependency-caching step and recompiles the real crate.
# Also remove stale build-script output dirs so proto stubs are regenerated.
RUN --mount=type=cache,id=cargo-registry-$TARGETARCH,target=/usr/local/cargo/registry \
    --mount=type=cache,id=cargo-git-$TARGETARCH,target=/usr/local/cargo/git \
    --mount=type=cache,id=cargo-target-$TARGETARCH,target=/build/target \
    find src -type f -name '*.rs' -exec touch {} + && \
    touch build.rs && \
    find /build/target -name 'build' -type d \
      -path '*/gitstore-server-*/build' -exec rm -rf {} + 2>/dev/null || true && \
    cargo build --release && \
    cp /build/target/release/git-service /build/git-service && \
    strip /build/git-service

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
COPY --from=builder /build/git-service /app/git-service

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

CMD ["/app/git-service"]
