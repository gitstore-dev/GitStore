# Research: Namespace Validation and Admission Matrix

## Decision: Use two ordered phases with distinct structural reasons

**Decision**: Evaluate structural/pre-receive validation before stateful policy
admission. Classify immutable-name rejection as a distinct machine-readable
reason within the structural phase, not as a third phase.

**Rationale**: The existing code already has parser/schema checks, old/proposed
blob comparison, and datastore-backed admission checks, but executes some of
them in overlapping paths. Making the phases explicit satisfies FR-001 through
FR-003 and prevents a stateful policy error from obscuring a malformed request.

**Alternatives considered**: A third immutable phase would conflict with the
specification's two request-time phases. Treating immutable changes as an
ordinary undifferentiated structural constraint would not satisfy FR-003.

## Decision: Keep the protobuf shape and define stable constraint codes

**Decision**: Continue using `ValidationError.file_path`, `field`,
`constraint`, and `message`. Structural constraints retain their concrete rule
(`required`, `read-only`, `reserved`, and similar); immutable changes use
`immutable`; policy failures use stable `policy/<reason>` values.

**Rationale**: The existing Git-service hook already transports and displays
these fields. A new protobuf field would add generated-code and mixed-version
coordination without adding information the current contract cannot carry.

**Alternatives considered**: Adding a `phase` enum to the protobuf is clearer in
isolation but unnecessary for the required distinctions and creates avoidable
rollout work across API and Git-service replicas.

## Decision: Use GraphQL error extensions for rejected operations

**Decision**: Create/update/delete rejections use `gqlerror.Error` extensions
with a stable category code and reason. Structural, immutable, policy, and
deletion-precondition errors remain GraphQL errors, not success payloads.

**Rationale**: The repository already uses `extensions.code` for
machine-readable authorization failures. Extending the same mechanism preserves
human-readable messages while allowing clients to branch without string
matching.

**Alternatives considered**: Adding a union result for every Namespace mutation
would be a much larger breaking client change. Message-only errors do not meet
the distinguishability requirement.

The `BOOTSTRAP_NAMESPACE` create/update rejection is existing lifecycle behavior
owned by spec 046. This feature only assigns it a stable policy reason; it does
not expand the reserved-identifier blocklist or add a new admission rule.

## Decision: Correlate immutable name changes by repository path

**Decision**: Reject `metadata.name` changes when an old and proposed Namespace
manifest occupy the same repository path. Do not infer a rename when both path
and name change.

**Rationale**: Namespace manifests contain no stable author-controlled UID.
Same-path comparison is deterministic and blob-only; a path-and-name change is
indistinguishable from deleting one declaration and creating another.

**Alternatives considered**: Pairing arbitrary removed and added Namespace
manifests would misclassify legitimate multi-resource pushes. Adding a writable
stable identity is outside this feature.

## Decision: Add an explicit successful deletion outcome

**Decision**: Add `NamespaceDeletionOutcome` with
`TERMINATION_STARTED` and `ALREADY_TERMINATING`, returned by
`DeleteNamespacePayload.outcome`.

**Rationale**: An already-terminating delete is explicitly not a rejection, but
the current payload only returns the identifier and cannot distinguish it from
a new deletion request. An additive enum field is the smallest honest contract.

**Alternatives considered**: Returning a GraphQL error would contradict the
idempotent lifecycle contract. Inferring the result from a follow-up query is
racy and forces an extra request.

## Decision: Aggregate delete blockers after the idempotency check

**Decision**: Reload the authorized Namespace, return
`ALREADY_TERMINATING` first, then independently evaluate bootstrap and
non-empty blockers. If one or both apply, return a precondition error whose
extensions contain all stable blocker reasons.

**Rationale**: The current bootstrap-first return prevents reporting the
non-empty blocker. Checking already-terminating first preserves spec 046's
idempotent no-op and avoids unnecessary repository reads for repeated deletes.

**Alternatives considered**: Fail-fast blocker evaluation violates FR-007.
Persisting blocker conditions would duplicate transient request-time decisions
in Namespace status and is not required by the specification.

