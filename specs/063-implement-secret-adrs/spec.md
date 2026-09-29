# Feature Specification: Implement Secret Material ADRs

**Feature Branch**: `063-implement-secret-adrs`  
**Created**: 2026-09-21  
**Status**: Draft  
**Input**: User description: "Spec 056 (PR#381) is open and may depend on ADR-001 and ADR-009. Let's implement the ADRs"

## Clarifications

### Session 2026-09-22

- Q: How should existing bare File credential references be handled? A:
  Require migration before the strict release; reject all bare references
  immediately in that release, with no legacy acceptance mode.
- Q: Does this feature introduce a File runtime consumer? A: Implement the
  reusable runtime resolver and typed-material validation with contract tests,
  wire production bootstrap identity now, and defer File runtime consumption.
  No File credential-readiness reconciler or source operation is added.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Reference integration credentials safely (Priority: P1)

As a resource author, I can declare a portable reference to credentials needed
by an integration without putting secret values, provider locations, or
environment-specific paths in the Git-backed resource.

**Why this priority**: This is the core safety promise of ADR-0001 and is a
prerequisite for resource contracts that need external credentials.

**Independent Test**: A valid same-namespace reference is accepted and remains
visible only as authored metadata; malformed, cross-namespace, or secret-value
references are rejected without creating or exposing sensitive material.

**Acceptance Scenarios**:

1. **Given** a resource with a valid same-namespace secret reference, **When**
   it is admitted, **Then** the reference is retained as authored and no secret
   value is stored, returned, or logged.
2. **Given** a resource with a reference to another namespace, **When** it is
   admitted, **Then** admission is rejected with an actionable
   cross-namespace-reference reason.
3. **Given** a resource that declares a credential type, **When** its backing
   material lacks a required item or the type is unsupported, **Then** the
   runtime resolver contract rejects the dependent operation with a
   non-sensitive reason. This feature exercises that behavior through a
   contract-test consumer, not a production File operation.

---

### User Story 2 - Bootstrap a process identity without sharing its key (Priority: P1)

As an operator, I can configure a GitStore process to obtain its own identity
material through an approved bootstrap reference, while keeping that material
unavailable to every other service.

**Why this priority**: A process identity must be available before it can make
authenticated calls; this prevents circular startup dependencies and avoids
turning another service into an impersonation path.

**Independent Test**: Starting with a valid per-service bootstrap reference
authenticates the process, while a missing, malformed, shared, or unauthorized
reference fails startup closed without falling back to a less-restricted
credential.

**Acceptance Scenarios**:

1. **Given** a service-specific bootstrap reference and accessible key
   material, **When** the process starts, **Then** it authenticates as its
   configured machine identity.
2. **Given** unavailable or incomplete identity material, **When** the process
   starts, **Then** it reports a classified, redacted failure and does not make
   unauthenticated requests.
3. **Given** a configuration source shared by multiple services, **When** it
   attempts to carry a process-identity key, **Then** startup rejects that
   unsafe configuration and identifies the need for a per-service source.
4. **Given** a process proving its own service-account identity, **When** it
   requests a short-lived access token, **Then** only an authorized issuance
   response delivers that token to the requesting process; no resolved provider
   material or private key is returned, and neither the token nor its client
   assertion appears in persisted records or observability output.

---

### User Story 3 - Rotate material without changing Git-backed resources (Priority: P2)

As an operator, I can rotate a referenced credential or process identity in its
external provider while resource manifests and callers continue to use the same
logical reference.

**Why this priority**: Rotation is essential for safe operations and is the
reason logical references must be decoupled from provider-specific locations.

**Independent Test**: Replacing material behind the same logical reference
allows a subsequent operation or credential renewal to succeed, while neither
old nor new values appear in GitStore records or observability output.

**Acceptance Scenarios**:

