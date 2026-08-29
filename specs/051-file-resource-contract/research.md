# Research: File Resource Contract

## Decision: Extend the existing parser and admission materialization path

**Rationale:** `ParseResource`, common pre-parse checks, `ValidateResources`,
changed-path selection, and admission comparison already provide the required
error aggregation and author/system field policy. File must be a real
`ParsedResource` branch and a real admission entry; otherwise valid documents
would parse but never persist.

**Alternatives considered:** A parallel parser would duplicate frontmatter and
validation behavior. Treating File as unstructured Markdown would violate the
exact kind contract and leave references unresolved.

## Decision: Add a durable File datastore entity

**Rationale:** Mainline resource entities persist author spec/body separately
from system metadata/status and support namespace/name identity, Git provenance,
finalizers, deletion timestamps, and resource-version transitions. File status
is explicitly datastore-owned, so a parser-only implementation cannot satisfy
the contract.

**Alternatives considered:** A transient or controller-manager-only File cache
would lose status and identity across replicas/restarts. A new storage service or
table family is unnecessary; the existing dual backend abstraction is sufficient.

## Decision: Reuse current shared metadata; defer generateName

**Rationale:** `ObjectMeta` currently has name, namespace, labels, annotations,
and system-managed identity/lifecycle fields, but not `generateName`. File
must include finalizers and deletion timestamp in its hydrated system view and
must not create a divergent metadata type.

**Alternatives considered:** Adding `generateName` only to File would create
contract drift. Extending shared GraphQL/Go metadata is a separate compatible
change and is not required to deliver the 051 contract's implemented fields.

## Decision: Validate conditions per resource kind

**Rationale:** The shared `Condition` shape is reusable, but the existing
validator contains product-specific condition names. File requires the fixed
set `AdmissionAccepted`, `SourceResolved`, `ProcessingComplete`, `Ready`, and
`Terminating` without broadening other resource kinds.

**Alternatives considered:** Globally accepting every condition type weakens
resource contracts. A File-specific condition struct duplicates serialization
and status handling.

## Decision: Reuse generic status/watch contracts

**Rationale:** Mainline already provides generic status updates and resource
watches. File should participate through those contracts with a resolved JSON
projection rather than introduce duplicate typed mutations before File CRUD is
in scope.

**Alternatives considered:** A File-specific mutation/watch API would duplicate
generic semantics and require a larger GraphQL compatibility surface.

## Decision: Keep source work deferred

**Rationale:** This phase defines schema and admission only. Source access,
checksum verification, processing, resolved URLs/variants, uploads, cleanup,
reverse-reference checks, and controller retry/backpressure remain ADR-0008
follow-on work.

**Alternatives considered:** Synchronous source hydration would add external I/O
to admission/read paths and violate bounded, replica-safe service behavior.

## Decision: Preserve the specified initial conditions

**Rationale:** The specification requires `AdmissionAccepted=True` and
`Ready=True` immediately after successful admission, with source and processing
conditions absent. Later controller work may recompute readiness.

**Alternatives considered:** Deferring Ready would contradict the accepted 051
contract even though future lifecycle work may change it.
