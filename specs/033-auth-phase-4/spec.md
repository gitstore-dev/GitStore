# Feature Specification: Pluggable AuthN/AuthZ — Phase 4 gRPC HMAC Inter-Service Authentication

**Feature Branch**: `033-auth-phase-4`
**Created**: 2026-06-26
**Status**: Closed
**GH Issue**: #126
**Design Doc**: `docs/implementation/pluggable_auth_architecture.md`
**Input**: User description: "update memory dependency graph and implement auth phase 4"

## Overview

Secure the gRPC channel between the API service and the git service using a shared HMAC
bearer token. Today every gRPC call from the API to the git service is unauthenticated —
any process that can reach the git service's port can issue arbitrary git operations without
going through the API's access-control layer. Phase 4 closes this gap by adding a Tonic
interceptor on the Rust git-service side that validates the secret, and a gRPC credential
wrapper on the Go API side that injects it, so that only the API can drive the git service.

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Git service rejects callers without the shared secret (Priority: P1)

A process that connects directly to the git service's gRPC port without the correct
shared secret receives an `unauthenticated` error immediately. No git operation is
performed and no repository data is exposed. This is the primary security boundary this
phase establishes.

**Why this priority**: Without this guard, the gRPC port is an open bypass of all API
authentication and authorization. Every other auth phase is rendered moot if a caller can
skip the API entirely.

**Independent Test**: Start the git service with `GITSTORE_AUTH__GRPC__HMAC_SECRET=test-secret`
and send a raw gRPC request (e.g., `ListRefs`) with no `Authorization` header or a wrong
token — the response must be `Status::unauthenticated`. No test fixtures required beyond
a running git service instance.

**Acceptance Scenarios**:

1. **Given** the git service is running with a configured HMAC secret, **When** a gRPC call arrives with no `Authorization` header, **Then** the service returns an `unauthenticated` status and does not execute the requested operation.
2. **Given** the git service is running with a configured HMAC secret, **When** a gRPC call arrives with an incorrect bearer token, **Then** the service returns an `unauthenticated` status.
3. **Given** the git service is running with a configured HMAC secret, **When** a gRPC call arrives with the correct bearer token, **Then** the call is accepted and the operation proceeds normally.

---

### User Story 2 — API transparently attaches the shared secret to all gRPC calls (Priority: P1)

The API operator sets one new environment variable (`GITSTORE_AUTH__GRPC__HMAC_SECRET`) on
both the API and the git service. All gRPC calls from the API to the git service
automatically include the secret — no changes are needed in individual resolver or service
logic. Existing flows (push, fetch, list refs) continue working without modification.

**Why this priority**: If the Go client side is not wired, enabling validation on the Rust
side would immediately break all existing API→git-service communication. Both sides must
land together.

**Independent Test**: Run the full `make bootstrap` flow (login → createNamespace →
createRepository) with `GITSTORE_AUTH__GRPC__HMAC_SECRET` set on both services — the
bootstrap must succeed end-to-end without any gRPC `unauthenticated` errors.

**Acceptance Scenarios**:

1. **Given** both API and git service are started with the same `GITSTORE_AUTH__GRPC__HMAC_SECRET`, **When** the API calls any gRPC method on the git service, **Then** the call succeeds as it did before Phase 4.
2. **Given** the API is started without `GITSTORE_AUTH__GRPC__HMAC_SECRET` set, **When** the API attempts a gRPC call, **Then** the API fails to start or logs a clear configuration error (not a runtime 401 on first operation).
3. **Given** the API has a stale HMAC secret and the git service has a rotated secret, **When** the API calls the git service, **Then** the git service returns `unauthenticated` and the API surfaces a clear inter-service authentication error to the caller.

---

### User Story 3 — HMAC secret rotation without service outage (Priority: P2)

An operator can rotate the shared HMAC secret by deploying the new secret alongside the
old one during a rolling update window. During that window, gRPC calls bearing either the
old or the new secret are accepted, so neither service needs to restart atomically with the
other. Once both services are confirmed updated, the old secret is removed.

**Why this priority**: Secret rotation is a mandatory operational capability. Without a
rotation window, any secret update causes a service outage. This is a safety net, not a
core feature.

**Independent Test**: Configure the git service with both `hmac_secret` and
`hmac_secret_previous`. Send requests with the old secret — they must succeed. Send
requests with the new secret — they must also succeed. Remove `hmac_secret_previous` and
verify only the new secret is accepted.

**Acceptance Scenarios**:

1. **Given** the git service is configured with both a primary and a previous HMAC secret, **When** a gRPC call arrives bearing the previous secret, **Then** the call is accepted (rotation window is open).
2. **Given** the git service is configured with only the primary HMAC secret (rotation window closed), **When** a gRPC call arrives bearing the old secret, **Then** the call is rejected with `unauthenticated`.

---