1. **Given** a credential has been rotated outside GitStore, **When** a
   dependent operation retries after an authentication or signing failure,
   **Then** it uses newly resolved material without changing the resource.
   Resource-credential behavior is demonstrated through a contract-test
   consumer; process-identity renewal is exercised end to end.
2. **Given** a private identity key is rotated with an overlap period, **When**
   the process renews its credential, **Then** it continues operating under the
   configured identity, the renewed token is delivered only through the
   authorized issuance response, and no resolved provider material or private
   key is exposed.

### Edge Cases

- A reference omits its optional item name and therefore addresses the entire
  secret record; a credential type still validates the items it requires.
- A provider is temporarily unavailable; the affected operation is blocked
  rather than becoming anonymous or silently using stale unbounded material.
- A resource declares credentials without a type wrapper; admission rejects the
  ambiguous shape and directs the author to the explicit credential-reference
  form.
- A secret name, key, URI fragment, whitespace, or namespace violates the
  reference contract; the error identifies the invalid field but contains no
  secret bytes.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST provide one canonical structured secret-reference
  contract for Git-backed resources, with a fixed discriminator, logical name,
  optional item name, and same-namespace-only resolution in this release.
- **FR-002**: The system MUST require credential-bearing resource fields to use
  an explicit, versioned credential type that delegates material location to
  the canonical secret reference; a bare secret reference is not a supported
  credential-field shape.
- **FR-003**: The system MUST distinguish invalid reference, not found, missing
  item, forbidden, provider-unavailable, unsupported-type, and oversized-value
  outcomes, and MUST fail closed for required material.
- **FR-004**: The system MUST prevent resolved provider material and private
  keys from being persisted in Git-backed documents, resource status,
  projections, audit payloads, logs, error text, metrics labels, or traces, or
  exposed through GraphQL reads or mutation responses. Issued access tokens and
  client assertions MUST likewise never appear in those persisted records or
  observability surfaces. Their intended authentication-protocol use remains
  permitted: a client assertion may authenticate a token request, and an issued
  access token may be returned only to the authorized requesting process in
  the token-issuance response. This is not permission to expose resolved
  provider material, persist the token response as resource status, or provide
  a general secret-read API.
- **FR-005**: The system MUST use the same secret-material acquisition boundary
  for resource credentials and a process's own identity material, while keeping
  their authorization and resolution contexts separate.
- **FR-006**: The system MUST require process identity material to use a
  bootstrap-capable source that cannot depend on a GitStore credential needed by
  that same process, and MUST prohibit use of that source for
  resource-authored references.
- **FR-007**: The system MUST require each process-identity key source to be
  readable only by its owning service and reject a shared configuration source
  that carries such material.
- **FR-008**: The system MUST retain only bounded, in-memory resolved material
  where caching is permitted and MUST not cache private keys unless the owning
  component explicitly defines that risk and rotation behavior.
- **FR-009**: The system MUST expose non-sensitive status and observability
  outcomes for reference validation and resolution, including the consumer,
  purpose, provider category, and reason without using secret names as metric
  labels.
- **FR-010**: The system MUST apply the explicit typed credential-reference and
  same-namespace rules to File resources and must not introduce payload
  retrieval, object-storage writes, or a Git-backed Secret resource as part of
  this feature. Runtime resource resolution and typed-material validation MUST
  be delivered as a reusable, contract-tested boundary; production File
  consumption and credential-readiness reconciliation are deferred. Existing
  bare File references MUST be migrated before the strict release, which MUST
  reject them without a legacy acceptance mode.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Two or more API and controller replicas must
  accept compatible reference metadata during a rolling upgrade; a replaced
  process must re-acquire its own identity without relying on process-local
  secret state. The supported baseline is spec 061's normalized identity
  contract as merged in #429: all participating API and controller versions
  must support that contract before this feature's rollout. Compatibility
  between pre-#429 and post-#429 identity contracts is outside this feature's
  rolling-upgrade guarantee.
- **PR-002 Multi-User Security**: Reference validation and resolution must
  retain existing namespace isolation and authorization; no caller can read or
  assume another service's identity material.
