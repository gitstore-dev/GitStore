# Feature Specification: Admission Control Contract

**Feature Branch**: `027-admission-contracts`  
**Created**: 2026-06-18  
**Status**: Closed  
**Input**: User description: "Admission Control is the mechanism that makes it possible for core and external systems to mutate and validate resources before they are hydrated into memdb/scylladb. We need to define the contract for both Mutation and Validation and migrate the existing admission validations into the validation layer of Admission Control."

## Overview

GitStore processes resource files committed to git repositories and stores them in its datastore after validating and admitting them. Today, semantic validation checks (such as verifying that a product variant references a valid parent product, that option selections are compatible, and that category hierarchies are acyclic) are embedded directly inside the storage handler with no defined boundary or extension model.

This feature defines a first-class admission control framework — inspired by the Kubernetes admission controller model — that separates the concerns of mutation and validation into distinct, composable phases. It also migrates the existing semantic validation checks into that new framework so they are expressed as independent, testable policies rather than inline imperative code.

Pre-receive structural validation (which checks YAML schema and mandatory frontmatter fields before a git push is accepted) is explicitly **outside** the scope of this feature; it remains a separate blocking gate.

---

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Resource author gets clear, attributable validation feedback (Priority: P1)

A catalog author pushes a `ProductVariant` file that references a parent product which does not yet exist in the system. The author expects to understand from the resource's status why certain conditions are not yet satisfied — and which policy made that determination — without reading internal server logs.

**Why this priority**: The admission control contract's primary user-facing value is surfacing well-structured status conditions back to the author. Without this, the new framework delivers no visible improvement over the current inline checks.

**Independent Test**: Push a `ProductVariant` referencing a non-existent product. Query the resource status via GraphQL and verify that a named condition (`ProductResolved: false`) is present with a reason string that identifies the unsatisfied policy.

**Acceptance Scenarios**:

1. **Given** a `ProductVariant` whose `productRef` names a product not yet in the datastore, **When** the push is processed through admission, **Then** the stored status contains a `ProductResolved: false` condition with a human-readable reason.
2. **Given** a `ProductVariant` with invalid CEL syntax in a pricing expression, **When** the push is processed through admission, **Then** the stored status contains a `PricingAccepted: false` condition that names the specific expression field.
3. **Given** a `CategoryTaxonomy` entry that would create a cycle with existing entries, **When** the push is processed through admission, **Then** the stored status contains an `Acyclic: false` condition.

---

### User Story 2 — Platform engineer registers a new validation rule without modifying the storage layer (Priority: P2)

A platform engineer adds a new business rule (e.g., "a ProductVariant must have at least one price before it is considered ready"). Today this requires modifying the monolithic admission handler. With this feature, the engineer registers a new `ValidatingAdmissionPolicy` and wires it into the chain without touching the storage or RPC code.

**Why this priority**: This is the architectural justification for the feature. If the framework does not support independent registration of policies, it delivers no extensibility benefit over the status quo.

**Independent Test**: Add a new `ValidatingAdmissionPolicy` implementation, register it in the chain, push a resource that violates its rule, and verify the violation surfaces as a status condition — without any changes to the storage handler.

**Acceptance Scenarios**:

1. **Given** a newly registered `ValidatingAdmissionPolicy` for a resource kind, **When** a resource of that kind is pushed, **Then** the policy's `Validate` method is invoked and its result is incorporated into the resource status.
2. **Given** a policy that denies a resource, **When** the push is processed, **Then** the resource is still stored (admission in the post-receive phase is non-blocking) but its status reflects the denial conditions.
3. **Given** a policy registered after the chain is constructed at startup, **When** the system processes a push, **Then** the policy is invoked in order alongside existing policies.

---

### User Story 3 — Platform engineer defines a mutating policy extension point (Priority: P3)

A platform engineer needs to add a default value to a resource field when the author omits it (e.g., setting a default `inventory.policy` to `"deny"` when not specified). The framework must expose a `MutatingAdmissionPolicy` extension point so such defaults can be applied before validation runs.

**Why this priority**: Mutation support is in scope for contract definition but no mutating implementations are required. The extension point must exist so that future specs can implement mutating controllers without reworking the admission framework.

**Independent Test**: Register a `MutatingAdmissionPolicy` that sets a default field value. Push a resource that omits the field. Verify the mutated value is visible in the stored resource.

**Acceptance Scenarios**:

1. **Given** a registered `MutatingAdmissionPolicy` for a resource kind, **When** that kind is admitted, **Then** the policy's `Mutate` method is called before any validating policies run.
2. **Given** a mutating policy returns a patch, **When** the admission chain runs, **Then** the patched object is passed to all downstream validating policies, not the original.

---

### User Story 4 — External system integrates via webhook extension points (Priority: P4)

