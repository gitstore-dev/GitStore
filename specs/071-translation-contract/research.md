# Translation Research and Decisions

This is the design baseline for [plan.md](plan.md), not a runtime support claim.
Source references describe the isolated branch used for planning.

## 1. Resource envelope and ownership

**Decision**: Select `i18n.gitstore.dev/v1beta1`, kind `Translation`, ordinary
namespace/name metadata, `spec.locale`, `spec.targetRef`, and optional ordered
`patches` with `fieldPath`/`translation`. The Markdown body is the sole body
channel. Resource identity does not include locale or target. Keep common
ObjectReference fields except `fieldPath`, which is rejected here.

**Rationale**: The i18n group expresses a distinct authored concern while
cross-group target references already fit File's separate storage group.
Patches keep each diagnostic attached to an authored entry; one addressing
channel avoids reference-level versus patch-level conflicts. Common source,
namespace and identity conventions fit the existing ingestion lifecycle.

**Alternatives considered**: Embedding locale maps into every target would
couple source ownership and concurrent updates; positional JSON Patch permits
operations beyond text replacement and unstable list identity; a path-to-text
mapping obscures duplicate-key errors. A generic Translation service or a
locale-scoped identity adds no required behavior. #78's envelope was evaluated,
not inherited as an accepted contract. No compatibility alias is introduced.

**Evidence**: `gitstore-api/internal/catalog/product.go:59-67` defines common
reference pins; `internal/catalog/file.go:14-18` demonstrates a separate group.
`internal/cataloggrpc/admission_operations.go:308-354` dispatches typed resource
identity, and `internal/validate/validator.go:216-228` currently rejects unknown
kinds. Translation support must be added explicitly.

## 2. Strict parsing and exact body preservation

**Decision**: Preserve the original blob/source attribution. Validate UTF-8
before YAML conversion. Locate the opening and first closing frontmatter
delimiter using existing accepted LF/CRLF delimiter conventions, retain its
byte offsets, and take the complete suffix after the closing delimiter's line
ending as body. Do not normalize line endings or interpret later delimiters.
Strictly inspect Translation YAML nodes before struct binding: duplicate keys,
unknown members, aliases, merges, non-string text and explicit nulls fail.

**Rationale**: Ordinary YAML struct unmarshalling loses unknown members and
missing/null distinctions. Re-serializing or trimming a parsed body cannot
prove exact preservation. Apply stricter behavior to Translation, not an
unrelated breaking rewrite of every parser.

**Alternatives considered**: Blind reliance on the existing frontmatter
library's body conversion is insufficient without byte tests; rendering,
sanitization and a Markdown IR are explicitly outside this feature.

**Evidence**: `internal/validate/validator.go:55-90,321-359` uses frontmatter and
ordinary YAML binding, with only a File-specific extra check.
`internal/validate/validator_test.go:103,951` does not establish arbitrary CRLF
preservation. Raw bytes already traverse
`shared/proto/gitstore/catalog/v1/catalog_service.proto:95-124` and
`gitstore-git-service/src/git/hooks/validation_handler.rs:158-190`; no new
Translation protobuf or independent Rust schema is necessary.

## 3. Locale validity versus a language registry

**Decision**: Accept the complete well-formed RFC 5646/BCP 47 grammar, including
private-use, extension and grandfathered forms. Reject whitespace, underscores,
empty subtags, duplicate variants/extension singletons and malformed sequences.
Syntactic well-formedness is authoritative, not membership in x/text's registry.
Use pinned `golang.org/x/text/language` canonicalization for fully recognized
tags; if it reports an unknown registry value for an otherwise well-formed tag,
retain every subtag and normalize its case deterministically instead of
substituting `und` or dropping data. The comparison key is the ASCII-lowercase
canonical form. Preserve the authored locale separately.

**Rationale**: `language.Parse` alone can reject well-formed unregistered
subtags or accept input spellings the spec forbids. A bounded grammar gate plus
lossless handling preserves the required acceptance domain. Canonicalization
is versioned with the validator descriptor; registry upgrades require fixture
review and invalidation, not silent change to identity.

