# ADR 0015: Resource Lifecycle Hooks

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: extension authors, catalog and commerce operators, and Git service, API and
controller-manager maintainers.

**Contracts**: [039 Resource lifecycle hooks](../implementation/039-resource-lifecycle-hooks.md).

**Supersedes in part**: [036 Git service extension architecture](../implementation/036-git-service-extension-architecture.md)
(§7 post-receive durability and the Phase 4 async webhook plan) and the action and gate model of
[037 Custom commerce workflows](../implementation/037-custom-commerce-workflows.md).

## Context

GitStore needs one way for extensions, including custom resources
([ADR 0016](0016-custom-resource-definitions.md)), seller/buyer workflows
([037](../implementation/037-custom-commerce-workflows.md)) and third-party integrations, to take
part in a resource's lifecycle. Several partial mechanisms already exist:

- **Git service.** The `HookPipeline` runs `SchemaValidationHandler` fail-closed at pre-receive
  (through `CatalogService.ValidateResources`) and `AdmissionControlHandler` fire-and-forget at
  post-receive (through `AdmitResources`).
- **API.** Spec 027's admission `Chain` runs four phases: mutating policies, mutating webhooks,
  validating policies and validating webhooks. The `Register*Webhook` methods exist but nothing
  calls them.
- **Durable watch journal.** `internal/watchjournal` provides resumable, at-least-once delivery
  across API replicas. The in-memory `eventbus` is not durable.
- **036** proposes a generic extension registry in the Git service. **037** proposes
  workflow-specific gates and WASI actions.

Resources live in four storage groups ([resource storage](../resource-storage/README.md)):
Git + datastore, datastore-only, LFS/object and transient. The groups differ in when a write
becomes authoritative, and so in what a hook may safely do. A Git-backed write is authoritative
once the ref moves, and it is authored by a human who reviews the diff. Changing that content
server-side would make the stored state diverge from the reviewed commit.

Prior art falls into five patterns:

| Pattern                      | Example                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               | Lesson for GitStore                                                                            |
|------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------|
| In-process expression policy | Kubernetes [ValidatingAdmissionPolicy](https://kubernetes.io/docs/reference/access-authn-authz/validating-admission-policy/) (GA in 1.30) separates policy, parameter and binding. [KEP-3488](https://github.com/kubernetes/enhancements/tree/master/keps/sig-api-machinery/3488-cel-admission-control) cites the separate infrastructure and network-hop latency of webhooks as its motivation. [Gatekeeper](https://open-policy-agent.github.io/gatekeeper/website/docs/validating-admission-policy) can generate VAP resources from its templates. | Make CEL the default admission extension. Webhooks are the exception.                          |
| Synchronous remote hook      | commercetools [API Extensions](https://docs.commercetools.com/api/projects/api-extensions) run "before the result is persisted", with a 2 s default and 10 s maximum timeout, and at most 25 per project. Saleor [sync webhooks](https://docs.saleor.io/developer/extending/webhooks/synchronous-events/overview) warn of a "significant performance impact".                                                                                                                                                                                         | Hard caps, trigger predicates and short deadlines. Only trusted operators get this slot.       |
| Asynchronous subscription    | commercetools Subscriptions, and [CloudEvents 1.0.2](https://github.com/cloudevents/spec/blob/v1.0.2/cloudevents/spec.md) envelopes signed per [Standard Webhooks](https://www.standardwebhooks.com/).                                                                                                                                                                                                                                                                                                                                                | Side effects run after commit, from a durable log, with duplicates allowed and detectable.     |
| Sandboxed pure function      | [Shopify Functions](https://shopify.dev/docs/api/functions/latest): WebAssembly, 256 kB module, 11 M instructions, 128 kB input, and "Shopify doesn't allow nondeterminism". Input comes from a GraphQL input query.                                                                                                                                                                                                                                                                                                                                  | Extensible business logic (price, eligibility, routing) without network calls on the hot path. |
| Pipeline of functions        | [Crossplane composition functions](https://docs.crossplane.io/latest/composition/compositions/) run in order over gRPC and return desired state.                                                                                                                                                                                                                                                                                                                                                                                                      | Functions compute desired state and never write storage themselves.                            |

## Decision

GitStore has **five hook types**. Which of them apply depends on the resource's **storage
group**.

### 1. Hook types

| Type                   | Runs where                                      | Timing                                            | May change the object?                                           | Trust tiers ([036 §4](../implementation/036-git-service-extension-architecture.md#4-trust-boundaries-and-threat-model)) |
|------------------------|-------------------------------------------------|---------------------------------------------------|------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------|
| **Admission policy**   | In-process CEL in the API admission `Chain`     | Synchronous, before persistence                   | Validating everywhere. Mutating only on datastore-only resources | Any. Namespace-scoped policies are sandboxed by CEL cost limits                                                         |
| **Admission webhook**  | Remote HTTPS call from the API                  | Synchronous, before persistence, under a deadline | Same rule as policies                                            | T0/T1 only                                                                                                              |
| **Lifecycle gate**     | Condition + finalizer, evaluated by controllers | Asynchronous. Blocks a named transition           | No. It only holds or releases a transition                       | Any. External attestations need signed evidence                                                                         |
| **Event subscription** | Controller outbox → CloudEvents delivery        | Asynchronous, after commit, at-least-once         | No                                                               | Any                                                                                                                     |
| **Function**           | Sandboxed WASM module in the API or controller  | Synchronous, inside a computation                 | Returns a value. Never writes storage                            | Any. Sandboxing enforces the bounds                                                                                     |

T2/T3 extensions (namespace-installed, third-party) never get a synchronous remote slot. Tier
guarantees (per-namespace limits, egress policy, secret access) are defined by 036 §4 and are
unchanged.

### 2. Storage-group matrix

| Storage group       | Admission (policy/webhook)                                                                                                             | Gates                                                                                              | Subscriptions                               | Functions                                                   |
|---------------------|----------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------------------------------|---------------------------------------------|-------------------------------------------------------------|
| **Git + datastore** | **Validating only.** Lane A rejects the push at pre-receive. Lane B denial sets `AdmissionAccepted=False` on an already-moved ref (§4) | Yes (for example `ReleaseEligible`, ADR 0014)                                                      | From the watch journal, per admitted change | Read-model computation only (for example resolved defaults) |
| **Datastore-only**  | Mutating and validating, inside the write transaction. The outbox/journal row is written in the same transaction                       | Yes (for example Order transitions)                                                                | From the journal                            | Yes (pricing, eligibility, routing)                         |
| **LFS / object**    | Validating on the manifest (a Git-backed File). Payload facts never block a push                                                       | **Primary mechanism.** A payload fact becomes a gate (for example a virus scan gates `File Ready`) | Payload-processing events                   | Metadata extraction                                         |
| **Transient**       | Validating only. There is nothing to persist                                                                                           | No                                                                                                 | No (audit only)                             | Yes. This is the main use (quotes, previews, reviews)       |

**Mutating hooks are banned on Git-backed resources.** Defaults, normalization and computed values
belong in `status.resolved` or the hydrated read model, derived deterministically from the admitted
blob. A policy that needs a field set must reject the push and tell the author which field. The
Git-backed `Chain` refuses to register mutating policies or webhooks. Its phases 1–2 are empty by
construction, and an attempt to register one is a startup error.

### 3. Registration and trust

Every registration is a Git-backed resource: reviewable, versioned, and admitted like any other
manifest. Where a registration is admitted decides its scope and trust, following the
`gitstore-system` repository convention of [ADR 0002](0002-namespace-lifecycle.md):

- **Cluster-scoped registrations** are admitted only from `gitstore-system/gitstore-system`, the
  same rule that applies to `Namespace` manifests. Examples are every `AdmissionHookConfiguration`
  (remote T0/T1 webhooks) and cluster-wide policies and bindings. Admission rejects them in any
  other repository, and creating one also requires `admissionHookConfiguration.create`
  ([ADR 0010](0010-authorization-model.md)).
- **Namespace-scoped registrations** are admitted only from `<namespace>/gitstore-system` and
  apply only in that namespace. These are `ValidatingAdmissionPolicy`,
  `ValidatingAdmissionPolicyBinding`, `LifecycleGate`, `EventSubscription` and `FunctionBinding`.
- **Tier is derived from where a registration is admitted, never declared by its author.** A
  remote webhook found anywhere except `gitstore-system/gitstore-system` is rejected.
- **The API owns the registry** and reads it from its own datastore. This answers 036 §18: the
  stateless Git service never reads a dynamic registry.
- **Operator safety limits stay in static config**, because they bound what Git-declared hooks may
  do:
  - whether remote webhooks are allowed at all;
  - the push deadline;
  - maximum hooks per push and per kind;
  - per-hook timeout ceilings;
  - the egress allowlist and SSRF policy.

  If these lived in Git, anyone with push access to `gitstore-system` could loosen the limits
  that constrain them.
- **Revocation is a datastore-only kill switch** (`HookSuspension`). It is effective within one
  registry refresh interval and requires the `hookSuspension.create` action. It is the break-glass path for
  stopping a misbehaving hook without a Git push or review.
- A registration takes effect for pushes **after** its own admission. A push that both installs a
  policy and violates it is judged by the policies in force before the push.

Registration schemas, CEL variables, match rules and failure policies are specified in
[039](../implementation/039-resource-lifecycle-hooks.md).

### 4. Git push admission: two lanes

A synchronous hook must never hold a large push hostage.

**Self-protection.** Webhooks are never called for registration kinds, for `HookSuspension`, or
for any push to a `gitstore-system` repository. Only core admission and CEL policies apply there.
A failing `failurePolicy: Fail` webhook therefore can never block the push that fixes or removes
it. This is the same exemption Kubernetes applies to its webhook configuration objects.

**Lane A: bounded and fail-closed (pre-receive, `ValidateResources`).**

1. **Policies first.** CEL policies run in-process against the parsed objects. They have
   estimated-cost limits and need no network.
2. **Webhooks (T0/T1 only)**, with these bounds:
   - `matchConditions` (CEL) pre-filter per object. A hook that matches nothing is never called.
   - **One batched call per hook per push**, carrying every matched object.
   - All hooks run as a **parallel fan-out under one push deadline**, with a per-hook timeout
     and a per-hook circuit breaker.
   - A **decision cache** whose key covers **everything the hook was sent**: hook ID and version,
     policy and params digests, plus a digest of the per-object request context. A cached
     decision can therefore never apply to a request the hook didn't see. Hooks declare
     `contextFields` (for example `[object, oldObject, operation, ref]`), and only those fields
     are sent and keyed. Narrower fields give more cache hits on re-pushes and rebases. Wider
     fields, such as `pusher`, give fewer.
   - `onLargePush: Defer | Reject` for pushes above a matched-object threshold. `Defer` moves the
     hook to Lane B for that push. `Reject` fails the push with a remedy message.
   - `failurePolicy: Fail | Ignore` on timeout or open circuit. `Ignore` also records a Lane B
     re-check, so the result is never lost.
3. **Diagnostics.** Git forwards pre-receive stdout/stderr to the pushing client
   ([githooks](https://git-scm.com/docs/githooks)), so rejections are printed as `remote:` lines
   with `path:line:column`, taken from YAML node positions. For example:
   `remote: error products/coat.md:7:3 [price-floor] spec.price below floor`.

Any deny in Lane A rejects every ref in the push. Git does not update any ref when pre-receive
exits non-zero.

**Lane B: asynchronous and receipted (post-receive, `AdmitResources`).** Heavy checks run here:
cross-resource checks, deferred webhooks, and anything with `mode: Async`.

- The result is an **`AdmissionReport`**, a datastore-only resource keyed by
  **`(repository, commit, ref, hook)`**. A commit reachable from two refs can be judged
  differently per ref, because bindings may match on ref. The report is Checks-API-like, with
  `status` (`queued|in_progress|completed`), `conclusion` (`success|failure|neutral|skipped`) and
  annotations using the fields of
  [GitHub check-run annotations](https://docs.github.com/en/rest/checks/runs) (path, start/end
  line and column, level, title, message). The report can be exported losslessly as a
  [SARIF 2.1.0](https://docs.oasis-open.org/sarif/sarif/v2.1.0/sarif-v2.1.0.html) `run`, with
  `versionControlProvenance` carrying `revisionId` = commit and `branch` = ref.
- A denial in Lane B sets `AdmissionAccepted=False` with reason `AdmissionReportFailed` on the
  affected resources. The ref has already moved, but the read model keeps serving the last
  accepted generation. A later push that fixes the issue produces a fresh report.
- **Git notes projection.** The API derives a **rebuildable** projection at
  `refs/notes/gitstore/admission` (one note per commit, a summary per ref and hook), so
  `git log --notes=gitstore/admission` shows results locally. The notes are derived output:
  - they are never authoritative;
  - `refs/notes/*` is excluded from admission;
  - `refs/notes/*` is protected from user pushes;
  - the notes can be rebuilt from reports at any time.

  Git does not rewrite notes on amend or rebase unless `notes.rewriteRef` is configured
  ([git-notes](https://git-scm.com/docs/git-notes)). Rewritten commits therefore show no note
  until they are re-admitted, which is correct.
- **Next-push echo.** Lane A prints a one-line summary of unresolved Lane B failures for the
  target ref, for example `remote: 3 unresolved admission errors on main@abc123 (gitctl admission
  show)`. Authors see deferred results in the same channel, without polling.

### 5. Lifecycle gates

A gate is a named precondition on a named transition, such as `ReleaseEligible` on
`CatalogRelease` preparation (ADR 0014), `Ready` on `File`, or `Paid → Fulfillable` on `Order`.
Gates are represented as a status condition on the subject plus, for deletion, a finalizer. They
follow the Kubernetes [finalizer](https://kubernetes.io/docs/concepts/overview/working-with-objects/finalizers/)
rules: removal-only, and a deletion completes once the list is empty. The shared gate vocabulary is:

| Gate kind                    | Satisfied when                                                                           |
|------------------------------|------------------------------------------------------------------------------------------|
| `requiredFields`             | Named fields are present in the admitted object                                          |
| `expression`                 | A CEL expression over the object and its declared references is true                     |
| `coreCondition`              | A named core condition (for example `Ready`) is `True` at the observed generation        |
| `approval`                   | A certificate from an authorized principal exists for the **same material-input digest** |
| `externalAttestation`        | A signed attestation from a registered issuer exists for the same digest                 |
| `allOf` / `anyOf` / `noneOf` | Composition of the above                                                                 |

A gate that is satisfied for one digest is **not** satisfied for another. Any change to the
material inputs invalidates approvals and attestations automatically. 037 uses this vocabulary
without redefining it.

### 6. Event subscriptions

- Events are produced **only from the durable watch journal**, never from the in-memory eventbus,
  the Git service's post-receive, or a request handler. A controller keeps an outbox cursor per
  subscription. Delivery is at-least-once, fenced per subscription key
  ([ADR 0018](0018-controller-ownership-concurrency-and-fencing.md)), and resumes after a restart
  or a rolling upgrade.
- The envelope is CloudEvents 1.0.2. `id` is the journal sequence, `source` is
  `gitstore://<namespace>/<kind>`, `subject` is the resource UID, and `type` is
  `dev.gitstore.<group>.<kind>.<verb>.v1`. Consumers may drop duplicates by `(source, id)`, as the
  spec permits. Payloads stay under 64 KB and carry references, not bodies.
- HTTP delivery is signed per Standard Webhooks (`webhook-id`, `webhook-timestamp`,
  `webhook-signature`, HMAC-SHA256 or Ed25519), with signing material taken from a `SecretRef`
  ([ADR 0001](0001-secretref-reference-contract.md)).
- Exponential backoff, then a per-subscription dead-letter record. A dead-lettered subscription
  never blocks any lifecycle transition. A subscriber that must block something uses a gate.

### 7. Functions (WASM)

- Modules are WebAssembly components with a versioned WIT interface per **target** (for example
  `gitstore:pricing/line-price@1`, `gitstore:catalog/resolve-defaults@1`,
  `gitstore:workflow/transition-guard@1`).
- **Pure and deterministic**: no clock, randomness, network or filesystem. Inputs are declared by
  a GraphQL input query over the subject, as Shopify does, and are materialized by the host. The
  same input produces the same output on every replica, which makes results cacheable and
  replayable.
- Bounded per target by fuel/instructions, memory, input size, output size and wall-clock time.
  Exceeding a bound is a typed failure handled by the target's `failurePolicy`. Exact limits are
  set in 039 and benchmarked before GA.
- Modules are content-addressed (digest-pinned in the `FunctionBinding`), signed per 036's
  artifact policy, and loaded from an OCI or LFS artifact. They are never fetched at call time.

### 8. Service responsibilities

| Service                         | Owns                                                                                                                                                                                         |
|---------------------------------|----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **gitstore-git-service**        | Relaying Lane A diagnostics as `remote:` lines; forwarding push options; excluding and protecting `refs/notes/*`; writing notes only on API command. It holds **no hook registry**           |
| **gitstore-api**                | The registry (Git-admitted registrations, bounded by static safety limits); the admission `Chain`; Lane A and B execution; the decision cache; `AdmissionReport`; the journal; Function host |
| **gitstore-controller-manager** | Gate evaluation and condition patching; event subscription outboxes and delivery; notes projection requests; Function host for controller-side targets                                       |

036's remaining Git-service extension scope is narrowed to Git-protocol and ref-level policy for
T0/T1 operators (ref naming, signed commits, tag protection). This is policy that cannot be
expressed over parsed resources.

## Consequences

Positive:

- One vocabulary covers core resources, CRDs, workflows and integrations across all storage
  groups.
- Git history stays the exact record of desired state, because no server-side mutation touches a
  Git-backed resource.
- Push latency stays bounded. A slow or failing webhook can at worst defer its verdict to Lane B.
- Side effects run only after commit, from a durable, resumable log.

Negative:

- Git-backed authors can't rely on server-side defaulting to fix their files. Diagnostics must be
  good enough to make rejection cheap.
- Lane B admits a ref move before every verdict is in. Readers must respect `AdmissionAccepted`
  and the last accepted generation.
- The API gains a WASM runtime and a registry, and the controller gains outbox delivery. Both add
  capacity and security testing surface.

## Cross-references

- [ADR 0001](0001-secretref-reference-contract.md): webhook and signing secrets.
- [ADR 0008](0008-file-lifecycle.md): File readiness gates.
- [ADR 0010](0010-authorization-model.md): registration, `hookSuspension` and
  `approvalCertificate` actions ([039 §8](../implementation/039-resource-lifecycle-hooks.md#8-authorization-actions)).
- [ADR 0014](0014-catalog-release-and-publication.md): the `ReleaseEligible` gate.
- [ADR 0016](0016-custom-resource-definitions.md): CRDs inherit this matrix through their declared
  storage group.
- Spec 027 (admission contracts), spec 050/055 (watch journal).

## Alternatives considered

### Keep the extension registry in the Git service (036 Phases 1–3)

Rejected as the primary path. The Git service is stateless and has no datastore. Each replica
would need a consistent dynamic registry (036 §18), and resource-level policy would be duplicated
across Rust and Go. The API already parses and admits every resource.

### Allow mutating webhooks on Git-backed resources and write the patch back as a commit

Rejected. Server commits race author pushes, break signature policies, and make reviews describe
content the server didn't store.

### Synchronous webhooks for all tiers, with timeouts

Rejected. Push latency would depend on third-party availability, and T2/T3 code would be on the
critical path of every author.

### A generic scripting runtime (JavaScript/Lua) for Functions

Rejected in favour of WASM. WASM has language-neutral authoring, deterministic metering, and
production precedent in commerce (Shopify Functions).

### Diagnostics only in git notes

Rejected as the source of truth. Notes are optional to fetch, aren't rewritten on rebase by
default, and a notes ref shared by concurrent writers must be merged. They remain a convenience
projection of `AdmissionReport`.
