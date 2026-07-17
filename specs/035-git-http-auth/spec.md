# Feature Specification: Git Smart-HTTP Authentication

**Feature Branch**: `035-git-http-auth`  
**Created**: 2026-06-27  
**Status**: Closed  
**Input**: Auth Phase 5 — gate all Git smart-HTTP traffic through the pluggable AuthN/AuthZ framework; enforce per-push policies end-to-end from API through to git-service hook pipeline.

## Clarifications

### Session 2026-07-12

- Q: How should `basicAuth` distinguish a transient provider error from a credential rejection when deciding between 503 and 401? → A: `err != nil` always means transient — return 503; `OutcomeDeny` with `err == nil` means credential rejection — return 401. No new types needed; the existing `err` / `decision` split is sufficient.
- Q: Where does push policy configuration live in the data model? → A: Stored as fields on the Repository record in the datastore; resolved via the existing `store.LookupRepository` call — no separate policy record or config file needed.
- Q: What is the default when push policy fields are absent or zero on a Repository record? → A: Zero/absent means unlimited — no enforcement for that limit. This extends the FR-015 sentinel consistently to all policy fields; no global default config is needed.
- Q: How does `GitHttpAuthorizer` access the repository ID without duplicating the datastore lookup? → A: A dedicated `RepoResolver` middleware runs first, calls `store.LookupRepository`, stores `repoID` via `c.Set`, and returns 404 if not found. `GitHttpAuthorizer`, `PushContextInserter`, and the route handlers all read `repoID` from the gin context.
- Q: Where should `PushContext`, `AuthContext`, and `PushPolicy` proto messages be defined? → A: New messages in the existing `gitstore/git/v1` proto package alongside `ReceivePackRequest` — no new package or file needed.

### Session 2026-06-27

- Q: Should anonymous read (fetch/clone without credentials) be allowed through `BasicAuthenticator` if the AuthZ provider permits it? → A: Yes — anonymous provider + authz decides; no-credential fetch/clone can succeed if policy allows `repository.read` for anonymous.
- Q: How should `BasicAuthenticator` behave when the auth chain returns a non-nil provider error (transient failure, not a credential rejection)? → A: Fail closed with `503 Service Unavailable` and log the error; distinguishes transient server fault from credential rejection.
- Q: How should the system handle a push policy change that occurs after the push stream has already started? → A: Snapshot at stream open — the `PushContext` policy is immutable for the lifetime of the stream; policy changes apply only to subsequent pushes.
- Q: Should the Git HTTP auth layer enforce application-level rate limiting or brute-force protection on credential failures? → A: Defer to infrastructure (reverse proxy / load balancer); no in-process rate limiting in this spec — document as an operational requirement.
- Q: Should `BasicAuthenticator` emit structured metrics for auth outcomes? → A: Yes — Prometheus counters per outcome (allow / deny / error) labelled by operation type (upload-pack / receive-pack).

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Authenticate Git HTTP Clients (Priority: P1)

A developer runs `git clone`, `git fetch`, or `git push` against a GitStore repository over HTTP. Every Git HTTP request passes through the authentication chain (including the anonymous provider). Whether access is granted depends on what the authorisation provider permits for the resolved principal — the anonymous principal can succeed for reads if policy allows it.

**Why this priority**: Routing all Git HTTP traffic through the auth chain is the minimum viable deliverable — it closes the unauthenticated surface and gives the AuthZ provider control over what anonymous and authenticated clients can do.

**Independent Test**: With `rbac-local` granting `repository.read` to the anonymous role, run `git clone http://localhost:5000/<ns>/<repo>.git` with no credentials and confirm it succeeds. Then attempt `git push` with no credentials and confirm a `401` response prompts for credentials.

**Acceptance Scenarios**:

1. **Given** an authz policy that grants `repository.read` to anonymous, **When** an unauthenticated client sends a `git-upload-pack` (clone/fetch) request, **Then** the server serves the request without requiring credentials.
2. **Given** an authz policy that denies `repository.read` to anonymous (e.g., a private repository), **When** an unauthenticated client sends a fetch request, **Then** the server responds with `401` and a `WWW-Authenticate: Basic realm="GitStore"` header.
3. **Given** an unauthenticated HTTP client, **When** it sends a `git-receive-pack` (push) request, **Then** the server responds with `401`; the git-service is never contacted.
4. **Given** a client with valid admin credentials, **When** it performs a `git push`, **Then** the push reaches the git-service and completes successfully.
5. **Given** a client presenting invalid credentials, **When** it performs any Git HTTP request, **Then** the server responds with `401`; no repository data is transferred.

---

### User Story 2 - Authorise Repository Read and Write (Priority: P1)