**Alternatives considered**: A small `ll-RR` regex incorrectly excludes scripts,
extensions and private use. Requiring a Locale/market resource changes scope.
Case comparison does not imply fallback, merging, uniqueness or winner choice.

**Verification**: Include `de-de`/`de-DE`, script/region/variant combinations,
`x-private`, extension sequences, grandfathered tags and syntactically valid
unknown language/script/region subtags, plus duplicate variants/singletons,
truncation, `en_US` and surrounding whitespace. x/text is already pinned
indirectly in `gitstore-api/go.mod`.

**Implementation placement (approved)**: Canonical locale, path, descriptor
and field validation belongs in the repository-local `shared/translation`
Go module. Both services import those pure semantics; API metadata decoding,
authorization and persistence and controller lookup/checkpoint I/O remain
local. This avoids importing another module's `internal` packages and avoids
a new authenticated validation operation. Pin x/text directly in this module;
include standalone `GOWORK=off`, aggregate and consuming-container checks.

## 4. Explicit text and list descriptors

**Decision**: A small immutable descriptor per target GVK/version declares
eligible textual leaves, text constraints, body support, list identity
extractors and a descriptor digest. It is not generic reflection-based
permission to translate all strings. Current Product options use `name`.
Media slots use their declared File-reference name for selection, but contain
no eligible text today. Future keyed options use explicit `key` descriptors;
there is no runtime `name`-then-`key` fallback.

**Rationale**: Collection identity, display labels and reference identity are
different. A selector may be grammatically correct yet end on an ineligible
object/reference. Descriptors allow nested keyed choices without depending on
the whole of #492 or silently traversing its `definitionRef`.

**Alternatives considered**: Numeric indexes change meaning on reorder;
heuristic identity guessing is ambiguous; generic string patching exposes
SKUs, URLs, credentials, lifecycle and policy. Full custom schema discovery
before CRDs exist would be an unrelated framework.

**Evidence**: `internal/catalog/product.go:42-47,70-92` has title, media
`fileRef`, option `name`/`title` and string-valued choices.
`internal/catalog/product_variant.go:18-27` separates title from SKU,
references and selected options. `internal/catalog/category.go:16-20` and
`collection.go:16-21` expose title. File has no title
(`internal/catalog/file.go:22-28`). Exact eligibility is in the
[authored contract](contracts/authored-resource.md).

## 5. Persisted evidence is not an atomic freshness guarantee

**Common status baseline**: Retain #40's `conditions`, `observedGeneration`,
`lastAppliedRevision` and kind-specific `resolved`, matching existing resource
status and generic status mutation interfaces. `lastAppliedRevision` is the
Translation source revision successfully reconciled into its observed status,
including an unresolved/invalid target observation; it is not target
application evidence. Keep accepted source attribution independent of this
controller progress marker. Do not replace it with `observedRevision`.
Omit only the application-specific `resolved.appliedFieldPaths` and
`ProjectionUpdated` proposal because this feature produces no localized target.

**Decision**: Controllers write observed validation evidence, not an eternal
current assertion. Persist the target UID, authored generation, source/content
digest, descriptor digest and any explicit resourceVersion pin. Before any
external representation advertises current Ready, a shared API resource
service performs bounded authoritative metadata reads and a Translation
version recheck. A per-target durable write fence brackets material mutations
and authoritative metadata reads, including deletion/name reuse. On mismatch,
forbidden lookup, I/O failure or exhausted retry
budget it must suppress Ready, never reuse a positive cache.

**Rationale**: Target-read followed by Translation-status CAS leaves a race:
the target can change between those operations. Translation CAS fences only
the Translation. The service guard verifies evidence at a point within the
read operation; it does not promise that a target cannot change after that
point. Point, list, Node, generic status and watch delivery share this rule.
Watch history is historical evidence, not a permanent current-state claim.