An operator wants to route admission decisions through an external HTTP service (e.g., a policy engine or audit logger) without modifying GitStore's source. The framework must define `MutatingAdmissionWebhook` and `ValidatingAdmissionWebhook` interfaces as named extension points, even if no HTTP callout implementation is shipped in this spec.

**Why this priority**: The webhook extension points are in scope as contract definition only. The interface must exist and be named in the right place in the pipeline order so that future specs can wire external callouts without redefining the chain.

**Independent Test**: The interface types are defined and correctly positioned in the admission chain's execution order (after built-in policies, before storage). Unit tests verify chain ordering.

**Acceptance Scenarios**:

1. **Given** a `ValidatingAdmissionWebhook` registered in the chain, **When** a resource is admitted, **Then** the webhook's `Validate` method is called after all built-in validating policies have run.
2. **Given** a `MutatingAdmissionWebhook` registered in the chain, **When** a resource is admitted, **Then** the webhook's `Mutate` method is called after built-in mutating policies and before any validating policies.

---

### Edge Cases

- What happens when a validating policy panics or returns an unexpected error? The chain must not propagate the panic to the caller; it logs the error and treats the result as a denial with a system-error reason.
- What happens when the same resource kind has no registered policies? The chain short-circuits to `Allowed` immediately without iterating.
- What happens when a push contains a mix of resource kinds, some with policies and some without? Each resource is evaluated independently; a policy for kind A is not invoked for kind B.
- What happens when a `PushSet` is large and a policy needs cross-resource data? The policy receives the full `PushSet` slice but must not mutate it; read-only access only.
- What happens when `CommitFile` or `DeleteFile` is used to write a resource? The `TriggerCommitFile` trigger constant is defined and the `AdmissionRequest` struct supports it, but no callout is wired in this spec; those writes bypass the admission chain until a future spec wires them.
- What happens when a mutating policy returns a patch but the downstream storage handler does not apply it? This is undefined behavior in the current scope; patch application is the responsibility of the storage caller, and the spec documents this contract clearly.

---

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The system MUST define an `AdmissionRequest` type that carries the resource object, the prior version of the resource (if any), the operation (create/update/delete), the resource kind and name, the namespace, the trigger source, and any git push context.
- **FR-002**: The `AdmissionRequest` type MUST be generic enough to carry any resource object regardless of whether the resource is git-backed or datastore-only, so that future specs can use the same contract for GraphQL mutation admission.
- **FR-003**: The system MUST define an `AdmissionDecision` sealed type with exactly two outcomes: `Allowed` and `Denied`. `Denied` MUST carry a human-readable reason and an optional field path.
- **FR-004**: The system MUST define a `MutatingAdmissionPolicy` interface with a `Name()` identifier and a `Mutate(request)` method returning an `AdmissionDecision`.
- **FR-005**: The system MUST define a `ValidatingAdmissionPolicy` interface with a `Name()` identifier and a `Validate(request)` method returning an `AdmissionDecision`.
- **FR-006**: The system MUST define a `MutatingAdmissionWebhook` interface and a `ValidatingAdmissionWebhook` interface as named extension points, with the same method signatures as their policy counterparts.
- **FR-007**: The system MUST provide an admission `Chain` that runs phases in this order: (1) mutating policies, (2) mutating webhooks, (3) validating policies, (4) validating webhooks. A `Denied` result from any step MUST short-circuit the remainder of the chain.
- **FR-008**: The `Chain` MUST expose registration methods for each of the four extension point types so that policies and webhooks can be added independently without recompilation of the chain itself.
- **FR-009**: The system MUST migrate the `ProductVariant` semantic checks (parent product resolution, option compatibility, CEL expression syntax validation) out of the storage handler and into a registered `ValidatingAdmissionPolicy`.
- **FR-010**: The system MUST migrate the `CategoryTaxonomy` semantic checks (parent resolution, cycle detection) out of the storage handler and into a registered `ValidatingAdmissionPolicy`.
- **FR-011**: The migrated policies MUST produce the same named status conditions that the current inline checks produce (`ProductResolved`, `OptionsAccepted`, `PricingAccepted`, `ParentResolved`, `Acyclic`) so that existing consumers of resource status are not broken.
- **FR-012**: The `AdmissionRequest` type MUST include a `PushSet` field containing all other resources in the same push batch, so that cross-resource policies (e.g., cycle detection across a batch of taxonomy entries) can inspect sibling resources.
- **FR-013**: The `AdmissionRequest` type MUST include a `Trigger` field that identifies the code path that initiated admission (git push, GraphQL mutation, or internal file commit), so that policies can apply trigger-specific logic if needed.
- **FR-014**: The system MUST define the `TriggerCommitFile` constant and document the intended hook point in `CommitFile`/`DeleteFile` even though no callout is wired in this spec.
- **FR-015**: The storage handlers for `Product`, `ProductVariant`, `CategoryTaxonomy`, and `Collection` MUST invoke the admission chain and incorporate its result into the status written to the datastore, replacing the current inline checks.
- **FR-016**: The pre-receive structural validation path (YAML schema and frontmatter checks) MUST remain unchanged and separate from the admission chain.
- **FR-017**: No mutating policy implementations MUST be shipped in this spec; only the interface and chain infrastructure.