- **PR-003 Capacity**: At a catalogue size of 5,000,000 resources, ordinary
  admission and status paths must keep reference work keyed to the affected
  resource and must not scan the catalogue or invoke a provider during
  stateless Git validation.
- **PR-004 Backpressure**: Resolution retries must be bounded, honor caller
  deadlines, and avoid an unbounded queue, goroutine population, or retry storm
  when a provider is unavailable.
- **PR-005 Capacity Evidence**: The implementation must extend the applicable
  `make capacity` admission or controller scenario with a resolver-outage and
  recovery check, proving bounded work and the declared correctness outcome.
- **PR-006 Fault Recovery**: After a provider outage or process replacement,
  affected operations must remain blocked without data corruption and recover
  within the documented retry window after valid material becomes available.

### Key Entities

- **Secret reference**: A portable, authored pointer to externally managed
  secret material within a resource's namespace.
- **Credential reference**: A typed use of a secret reference that identifies
  the required credential shape without exposing its values.
- **Secret resolver**: The controlled boundary through which a process obtains
  secret material and receives redacted, classified failures.
- **Bootstrap identity reference**: A deployment-owned secret reference used by
  one process to obtain its own identity before authenticated GitStore calls.

### Dependencies and Assumptions

- ADR-0001 is the authoritative contract for resource-authored secret
  references; ADR-0009 is the authoritative boundary for typed credentials and
  bootstrap process identity.
- Spec 056's File-reference deletion behavior remains independent of this
  feature: it must not be blocked by a referenced File, and it does not require
  secret values to resolve a `fileRef`.
- The existing File resource and controller service-account delivery are
  treated as inputs to planning. The identity baseline is spec
  `061-controller-serviceaccount-auth`, including merged PR #429. Planning MUST
  reuse its updated controller token client and service-account enrollment flow,
  and create work only for unimplemented or insufficiently verified ADR
  obligations.
- The existing #429 contract uses shared `ObjectMetaInput`, object-typed
  `serviceAccount` mutation payloads, and
  `issueServiceAccountToken.tokenRequest.status.{token, expirationTimestamp}`.
  Token requests use `TokenRequestSpecInput` with `audiences` and
  `expirationSeconds`. These are compatibility constraints, not new API work
  for this feature; the authoritative contract is
  [spec 061's mutation contract](../061-controller-serviceaccount-auth/contracts/serviceaccount-mutations.md).
- Shared metadata does not make ServiceAccounts Git-backed resources or merge
  their identity context with resource-namespace secret resolution. Existing
  assertion-based issuance authorization and public-key enrollment/rotation
  remain unchanged.
- ADR-0009's temporary bare-reference allowance ends for File in this feature's
  strict release. Deployment is conditional on migrated Git manifests and
  projections and compatible readers; the plan must define a preparation and
  rollback sequence rather than assume old readers understand the wrapper.
- Operators, rather than Git-backed resource authors, own physical provider
  selection, material provisioning, rotation, and service-specific access.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: 100% of exercised invalid, cross-namespace, missing-item, and
  unavailable-provider cases fail closed with a classified non-sensitive
  outcome.
- **SC-002**: 100% of exercised API responses, status records, audit payloads,
  logs, error text, metrics labels, and traces contain no resolved test-provider
  secret value or private key. Issued access tokens appear only in authorized
  token-issuance responses, never in other API responses, persisted records, or
  observability output; client assertions likewise never appear in any of
  those outputs. Successful authorized issuance and rejected unauthorized
  issuance MUST both be exercised.
- **SC-003**: In a two-replica rolling-replacement test using the normalized
  identity-contract baseline defined in PR-001, both replicas accept compatible
  references and a replacement controller authenticates with its configured
  identity without manual token injection.
- **SC-004**: Under the declared sustained admission/controller scenario, a
  provider outage adds no unbounded background work and recovery resumes
  affected operations within the documented bounded retry window.