**Alternatives considered**: Eventual invalidation alone has a false-Ready
window. A catalog-wide lock or cross-resource transaction coordinator creates
hotspots and a new framework. The selected fence is per target identity and
contains no dependent list or cross-resource transaction; interrupted target
writes are recovered by their durable intent. Live path resolution or dependency aggregation
inside GraphQL violates the materialization boundary. A metadata-only guard
does neither: controllers still compute all validation and summaries.

**Evidence**: Existing typed CAS methods are in
`internal/datastore/datastore.go:443-464,537-575`; explicit status routes in
`internal/admin/graph/resolver/status_generic.go:43-85,185-223` are not generic
support for every kind. The exact guard, consistency requirements and
deterministic race cases are in
[lifecycle-and-storage.md](contracts/lifecycle-and-storage.md).

## 6. Durable dependencies, binding and bounded recovery

**Decision**: Add a typed Translation store, authoritative watched row and thin
namespace/name/target indexes. Target-name dependencies wake initially missing
targets; UID dependencies invalidate resolved incarnations. Persist first
resolved UID against the authored reference epoch; a same-name replacement
requires explicit new-UID retargeting. Derived indexes use recoverable
maintenance and audit/repair, not process-local correctness maps. Durable
fan-out cursors advance only after work is durably enqueued.

**Rationale**: Source can arrive before the target, and millions of Products
cannot be rescanned to discover dependents. Restart must not erase a sticky UID
or a partially drained invalidation. An index is neither ownership nor an
authorization grant.

**Alternatives considered**: Reusing owner references changes deletion
semantics; a memory reverse map loses work on restart; synchronously
revalidating all dependents on a target write creates unbounded push latency.

**Evidence**: `internal/datastore/scylla/catalog_cdc.go:25-57` is the namespaced
CDC registration seam. `scylla/owner_references.go:29-76` is an indexing
precedent, not Translation ownership. Production controller reuse is defined
in `gitstore-controller-manager/AGENTS.md`; `internal/checkpoint/filesystem.go:177-215,577-669`
provides disk-backed work. Existing category File resolution is a stub
(`categorytaxonomy/fileref.go:9-38`), not a reusable complete target resolver.

## 7. Related-resource and historical-content boundaries

**Decision**: Direct target plus owned inline fields is the complete dependency
closure needed by this feature today. Do not traverse Product category, media,
variant or shared-definition references to discover more translatable text.
Shared-definition text is translated on the defining resource once that kind
ships. Product-local override requires its own explicit future contract.

**Rationale**: Product-owned inline labels and independently shared labels have
different identity, permissions and lifecycle. #492 informs descriptors and
extraction without becoming an implementation prerequisite.

**Alternatives considered**: Implicit dereferencing would bypass defining
resource authorization, require unbounded dependency traversal and conflate
sharing with ownership. Broad localized Product projection belongs to a later
feature with a bounded dependency-vector contract.

**Handoffs**:

- #492 owns stable option/value keys, explicit offered membership, variant
  combination identity and extraction. Translation never rewrites those keys.
  [data-model.md](data-model.md) gives the extraction and stale-dependency cases.
- A future localized materializer owns override/winner selection and fallback.
  It must track every shared UID/generation/digest it consumes even when the
  Product generation is unchanged. Any summary is asynchronously materialized.
- Proposed ADR 0014 owns release closure, readiness policy and immutable
  snapshots: capture exact Translation UIDs/revisions/content and dependencies,
  locale policy and descriptor/conversion digests. Later edits cannot rewrite
  a published snapshot; readers cannot reconstruct it from mutable rows.
- Proposed ADR 0016 owns custom-kind discovery and explicit text/list identity
  capabilities. Visibility or publication eligibility is not translation
  eligibility. Unsupported schemas/versions fail closed today.
- There is no recursive reference walk today, hence no new cross-resource
  cycle is created. Reference traversal attempts fail at the first boundary.
  Future traversal must define depth/work ceilings, UID-based visited sets,
  deterministic cycle diagnostics and bounded missing/stale outcomes before
  activation; this plan does not implement that traversal.