After authentication, the system verifies that the authenticated principal has permission to perform the requested operation (read or write) on the specific repository before forwarding the request to the git backend.

**Why this priority**: Authentication without authorisation means any authenticated user can read or write any repository. Repository-level authz is essential for multi-tenant use.

**Independent Test**: Configure a principal with only `repository.read` permission, attempt a `git push`, and confirm `403 Forbidden` is returned before the git-service is contacted.

**Acceptance Scenarios**:

1. **Given** an authenticated principal with `repository.read` permission, **When** they perform a `git fetch`, **Then** the operation succeeds.
2. **Given** an authenticated principal with only `repository.read` permission, **When** they attempt a `git push`, **Then** the server responds with `403 Forbidden` before contacting the git-service.
3. **Given** an authenticated principal with `repository.write` permission, **When** they perform a `git push`, **Then** the push is forwarded to the git-service.
4. **Given** a principal requesting access to a repository that does not exist, **When** they attempt any Git HTTP operation, **Then** the server responds with `404 Not Found`.

---

### User Story 3 - Push Policy Enforcement (Priority: P2)

When an authenticated and authorised principal pushes, the push is validated against the repository's effective push policy (maximum pack size, maximum file size, hook configuration, schema validation, and admission settings). The policy is resolved by the API at push time and forwarded to the git-service so it is enforced during pack reception and the hook pipeline.

**Why this priority**: The P1 stories cover the authentication and authorisation gate. Policy enforcement adds the per-repository safety boundary and requires the P1 infrastructure to be in place first.

**Independent Test**: Configure a repository with `max_file_size_bytes = 1048576` (1 MB), then push a commit containing a 2 MB blob. Confirm the push is rejected with an error referencing the policy limit and no objects are written to disk.

**Acceptance Scenarios**:

1. **Given** a repository with a maximum file size limit, **When** a push includes a blob exceeding that limit, **Then** the git-service rejects the push with an error referencing the policy limit; no objects are written.
2. **Given** a repository with a maximum pack size limit, **When** a push pack exceeds that limit, **Then** the git-service rejects the pack before writing any objects.
3. **Given** a repository with no special limits, **When** a valid push is received, **Then** the policy context is forwarded and the hook pipeline uses it to attribute log entries to the authenticated actor.
4. **Given** a push stream where the first chunk is missing a `PushContext`, **Then** the git-service rejects the stream before processing any ref commands.

---

### User Story 4 - Hook Pipeline Receives Typed Actor Context (Priority: P2)

The in-process hook pipeline (pre-receive validation, admission) currently receives no authenticated actor information. After this feature, every pipeline stage receives a typed context carrying the actor's identity and the resolved policy, enabling attribution in logs and audit records without reading environment variables.

**Why this priority**: Actor attribution in admission records is needed for audit trails. Hooks reading auth state from environment variables are fragile and untestable; replacing them with typed context improves reliability.

**Independent Test**: Perform an authenticated push and inspect the admission log entries. Confirm the actor subject from the push credentials appears in the log output. Confirm no hook stage reads `GIT_*` or similar auth environment variables.

**Acceptance Scenarios**:

1. **Given** a completed authenticated push, **When** the admission handler runs, **Then** log entries include the actor subject derived from the hook context actor field.
2. **Given** a push where the forwarded repository ID is inconsistent with the stream's own repository ID, **Then** the git-service rejects the stream with an error before processing any ref command.
3. **Given** a hook pipeline stage that needs push policy limits, **When** it reads those limits, **Then** it reads them from the typed hook context policy field — not from environment variables.

---

### Edge Cases