## Decision: Recheck policy at the conditional write boundary

**Decision**: Preflight policy checks improve caller feedback, but
`ApplyManifest` and deletion writes remain authoritative and repeat relevant
state checks before expected-resource-version mutations.

**Rationale**: Two API replicas may evaluate the same Namespace concurrently.
Durable rechecks and existing resource-version conflicts prevent stale policy
decisions from becoming successful writes.

**Alternatives considered**: Trusting only the pre-receive/preflight result
creates a time-of-check/time-of-use gap. Process-local locks do not coordinate
replicas.

## Decision: Converge stale Namespace work from the current Git head

**Decision**: When the authoring ref has advanced to a descendant, read and
parse the Namespace manifest at the current head and the changed Namespace
path, then conditionally admit that latest content. Retry if the ref advances
again. Stale non-Namespace paths remain skipped.

**Rationale**: Requiring exact `HEAD == submitted commit` can permanently omit
Namespace X when descendant commit B changes only Namespace Y. Per-path
convergence materializes both X and Y, while reading the current head ensures a
newer same-resource manifest wins and an ancestor can never overwrite it.

**Alternatives considered**: Replaying every ancestor delta requires commit
graph traversal and duplicate ordering logic. Blindly admitting the stale
manifest preserves disjoint changes but can overwrite newer same-resource
content.

## Decision: Persist and version the complete authored Namespace

**Decision**: Store API version, kind, labels, annotations, the full
`NamespaceSpec`, Markdown body, and Git path/SHA/ref provenance. Compare every
authored field when deciding generation changes. Provenance-only changes advance
resourceVersion but not generation.

**Rationale**: Git and GraphQL are declarative authoring surfaces. Omitting
accepted fields from the candidate makes reads lossy and prevents controllers
from observing changes to repository or push-policy defaults.

**Alternatives considered**: Keeping only denormalized title/tier is smaller but
breaks the resource contract. Advancing generation for ref movement alone
incorrectly signals a desired-state change.

## Decision: Share Namespace identifier validation

**Decision**: Put DNS-label and reserved-identifier validation in
`internal/namespace` and call it from GraphQL conversion and Git parsing.
Bootstrap identifiers remain structurally valid and are rejected by the
existing policy rule.

**Rationale**: Two private rule sets drift and let malformed or reserved Git
manifests bypass GraphQL's contract. A shared helper guarantees parity without
collapsing the structural-versus-policy distinction.

**Alternatives considered**: Duplicating the map in catalog gRPC is easy but
does not prevent future drift. Treating bootstrap names as structurally
reserved would change the stable `BOOTSTRAP_NAMESPACE` policy outcome.

## Decision: Use a durable repository lifecycle fence

**Decision**: Add `CreateRepositoryInActiveNamespace` and
`MarkNamespaceDeletion` datastore operations. memdb performs each decision and
mutation in one write transaction. Scylla stores an internal
`repository_creation_epoch` and `pending_repository_creations` on
`namespaces_by_uid`: repository creation increments both by LWT while
`deletion_timestamp` is null, then decrements only the pending counter on
completion. Deletion marks termination by LWT only when the expected
resourceVersion, observed epoch, and zero pending count still match.

**Rationale**: The pending counter covers in-flight creates, while the monotonic
epoch detects a create that both starts and completes between the emptiness read
and deletion LWT. If deletion wins, the creation reservation fails. No
repository-delete bookkeeping is needed, which keeps legacy rows and repository
transfers from decrementing the wrong reservation.

Both columns are nullable for rolling upgrades. Null legacy values are treated
as the zero pre-fence state for the first conditional reservation/mark, while
the existing indexed `HasRepositories` check continues to block deletion for
pre-fence repository rows. Failed or completed creates release the pending
reservation; failed release is surfaced as repair-required rather than risking
unsafe deletion.

**Alternatives considered**: A process-local lock cannot coordinate replicas.
An emptiness read followed by a resourceVersion update leaves the original
race. A lease row requires expiry/recovery semantics. An epoch without a
pending counter misses an in-flight create; a current repository count requires
correct decrement bookkeeping across legacy rows and transfers.