### Key Entities

- **AdmissionRequest**: The input to the admission chain. Carries the resource object and its prior state, the operation type, kind, name, namespace, trigger, optional git context (repository ID, commit SHA, ref name, revision), and a slice of sibling resources in the same batch.
- **AdmissionDecision**: The sealed output of any admission policy or webhook. Either `Allowed` (with optional mutation patches) or `Denied` (with a reason and optional field path).
- **MutatingAdmissionPolicy**: A named, built-in extension point invoked in phase 1 of the chain. May return patches to be applied to the resource before validation.
- **ValidatingAdmissionPolicy**: A named, built-in extension point invoked in phase 3 of the chain. May only allow or deny; patches are ignored.
- **MutatingAdmissionWebhook**: An external extension point invoked in phase 2. Interface defined; HTTP transport not implemented in this spec.
- **ValidatingAdmissionWebhook**: An external extension point invoked in phase 4. Interface defined; HTTP transport not implemented in this spec.
- **Chain**: The admission orchestrator. Holds ordered registries of each extension point type and executes them in phase order, short-circuiting on the first denial.
- **ProductVariantValidatingPolicy**: A concrete `ValidatingAdmissionPolicy` for `ProductVariant` resources. Encapsulates parent product resolution, option compatibility, and CEL expression syntax checks.
- **CategoryTaxonomyValidatingPolicy**: A concrete `ValidatingAdmissionPolicy` for `CategoryTaxonomy` resources. Encapsulates parent resolution and cycle detection using the `PushSet`.

---

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: All existing `ProductVariant` and `CategoryTaxonomy` status conditions (`ProductResolved`, `OptionsAccepted`, `PricingAccepted`, `ParentResolved`, `Acyclic`) continue to be produced with identical values after migration, verified by running the existing integration test suite without modification.
- **SC-002**: A new `ValidatingAdmissionPolicy` for an existing resource kind can be registered and exercised end-to-end without modifying any storage handler code, verified by adding a test-only policy in the integration tests.
- **SC-003**: The admission chain execution order (mutating policies → mutating webhooks → validating policies → validating webhooks) is verifiable by unit test without any push or database involvement.
- **SC-004**: The admission chain handles a policy that returns a `Denied` decision without propagating panics or unhandled errors to the storage handler, verified by a unit test that registers a panicking policy stub.
- **SC-005**: The `AdmissionRequest` struct can represent a resource from any trigger source (git push, GraphQL, internal write) without requiring a separate type per source, verified by constructing requests for each trigger in unit tests.
- **SC-006**: The end-to-end git push path (pre-receive validation → post-receive admission → datastore storage) continues to pass all integration tests after the migration with no performance regression observable in local test execution.

---

## Assumptions

- The pre-receive structural validation (`validate/validator.go`, `ValidateResources` RPC) is deliberately excluded from this framework. It remains a blocking gate and is not a candidate for the admission chain.
- Admission in the post-receive phase is and remains non-blocking (fire-and-forget). A `Denied` result from the chain does not reject the git push; it sets status conditions to reflect the denial. This is consistent with the existing behavior.
- Mutating policies may return patches describing desired field modifications. The storage handler is responsible for applying those patches before writing to the datastore. Patch application mechanics are implementation details for the planning phase.
- The `CommitFile` and `DeleteFile` internal write paths bypass the admission chain in this spec. The `TriggerCommitFile` constant is a forward-compatibility marker only.
- Webhook callouts (external HTTP round-trips) are interface definitions only; no HTTP transport, retry logic, or timeout configuration is implemented in this spec.
- The existing `AdmissionContext` struct in `cataloggrpc/context.go` is superseded by the `GitAdmissionContext` embedded in `AdmissionRequest`; the old struct may be removed or aliased during implementation.
- Cycle detection for `CategoryTaxonomy` currently relies on a two-pass batch pre-processing step in `AdmitResources`. That pre-pass stays in the storage handler; the policy receives the result via `PushSet` and does not re-implement the graph traversal from scratch.

---

## Out of Scope

- Implementing any mutating admission controller (e.g., defaulting fields).
- Wiring admission control to `CommitFile` / `DeleteFile` internal writes.
- Implementing external webhook HTTP callouts or webhook configuration management.
- Admission control for datastore-only resources created via GraphQL mutations (future spec).
- Changes to pre-receive structural validation.
- Changes to the Rust git-service hook pipeline or the proto contract.