- What happens when the `(namespace, repository)` tuple from the URL cannot be resolved to a repository ID (repository deleted between URL lookup and policy resolution)?
- When the auth provider returns a transient error (e.g., OIDC JWKS endpoint timeout), the system responds with `503 Service Unavailable` and logs the error — it never fails open.
- What happens if a push stream's first chunk arrives without a `PushContext` field (malformed or old client)?
- When any push policy limit field is zero or absent, no enforcement is applied for that limit — zero is the universal "unlimited" sentinel across all policy fields.
- If the repository's push policy changes while a push stream is in progress, the policy snapshot captured in `PushContext` at stream-open remains authoritative for that stream; the new policy takes effect on subsequent pushes.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST route all Git smart-HTTP requests through the configured authentication chain (including the anonymous provider) before any git backend operation proceeds.
- **FR-001a**: If the authentication chain resolves an `OutcomeDeny` (credentials present but rejected), the system MUST respond with `401 Unauthorized` and a `WWW-Authenticate: Basic realm="GitStore"` header.
- **FR-001c**: If the authentication chain returns a non-nil `err` (transient failure), the system MUST respond with `503 Service Unavailable`, log the error with full detail, and MUST NOT serve or accept any repository data. A non-nil `err` is always treated as transient; `OutcomeDeny` with `err == nil` is a credential rejection and MUST return `401`.
- **FR-001b**: If no credentials are present and the auth chain resolves an `OutcomeAllow` for the anonymous principal, the request MUST proceed to the authorisation check (not be rejected at the auth layer).
- **FR-002**: The system MUST authenticate Git HTTP credentials using the configured authentication chain before any git backend operation proceeds.
- **FR-003**: The system MUST authorise each Git HTTP request against the configured authorisation provider using `repository.read` for upload-pack (fetch/clone) and `repository.write` for receive-pack (push), and return `403 Forbidden` when authorisation is denied.
- **FR-004**: The system MUST return `404 Not Found` for Git HTTP requests targeting a namespace or repository that does not exist. This check is performed once by the `RepoResolver` middleware before any subsequent middleware or handler runs.
- **FR-005**: The system MUST resolve the target repository's stable identifier and effective push policy from the Repository record in the datastore (via `store.LookupRepository`) before opening the streaming connection to the git-service. No separate policy record or operator config file is read at push time.
- **FR-006**: The system MUST include a push context on the first message of every receive-pack stream, carrying the actor's identity context and the resolved push policy.
- **FR-007**: The git-service MUST validate that the first chunk of a receive-pack stream contains a push context, and reject the stream with an error if it is absent.
- **FR-008**: The git-service MUST enforce the maximum pack size and maximum file size limits from the push policy during pack reception; packs or blobs exceeding the limit MUST be rejected before writing any objects to disk.
- **FR-009**: The git-service MUST derive a typed hook context from the push context and pass it to every stage of the in-process hook pipeline (validation, admission).
- **FR-010**: Hook pipeline stages MUST NOT read authentication or policy state from process environment variables; all such state MUST come from the typed hook context.
- **FR-011**: The repository identifier in the push context MUST match the repository identifier carried by the stream chunks; streams with inconsistent identifiers MUST be rejected.
- **FR-012**: Admission and validation log entries MUST include the actor subject from the hook context.
- **FR-013**: The push policy resolved by the API MUST be derived from operator-controlled and repository control-plane configuration only; it MUST NOT be read from content within the incoming push being validated.
- **FR-014**: Repository-authored policy changes within a push apply only after that push is successfully validated, admitted, and reconciled — not during the same push.
- **FR-015**: A zero or absent value for any push policy limit field (including `max_pack_size_bytes` and `max_file_size_bytes`) MUST be treated as "no limit enforced." There is no global operator default; unlimited is the baseline when a field is unset.
- **FR-016**: The push policy captured in `PushContext` at stream-open is immutable for the lifetime of that stream; policy changes made during an in-progress push take effect only on subsequent pushes. The `config_resource_version` field in `PushContext` records which policy version was in effect for auditability.
- **FR-017**: `BasicAuthenticator` MUST emit Prometheus counters for each auth outcome — allow, deny, and provider error — labelled by Git operation type (`upload-pack` or `receive-pack`).
- **FR-018**: The server MUST pre-register `go-grpc-prometheus` client metric vectors at startup by calling `gitclient.RegisterClientMetrics(prometheus.DefaultRegisterer)` so that gRPC client metrics (`grpc_client_started_total`, `grpc_client_handled_total`, `grpc_client_handling_seconds`) appear in `/metrics` output with zero values before the first RPC fires. All auth outcome counters (FR-017) MUST also be registered on `prometheus.DefaultRegisterer` (consistent with the existing `gitstore_datastore_*` metrics) so that `promhttp.Handler()` serves the complete metric set from a single registry.
- **FR-019**: The API server MUST expose a `/metrics` HTTP endpoint on the GraphQL port serving Prometheus text format, implemented as a `Metrics` method on the existing `health.Handler` (mirroring the controller-manager pattern), so that auth outcome counters (FR-017) and gRPC client metrics (FR-018) are queryable from a single scrape target.

### Key Entities

