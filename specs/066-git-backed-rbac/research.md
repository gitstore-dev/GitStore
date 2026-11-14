# Research: Git-Backed RBAC and Embedded OPA

**Date**: 2026-11-09
**Scope**: Planning decisions; implementation and production acceptance pending.

## 1. Current integration baseline

Decision: extend existing boundaries rather than implement doc 022 literally.

Evidence:

- `gitstore-api/internal/auth/types.go:139-160,218-232`: `Decision`,
  `ResourceContext` and `AuthZProvider.Authorize` already exist.
- `gitstore-api/internal/auth/registry.go:36-86`: optional immutable
  authorization snapshot diagnostics and registry/catalog identities exist.
- `gitstore-api/internal/auth/provider/rbaclocal/policy.go:18-69` and
  `provider.go:235-332`: structured v1beta1 rules, subjects, conditions and
  canonical lookup are the semantic reference.
- `gitstore-api/internal/app/graphql.go:214-223`: Admin GraphQL already uses
  `/admin/graphql`, including WebSockets. The endpoint split is not new work.
- `gitstore-api/internal/admission/committed_manifest.go:15-38,73-77`:
  Git is authoritative and common committed admission projects it.
- `gitstore-api/internal/datastore/entities.go:20-128` and
  `datastore.go:58-82,665-676`: existing metadata, journal, fencing and
  per-kind store conventions; no RBAC resource store currently exists.
- `gitstore-api/internal/watchjournal/readiness.go:17-44`: progress and
  continuity are available, but complete per-tier snapshot acquisition
  must be added.
- `gitstore-controller-manager/internal/types/types.go:23-81`,
  `internal/status/patch.go:55-114` and
  `internal/checkpoint/filesystem.go:180-216`: reuse reconcile/status and
  durable per-kind work, not new controller infrastructure.

Rationale: These contracts shipped after the original spec was written;
existing metadata/errors/diagnostics and Staff classification must remain
the compatibility baseline.

Alternatives: a separate IAM service, resolver-only enforcement or a new
watch channel would duplicate existing ownership and durability mechanisms.

## 2. Embedded OPA

Decision: pin `github.com/open-policy-agent/opa v1.22.2`; use the
`/v1/rego` prepared-query API with a fixed, embedded Rego module. Prepare
policy once per provider runtime, not per manifest or decision. Keep
indexed, immutable tier data separate; publish data, readiness and evaluated
identity together. Use read transactions if mutable OPA storage is chosen;
otherwise pass immutable selected-tier data as query input.

The implementation must allow only deterministic policy built-ins needed by
the fixed module. No `http.send`, filesystem fetch, remote bundles, plugins
or operator module loading. Undefined, malformed or errored output denies.

Rationale: Meets no-network ordinary decisions without creating another
service. Native Rego permits testing the fixed semantics directly.
Prepared policy must not imply a cached decision or stale data binding.

Alternatives: OPA SDK server-management/plugins add unneeded management
machinery; sidecar/remote OPA violates this feature's embedded/no-network
decision boundary; Cedar/OpenFGA are future providers, not implemented here.

Verified upstream references on 2026-11-09:

- [OPA v1.22.2 release](https://github.com/open-policy-agent/opa/releases/tag/v1.22.2),
  published 2026-11-06.
- [OPA Go integration](https://www.openpolicyagent.org/docs/integration).
- [Prepared Rego API](https://pkg.go.dev/github.com/open-policy-agent/opa/v1/rego).
- [Pinned module manifest](https://github.com/open-policy-agent/opa/blob/v1.22.2/go.mod):
  upstream minimum Go 1.26.0; local Go 1.27.2 satisfies it.

### Planning size probe

Measured an isolated module with Go 1.27.2 on macOS/arm64, cross-built for
Linux/amd64, `CGO_ENABLED=0`, `-trimpath -ldflags="-s -w"`. The baseline
prints `true`; the OPA executable imports `/v1/rego`, prepares a fixed
allow query and evaluates an input. Native execution returned `true`.

| Artifact | Bytes |
|---|---:|
| Baseline stripped executable | 1,507,488 |
| Embedded OPA executable | 23,171,232 |
| Difference | 21,663,744 (20.66 MiB) |

This is a linked evaluator probe, **not** the marginal API binary size,
compressed image-layer delta, runtime memory, or latency benchmark. Existing
API dependencies overlap with OPA, and minimal-version selection may update
transitive dependencies. Implementation must measure before/after API
binaries and same-architecture images with identical build flags, review
module changes and run existing SDK telemetry regressions. Image-reduction
work remains #487, separate from this feature.

### Implementation API baseline

Before adding OPA, built the actual API image from `docker/api.Dockerfile`
for `linux/arm64`, at runtime-source revision
`c4874fc0fd30c2f59bcf8b41d40fb06bc1570c08`.
The builder was `golang:1.27.2-alpine3.23`
(`sha256:9e45f0eb4a63ed37ad6e604950407558b7c738e34e015ae77a5d4ad369cde079`);
the runtime was `alpine:3.23.3`
(`sha256:25109184c71bdad752c8312a8623239686a9a2071e8825f20acb8f2198c3f659`).
Both binaries used the shipped `CGO_ENABLED=0 GOOS=linux go build`
commands, without stripping or `-trimpath`. No running deployment was
replaced.

| Pre-OPA artifact | Bytes |
|---|---:|
| `/app/api` | 64,479,538 |
| `/app/gitctl` | 39,047,408 |
| Docker-reported image size | 168,804,961 |

The image size is Docker's reported local size, not compressed registry
transfer size. Preserve the same platform, Dockerfile, base-image digests
and build flags for the linked-OPA comparison; this baseline alone does
not establish the final OPA delta.

Adding OPA v1.22.2 raised the selected versions of gqlparser
v2.5.33 -> v2.5.58, x/time v0.15.0 -> v0.16.0, go-json
v0.10.5 -> v0.10.6 and logrus v1.9.4 -> v1.10.2. The existing
shared-module and Scylla driver replacements were retained. Module
verification and the existing config, app and auth suites passed after
resolution. Prune unused indirect modules when the embedded evaluator is
linked; adding the module alone does not link OPA into the API.

## 3. Translation and lifecycle ownership

Decision: one provider-neutral RBAC controller group owns lifecycle,
finalizers, ownerReferences and managed personal-owner bindings. Each API's
active dynamic provider consumes a common revisioned RBAC model and
translates locally. `rbac-local` remains static, without resource merging.

Rationale: Controller state must not depend on engine-native representation.
OPA data is disposable; Git and datastore projections survive replacement.

Alternatives: one controller per provider, persisted OPA translations or
in-memory-only grants create unnecessary coupling or lost-state risk.

## 4. Authoritative validation snapshots

Decision, explicitly chosen by the user: capture authoritative per-tier
snapshots at validation start. A write may complete after a concurrent
cross-repository revocation. No globally atomic vector, commit-time global
fence, or distributed authorization transaction is claimed.

For each tier, read the current canonical Git tip, require the projected
desired state to match that admitted tip, and acquire a complete stable
projection generation including current lifecycle state. Lag/error returns
retryable unavailability; it never falls back to the ordinary watch cache.
Each captured tier has its own capture time, Git revision, projection
revision, namespace UID, catalog digest and built-in digest. Pin these for
the whole validation. A new candidate after Git conflict starts over.

Rationale: This is the selected tradeoff after identifying that per-repository
Git serialization cannot make a cross-repository check linearizable.

Alternatives: a durable global fence with crash-recoverable Git intents
would enforce a stronger model but was not selected. A last-moment reread
without commit fencing would still race and falsely imply stronger safety.

Access reviews follow the same captured-revision semantics. Ordinary
decisions remain watch-driven with no per-decision datastore/Git reads.

Access-review API decision: adapt the existing TokenRequest-style envelope
for transient `SubjectAccessReview`, not a flat GraphQL decision object.
`authorization.gitstore.dev/v1beta1` request parameters live in `spec`;
the response echoes `spec` and places decision/revision diagnostics in
`status`. `AccessReviewPayload.subjectAccessReview` wraps the result.
Evidence: [the existing token contract](../../shared/schemas/admin/serviceaccount.graphqls)
and [the transient resource catalogue](../../docs/storage-groups/transient.md#authn-and-authz-checks).
The catalogue already identifies this placement, so no new storage group
or persisted review kind is needed. Retain the query, canonical GitStore
actions, authoritative target lookup and scope review permissions.
A distinct self-review API and Kubernetes-specific resource/non-resource
attribute semantics are not introduced.

## 5. Conservative grant coverage

Decision, explicitly chosen by the user: require explicit `bind` or
`escalate` whenever complete permission coverage cannot be proved.

Expand candidate rules against the finite catalog. For each action, a
safe ordinary proof requires a covering caller grant at equal-or-broader
scope, with no principal-dependent condition, and unrestricted resource
names or a superset of every proposed resource name. An unrestricted
candidate cannot be justified by enumerating today's resources. A
conditional candidate can be covered by an unconditional caller grant,
but evaluating an owner condition for the caller alone is not proof of
the grant's behavior for another subject.

Expose this as a bounded optional coverage capability evaluated by the
active provider on the pinned model, alongside ordinary `Authorize`
checks. The fixed OPA module may answer a named coverage query; do not
replace OPA enforcement with a second production decision evaluator.
Providers without coverage evidence require explicit bind/escalate.
Bindings to absent roles require `bind`, remain unresolved and grant nothing.

Rationale: finite actions do not make resource names, subjects or conditional
attributes finite. A concrete successful authorization cannot establish
universal containment.

Alternatives: a general symbolic containment solver is unnecessary scope.
Treating an absent role as an empty grant invites later privilege activation.

## 6. Projection and complete snapshots

Decision: use one logical authoritative RBAC resource collection partitioned
by tier identity, with kind/name clustering, full existing envelopes and a
same-partition revision/count sentinel. Apply an admitted bounded candidate
as a single conditional transaction within the tier. Scylla uses a
single-partition conditional batch; memdb provides equivalent atomicity.
Verify native Scylla batch/CDC behavior in datastore contracts before
depending on it.

A stable paged snapshot reads the revision sentinel before and after the
scan at the required consistency; differing revisions cause bounded retry.
Metadata/status writes participate in the same revision protocol. A CAS
failure cannot publish some resources or advance quota counts. Query-first
secondary indexes remain projections, never the source of the snapshot.

Register full-image CDC in the existing journal. A journal event marks a
tier dirty; consumers rebuild a complete snapshot rather than publishing
partial rows from a multi-resource change. Capture a watch cursor before
the snapshot scan, retain a bounded dirty signal through installation and
verify source progress/continuity. Missing cursors, buffer overflow,
unstable snapshots or lost source progress deny and resync.

Rationale: independent resource pages plus an unrelated journal high-water
do not establish a complete, consistent policy. Tier serialization is a
small bounded domain constraint, not global cross-repository locking.

Alternatives: global scans and incremental publication of incomplete CDC
batches violate readiness. Versioning every full policy snapshot durably
would unnecessarily duplicate reconciled state.

## 7. Admission and Git publication

Decision: add RBAC validation to blocking pre-receive, not the existing
fire-and-forget post-receive callout alone. Validate final candidate trees
and deletions as a unit with authenticated actor and old/new Git OIDs;
revalidate changed authority for a new candidate. GraphQL commits use the
same candidate pipeline.

`gitstore-git-service/src/git/hooks/mod.rs:131-210,280-314` provides blocking
validators. `src/git/hooks/admission_handler.rs:18-20,59-122` currently
always accepts after scheduling its post-receive notification. That
notification is projection triggering, not an RBAC security gate.

Only the canonical branch is authoritative for RBAC. Reject multi-ref
RBAC-changing pushes unless the transport guarantees atomic ref publication;
never report all-or-none acceptance while accepting only some refs. Ordinary
multi-manifest single-ref commits remain supported. Other branches do not
change effective RBAC and cannot shadow protected system paths.

Committed Git remains authority if projection is delayed or an acknowledgment
is lost. Retry projection from the committed OID; never roll back Git
silently, publish part of the tier, or treat a projection timeout as proof
the write did not happen.

## 8. Bootstrap and personal namespaces

Decision: release built-ins are virtual immutable resources with deterministic
IDs and release digest. Keep `namespace-owner -> system:authenticated`;
change `namespace-creator -> system:staff` in line with implemented spec 065.
The initial-admin binding is different: persist an ordinary removable
manifest once, with a durable initialization receipt on existing root
Namespace bootstrap status. Its removal must survive restart.

The RBAC controller provisions fixed-name `namespace-owner-access` in each
USER namespace, tied to the Namespace UID and verified Staff owner. Its
binding points to `namespace-full-control`. The existing enrolled controller
identity receives only the necessary cataloged controller capabilities;
API admission additionally enforces the protected fixed template, actor
classification and UID/version preconditions. No user gets bootstrap bind
or escalate authority. The controller is never allowed to maintain arbitrary
role references or subjects through this path.

Rationale: deterministic identity plus UID fences permits retries and
same-name recreation without silently adopting user-created manifests.
The initial-admin receipt prevents unwanted re-granting after removal.

Alternatives: per-user substitutions in static policy and blindly accepting
caller-supplied managed annotations violate the spec.

## 9. Lifecycle, telemetry and compatibility

Decision: binding role references are immutable; replacement means
delete/create. Namespace owner links are non-blocking; role links are
blocking, once resolved by UID. No reference reattaches to a same-name
replacement role implicitly. Deletion suppresses grants before finalization.
Built-ins have virtual ownership and cannot be deleted.
Cross-tier reference resolution uses a durable per-role UID claim guard;
an eventually updated relation index cannot fence ClusterRole finalization
against a binding in another namespace. Claim/release is idempotent by
binding UID and uncertainty blocks completion until reconciled.

Reuse SDK metrics, bounded decision logging and registry metadata. Add
exact evaluated cluster/namespace revisions, builtin/catalog digests and
bounded failure classes. Do not label metrics by namespace, subject,
resource, commit, cursor or revision. Use finite tier-class/health labels
and bounded diagnostic detail for individual tiers.

Deploy compatible binaries with feature disabled, migrate additively, seed
and verify parity, then perform a controlled provider switch. A static
provider cannot observe new dynamic revocations; prevent dynamic writes
while old/static replicas can still serve.

## 10. Verification and lifecycle status

Decision: extend current test/Makefile families rather than invent a public
RBAC command family. Native topology and offered-load correctness need new
fixtures; current watch/readiness profiles alone are not evidence.

ADR-0010 is already Accepted. ADR-0018 is Proposed; this plan adopts scoped
CAS/ownership practices but does not implement its platform-wide fencing
core, so it remains Proposed. Review that boundary explicitly in the
implementation PR. Spec 066 closes only with its acceptance; #451 and
#487 remain separate work.