Repository transfer into a target Namespace uses the same reservation. memdb
moves the mapping and authoritative row only after checking the target in the
same write transaction. Scylla reserves the target's epoch/pending fence before
the transfer saga and releases it only after the authoritative row and
projections are committed. Leaving transfer outside this protocol would allow a
target Namespace deletion to complete while a repository is moving into it.

## Decision: Gate production activation and retain forward migrations on rollback

**Decision**: Add
`GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE=auto|disabled|enabled`.
`auto` enables memdb development behavior and disables Scylla production
behavior. Production rollout first applies a fleet-wide ingress/AuthZ deny for
the four unsafe mutations, applies migration 005, deploys the entire new API
fleet with the gate disabled, then enables the gate everywhere before removing
the deny. Rollback artifacts may revert behavior but retain migrations 001-005.

**Rationale**: A new binary-local gate cannot stop a legacy replica that does
not know the setting. The external deny makes the mixed window safe; the local
gate prevents accidentally activating a partially upgraded new fleet. gocqlx
validates that every applied migration exists in the binary, so an arbitrary
001-004 binary honestly cannot boot after 005.

**Alternatives considered**: Silently teaching the migration runner to ignore
unknown applied migrations would discard checksum/history protection. Enabling
the gate replica-by-replica without a fleet-wide deny would allow old replicas
to bypass the fence. Claiming arbitrary older binaries are rollback-safe is
false because gocqlx rejects a database whose migration history is ahead.

## Decision: Reuse existing status ownership

**Decision**: Admission owns `AdmissionAccepted`; the Namespace reconciler owns
`SystemRepoReady` and `Ready`; `Terminating` remains derived from lifecycle
fields. Add tests for condition type and observed generation, but no new status
column or writer.

**Rationale**: Spec 046 already implemented the correct durable status model.
Spec 047 documents and verifies the matrix rather than duplicating lifecycle
behavior.

**Alternatives considered**: Persisting validation failures as status would
overwrite the last accepted state with rejected input that never became the
resource's current configuration.

## Decision: Preserve bounded work and existing authorization

**Decision**: Structural/immutable phases are blob-only. Policy checks use keyed
Namespace reads, and deletion uses the indexed `HasRepositories` existence
check. GraphQL authorization remains before service validation.

**Rationale**: This keeps the feature safe at production catalogue scale and
prevents clearer error reporting from becoming an authorization side channel.

**Alternatives considered**: Listing repositories or namespaces to determine
policy would violate query-first capacity requirements. Moving checks ahead of
authorization could leak resource state.

## Decision: Use server-first GraphQL rollout

**Decision**: Keep `outcome` non-null on new API replicas, deploy the API fleet
before clients select it, and roll back clients before API replicas.

**Rationale**: Existing GraphQL selections remain valid on old and new replicas.
A new client selecting a field cannot be compatible with an old schema, so
client activation must wait for fleet convergence.

**Alternatives considered**: Making `outcome` nullable does not make the field
exist on old replicas. Versioning a second mutation would add unnecessary API
surface.

## Decision: Set measurable capacity objectives

**Decision**: Validate 500-file requests with at most 50 Namespace manifests at
10 requests/second and concurrency 20 across two replicas for 30 minutes, with
p95 ≤100 ms, p99 ≤250 ms, internal errors <0.1%, zero wrong decisions, recovery
within 30 seconds after one replica replacement, CPU <80%, retained-memory
growth <10%, and post-soak goroutines within 5% of baseline.

**Rationale**: These thresholds make the constitution's latency, throughput,
error, recovery, and saturation requirements executable while retaining bounded
work.

**Alternatives considered**: A duration-only soak cannot determine whether the
feature is production-ready.

## Clarification resolution

Immutable-name validation remains in the structural/pre-receive phase with its
own reason; tier demotion remains a stateful policy rule. No unresolved
`NEEDS CLARIFICATION` remains.