### Edge Cases

- What happens when `GITSTORE_AUTH__GRPC__HMAC_SECRET` is not set on the git service? → The git service fails to start with a clear error message rather than silently accepting all callers.
- What happens when the secret is set on the git service but omitted on the API side? → The API fails to start (or logs a fatal config error) rather than silently sending unauthenticated calls that will be rejected.
- What happens when `hmac_secret_previous` equals `hmac_secret`? → The git service treats them as one active secret (no behavioral difference).
- What happens when the gRPC connection is established but the HMAC token changes mid-stream? → The interceptor is invoked per-call (not per-stream for unary; for server-streaming, once on stream open). A token change takes effect on the next call/stream.
- What happens when the gRPC port is accessed over a plaintext connection? → This is a network-layer concern outside this phase; Phase 4 only adds credential validation, not transport encryption. TLS is a separate infrastructure concern.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The git service MUST validate every incoming gRPC call against a configured HMAC bearer secret; calls missing or bearing an incorrect token MUST be rejected with an `unauthenticated` status before any operation logic runs.
- **FR-002**: The git service MUST fail to start when `GITSTORE_AUTH__GRPC__HMAC_SECRET` is not set, emitting a clear error that names the missing configuration key.
- **FR-003**: The git service interceptor MUST accept calls bearing either the primary `hmac_secret` or a `hmac_secret_previous` value during the rotation window; when `hmac_secret_previous` is absent or empty, only the primary secret is accepted.
- **FR-004**: The API gRPC client MUST attach `Authorization: Bearer <hmac_secret>` as per-call metadata on every outbound gRPC call to the git service.
- **FR-005**: The API MUST fail to start when `GITSTORE_AUTH__GRPC__HMAC_SECRET` is not set, emitting a clear error that names the missing configuration key.
- **FR-006**: All existing gRPC-dependent flows (repository creation, push, fetch, list refs) MUST continue working without caller-visible changes once both services are configured with the same secret.
- **FR-007**: The git service MUST log a structured startup message confirming that gRPC HMAC authentication is active (provider name and whether the rotation window is open).

### Key Entities

- **HMAC Secret**: A shared symmetric secret held by both API and git service; used as a bearer token for gRPC inter-service calls. Not derived from any user credential.
- **Rotation Window**: The optional period during which the git service accepts both the current and a previous HMAC secret, enabling rolling deployments without a call-rejection gap.
- **gRPC Interceptor (git-service)**: The server-side validation component that checks the `Authorization` metadata header before any handler logic runs.
- **gRPC Credential Wrapper (API)**: The client-side component that injects the HMAC token into every outbound gRPC call's metadata automatically.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A gRPC call to the git service without the correct HMAC token is rejected 100% of the time; zero unauthenticated calls reach handler logic.
- **SC-002**: All existing integration tests that exercise API→git-service communication pass without modification after Phase 4 is deployed with matching secrets on both sides.
- **SC-003**: The `make bootstrap` end-to-end flow completes successfully with `GITSTORE_AUTH__GRPC__HMAC_SECRET` set on both services.
- **SC-004**: A deliberate wrong-secret test confirms rejection within the same response latency as a valid-secret call (the validation adds no observable overhead to the happy path).
- **SC-005**: Secret rotation scenario — both old and new secrets accepted during the window, only the new secret accepted after the window closes — is verified by automated tests.
- **SC-006**: Both services emit clear startup log messages when HMAC auth is active, observable without a running gRPC client.

## Assumptions

- The `GITSTORE_AUTH__GRPC__HMAC_SECRET` environment variable format and naming convention
  are already established in the design doc (§5a) and in the `.env` local profiles; this
  spec assumes no renaming is required.
- The secret is a plain bearer string (not a signed payload) — HMAC in the name refers to
  the security model (symmetric shared secret), not a keyed-hash message authentication
  code computed per-message.
- Transport-level TLS for the gRPC channel is out of scope for Phase 4; network-layer
  security is an infrastructure concern handled separately.
- The Go gRPC client already connects to the git service; Phase 4 adds credential injection
  to the existing connection construction, not a new connection pool.
- The `hmac_secret_previous` rotation key is optional; its absence means the rotation
  window is closed and only the primary secret is accepted.

## Dependencies

- **Requires**: Phase 1 (`031-pluggable-authn-authz`) merged — provides the config
  infrastructure (`Viper`/`config-rs`) and established `GITSTORE_AUTH__*` naming convention.
- **Independent of**: Phase 3 (`032-auth-phase-3`) — gRPC HMAC auth does not depend on
  session lifecycle; both may land in either order.
- **Blocks**: Phase 5 (`034-auth-phase-5`, Git smart-HTTP) — Phase 5 adds `PushContext`/
  `AuthContext` to the gRPC stream, which assumes the channel is already secured by Phase 4.
- **GH Issue**: #126