- **PushContext**: Immutable context attached to the first message of a receive-pack stream; carries namespace, repository name, repository ID, config resource version, actor identity context, and push policy.
- **AuthContext**: Sanitized snapshot of the authenticated principal's identity (subject, issuer, auth method, roles, groups, scopes) — not a raw credential or token.
- **PushPolicy**: Per-repository limits and hook configuration read from fields on the Repository datastore record: maximum pack size, maximum file size, hook enablement, schema validation policy, admission control policy.
- **HookContext**: Typed struct passed to every in-process hook stage; derived from `PushContext`; replaces environment-variable auth state in hooks.
- **BasicAuthenticator**: `security.Authenticate.BasicAuthenticator` — the existing gin middleware in `internal/middleware/security`. Handles Basic auth for the Git smart-HTTP mux; already wired in `githttp.NewMux`. Needs to emit FR-017 counters and distinguish transient errors from credential rejections: `err != nil` → 503; `OutcomeDeny` with `err == nil` → 401 (per FR-001c). No new error types required.
- **RepoResolver**: new gin middleware in `githttp` package. Reads `(:namespace, :repo)` path params, calls `store.LookupRepository`, stores the result as `repoID` in the gin context via `c.Set("repoID", ...)`, and aborts with `404` if not found. Runs before `GitHttpAuthorizer` and `PushContextInserter`; eliminates the per-handler `resolveRepo` calls.
- **GitHttpAuthorizer**: `security.Authorize.GitHttpAuthorizer` — gin middleware stub in `internal/middleware/security`. Reads `repoID` from the gin context (set by `RepoResolver`), checks the authenticated principal's permission (`repository.read` for upload-pack, `repository.write` for receive-pack), and aborts with `403 Forbidden` if denied. Wired into `githttp.NewMux` after `BasicAuthenticator` and `RepoResolver`.
- **PushContextInserter**: gin middleware to be added in `githttp.NewMux` on the receive-pack route only. Reads `repoID` from the gin context (set by `RepoResolver`), reads push policy fields from the Repository record already fetched, builds a `PushContext` proto carrying the principal from context and the resolved policy, and stores it on the request context for `ReceivePack` to attach to the first stream chunk.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every Git HTTP request without credentials receives a `401` response with a `WWW-Authenticate` header; no repository data is served or accepted.
- **SC-002**: An authenticated push from a principal with valid write permission completes end-to-end successfully; the admission and validation logs for that push include the actor subject.
- **SC-003**: A push containing a blob exceeding the repository's maximum file size limit is rejected without writing any pack objects to disk; the error message references the exceeded limit.
- **SC-004**: A receive-pack stream missing a first-chunk push context is rejected by the git-service before any ref command is processed.
- **SC-005**: All existing integration tests for authenticated GraphQL operations continue to pass without modification after this feature is introduced (no regression to the auth chain for non-Git traffic).
- **SC-006**: Hook pipeline stages pass CI with no environment-variable reads for auth or policy state confirmed by code review; all such reads are replaced by typed hook context field access.
- **SC-007**: After an authenticated push and a rejected (unauthenticated) push attempt, the `git_http_auth_allow_total` and `git_http_auth_deny_total` counters are non-zero and queryable from `GET /metrics` on the GraphQL port. The gRPC client metric vectors (`grpc_client_started_total` etc.) are present with zero values at startup before any RPC fires.

## Dependencies

- **033-auth-phase-4** (shipped): HMAC inter-service authentication between API and git-service. Required so the API can authenticate itself to the git-service when opening the gRPC stream.
- **031-pluggable-authn-authz** and **032-auth-phase-3** (shipped): `ChainedAuthN`, `AuthZProvider`, `Principal`, `ProviderRegistry`. Required for `BasicAuthenticator` to call the authentication chain and for `GitHttpAuthorizer` to call the authorisation chain.
- **Proto contract** (part of this spec): `PushContext`, `AuthContext`, and `PushPolicy` message types must be added to the existing `gitstore/git/v1` proto package alongside `ReceivePackRequest`, and both the Go and Rust generated code must be regenerated before either service can consume them.

## Operational Requirements

- Brute-force and credential-stuffing protection for the Git HTTP surface MUST be handled at the reverse proxy or load balancer layer. Operators MUST configure rate limiting on repeated `401` responses from the Git HTTP port before exposing it to untrusted networks. This is not enforced in application code.

## Assumptions

- The Git smart-HTTP mux (`githttp.NewMux`) is already built on gin; middleware is added by extending the `r.Use(...)` / route-level chain in `NewMux`, not by wrapping an `http.Handler`.
- The `rbac-local` policy already defines `repository.read` and `repository.write` action names. No policy file schema changes are needed.
- A single admin principal covers all initial testing; multi-user authz testing is deferred to Phase 6 (OIDC) or a future user-management spec.
- If no per-repository policy fields are set (zero/absent), no limits are enforced — there is no global operator default. Operators set limits explicitly on each repository record.
- SSH transport and dumb HTTP are out of scope for this spec; only smart-HTTP (`/info/refs`, `git-upload-pack`, `git-receive-pack`) is covered.
- The repository control-plane resources (Namespace, Repository) referenced for policy resolution already exist and are accessible from the API.
