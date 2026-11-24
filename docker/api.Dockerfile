# syntax=docker/dockerfile:1.7

# Multi-stage build for GraphQL API (Go)
FROM golang:1.27.2-alpine3.23 AS builder

RUN apk add --no-cache git

WORKDIR /build/gitstore-api
ENV GOWORK=off

# Copy go modules manifests
COPY gitstore-api/go.mod gitstore-api/go.sum ./
COPY shared/secretmaterial/go.mod /build/shared/secretmaterial/go.mod
COPY shared/observability/go.mod /build/shared/observability/go.mod
COPY shared/scylla-cdc-go/go.mod /build/shared/scylla-cdc-go/go.mod
COPY shared/translation/go.mod /build/shared/translation/go.mod

# Download dependencies
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# Copy source code
COPY gitstore-api/ ./
COPY shared/secretmaterial/ /build/shared/secretmaterial/
COPY shared/observability/ /build/shared/observability/
COPY shared/scylla-cdc-go/ /build/shared/scylla-cdc-go/
COPY shared/translation/ /build/shared/translation/
COPY shared/schemas /build/shared/schemas

# Build application
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /build/api ./cmd/server

# Opt-in shell image for debugging and privileged credential-volume setup.
FROM alpine:3.23.3 AS debug

ARG GIT_REVISION=unknown
LABEL org.opencontainers.image.revision=${GIT_REVISION}

RUN apk --no-cache add ca-certificates \
    && addgroup -g 65532 gitstore \
    && adduser -D -H -u 65532 -G gitstore gitstore

WORKDIR /app

# Copy binary and schemas
COPY --from=builder /build/api /app/api
COPY --from=builder /build/shared/schemas /app/schemas

# Expose ports
EXPOSE 4000
EXPOSE 4001
EXPOSE 9000
EXPOSE 6000

USER 65532:65532
CMD ["/app/api"]

# The default published image needs no shell, package manager or second Go binary.
FROM scratch AS runtime

ARG GIT_REVISION=unknown
LABEL org.opencontainers.image.revision=${GIT_REVISION}

WORKDIR /app
COPY --from=debug /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=debug /etc/passwd /etc/group /etc/
COPY --from=debug --chmod=1777 /tmp/ /tmp/
COPY --from=builder /build/api /app/api
COPY --from=builder /build/shared/schemas /app/schemas

EXPOSE 4000 4001 9000 6000
USER 65532:65532
CMD ["/app/api"]
