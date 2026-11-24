# Feature Specification: Publication Authority and Immutable Catalog Releases

**Feature Branch**: `codex/076-publication-authority`

**Created**: 2026-11-23

**Status**: Draft

**Input**: Priority 1 of the Storefront dependency sequence: implement the publication authority proposed by ADR 0014 and issue #132. Separate private admission from immutable public release snapshots; define trusted target binding, activation, scheduling, replacement, rollback, withdrawal and suppression. Supply the authoritative read boundary consumed by specs 077/078 and then 073.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Prepare a Public Release Without Changing Private Admission (Priority: P1)

A merchant selects an admitted catalog revision and explicit resources for release. Preparation captures their exact public representations and required dependencies while normal draft editing continues independently.

**Why this priority**: A public catalog needs an immutable, reviewable source before a target can safely serve it.

**Independent Test**: Prepare a selected Product/Variant fixture at revision A, advance private authoring to B and inspect the prepared snapshot. No live Storefront schema or search engine is required.

**Acceptance Scenarios**:

1. **Given** an admitted source revision and explicit selection, **When** preparation succeeds, **Then** the release pins the revision, selected identities, public serializer versions and complete bounded required dependency closure.
2. **Given** a prepared release A, **When** private resources, translations or definitions change to B, **Then** A retains its original content, identities and digest.
3. **Given** missing dependencies, unproven admission, unsatisfied required gates, unsupported public serialization or excessive selection, **When** preparation runs, **Then** it fails closed with actionable diagnostics rather than copying current private rows or exposing secrets.

### User Story 2 - Activate One Trusted Snapshot Per Publication Target (Priority: P1)

A merchant publishes a prepared release to a store/channel/market target, immediately or on schedule. A buyer's trusted Storefront identity selects only an authorized target; publication never depends on an arbitrary namespace supplied in a query.

**Why this priority**: This is the authority needed to replace the existing unavailable Storefront boundary safely.

**Independent Test**: Bind two target fixtures to different prepared snapshots and test authorized/anonymous binding, concurrent activation, missing binding and target isolation through an internal published-read fixture.

**Acceptance Scenarios**:

1. **Given** two API/controller replicas and a ready release, **When** they race activation of the same target, **Then** readers observe one complete authorized snapshot and stale workers cannot overwrite the winner.
2. **Given** target A's identity, **When** a request supplies target B's namespace/channel/market or forged forwarding metadata, **Then** no B data, identifiers or derived metadata is disclosed.
3. **Given** a future activation or unavailable/untrusted target, **When** a buyer reads, **Then** the target serves no premature data and never falls back to private Admin rows.

### User Story 3 - Replace, Withdraw and Roll Back Without Mutating Release History (Priority: P2)

A merchant replaces a live release, rolls back to an earlier prepared release, or withdraws public content. Scheduled expiry remains effective even if controllers are delayed.

**Why this priority**: Safe launch and takedown must be established before public caches or search indexes rely on publication.

**Independent Test**: Activate A, prepare B, inject replacement failures, perform rollback through another Publication, and stop controllers across an activation/expiry boundary.

**Acceptance Scenarios**:

1. **Given** active A and an authorized replacement B, **When** the switch succeeds, **Then** each request sees one complete snapshot; failed preparation/switch leaves A serving only within its own valid window.
2. **Given** an earlier retained release, **When** rollback is requested, **Then** a new Publication selects that immutable release without moving a release tag or rewriting the snapshot.
3. **Given** an expired, withdrawn or emergency-suppressed target/resource, **When** controllers or derived indexes lag, **Then** new reads still enforce current visibility and no implicit predecessor becomes public.

### User Story 4 - Diagnose and Recover Interrupted Publication Work (Priority: P2)

An operator can distinguish preparing, scheduled, active, failed and withdrawn work, resume interrupted preparation/activation, and determine why publication is unavailable without inspecting private storage manually.

**Why this priority**: Publication is an operational authority, not a best-effort flag on current catalog resources.

**Independent Test**: Interrupt preparation, tag verification and pointer replacement at defined boundaries; inspect status and recover using another authorized replica with preserved storage.

**Acceptance Scenarios**:

1. **Given** a crash or lost response, **When** recovery resumes, **Then** the same operation is verified or completed idempotently without publishing a partial snapshot or claiming a failed switch succeeded.
2. **Given** a matching, missing or conflicting release tag, **When** activation verifies evidence, **Then** only the pinned admitted commit can activate and a conflicting tag is never overwritten.
3. **Given** obsolete source/projection versions or unsupported gates, **When** activation is attempted, **Then** readiness remains explicit and unavailable rather than bypassing the missing contract.

### Edge Cases

- Same-name resources, repositories or targets are deleted/recreated; release selection must bind stable incarnations.
- Selecting a Variant includes its Product presentation, not every sibling Variant; selecting a Product does not silently select all its Variants.
- Required dependency cycles, cross-namespace references, missing translations/media, private credential references and unsupported custom kinds occur in the pinned source tree.
- A tag moves or disappears after observation; a scheduled activation races expiry, suppression, cancellation or replacement.
- A stale controller holds a lease but tries to write after ownership loss; uncertain external effects cannot be erased by resetting status.
- A continuation/cache entry outlives target replacement, read-time expiry or suppression; immutable retention is not continuing public authorization.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Keep admission/private current state and publication/public visibility separate. No source Published flag, branch alias or tag admission may substitute for target-scoped immutable authority.
- **FR-002**: Define CatalogRelease and Publication as Git-authored desired resources with canonical apiVersion/kind/ObjectMeta/spec and shared status/Condition/resolved. System snapshots, tag observations, operation receipts and target pointers are runtime-owned; no alternate metadata/status contract.
- **FR-003**: Resolve and pin the selected admitted source commit at acceptance, with verifiable admission provenance and explicit source repository/ref identity. A controller cannot resolve a moving branch later and label it the original release.
- **FR-004**: Require bounded nonempty explicit selection and capture stable selected resource identities. Define Product/Variant inclusion independently; preserve namespace/repository boundaries and reject unintended whole-catalog expansion.
- **FR-005**: Build an immutable public snapshot from the pinned source and required dependency closure. Record source/definition/serializer/body/translation versions and digests needed to reproduce it; never reconstruct historical content from mutable Admin rows.
- **FR-006**: Define the initial supported built-in kind/operation matrix. Enforce every applicable required release gate against the pinned candidate and current accepted evidence at commitment. Unsupported custom serializers, gates or dependencies fail explicitly; the complete CRD/App/WASI platform is not an implicit prerequisite for supported built-ins.
- **FR-007**: Reuse implemented Translation identity/freshness/snapshot rules from spec071. Required localized/media/body material must be pinned or preparation fails. ADR0013/078 own the detailed public body representation; unresolved interpolation/private references must never escape through raw source.
- **FR-008**: Prepare before activation/tag creation where possible. Verify registered tag evidence against the pinned commit at activation; a matching existing tag is idempotent and a conflicting tag is never moved. Signature policies retain their owning authority.
- **FR-009**: Use bounded authoritative Git tag verification before activation until #139's replayable source exists. Source074 may improve observation/recovery, but its entire Events/Audit/Svix stack is not required and event arrival alone never authorizes publication.
- **FR-010**: Establish trusted Storefront binding to namespace/store identity and permitted publication targets using server-validated credentials/configuration. Anonymous access requires an explicit trusted binding. Client context may only select within that authority; untrusted Host/forwarded headers, IDs and namespace arguments cannot create authority.
- **FR-011**: Define a target by namespace incarnation, channel and market; allow at most one active publication per target. Preserve requested activation/expiry windows and explicit predecessor/replacement identities. A target's absence/unavailability never broadens to another target.
- **FR-012**: Activation and replacement must atomically expose one complete prepared snapshot at the target authority, with expected predecessor/version checks and target-enforced stale-writer rejection. Leases alone are insufficient and cross-record status updates cannot be assumed transactional without proof.
- **FR-013**: Enforce activation/expiry on the read authority independently of controller scheduling. Reads beginning at/after effective expiry fail closed. Define bounded request-time and continuation validity; a publication change cannot silently mix snapshots or continue serving withdrawn content.
- **FR-014**: Define first publication, scheduled replacement, failed replacement, withdrawal, identity removal and rollback. Rollback creates a new Publication selecting a retained prepared release; no immutable tag moves or snapshot mutation. Expiry/withdrawal serves nothing unless a separately authorized publication explicitly becomes active.
- **FR-015**: Provide separately authorized, bounded emergency suppression independent of private retirement and controller lag. Apply it to lookup, relationships, listing, search-derived metadata and caches. Historical snapshot retention does not override suppression.
- **FR-016**: Expose bounded authorized preparation/publication status and diagnostics, with asynchronously materialized progress/aggregates. Keep authority at owning API operations, controller coordination on existing GraphQL HTTP/WS and Git primitives on the existing signed native RPC boundary. Reuse core authorization verbs/subresources rather than invent publication-specific verb families without necessity.
- **FR-017**: Make preparation/activation/suppression/cleanup operation identity and recovery enumerable and idempotent across API/controller replicas. Persist required work before acknowledging progress; preserve pending snapshots/evidence and explain source gaps/uncertainty rather than silently recreating history.
- **FR-018**: Define safe snapshot/input/index retention and reference-aware cleanup. Never delete a snapshot still required by active authority, accepted preparation or an allowed rollback/continuation window. Logical withdrawal is not a physical-erasure guarantee.
- **FR-019**: Supply specs077/078/073 a versioned publication-read authority: trusted target identity, immutable snapshot/public serializer revision, effective time bounds and current suppression/eligibility checks. Derived search indexes are candidates only; no private hydration or unsafe facets/counts.
- **FR-020**: Reconcile issue132's live-projection/rollback wording and ADR0014's identity, atomicity and source assumptions in planning. Preserve ADR0012's endpoint isolation and existing error conventions. Do not accept ADR0014 or close implementation issues merely by writing this spec.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Focused implementation tests cover two API/controller actors, stale target writes, lost acknowledgements and version compatibility. Production qualification adds actual concurrent replicas/rolling upgrades. Git remains one retained active instance with non-overlapping replacement.
- **PR-002 Multi-User Security**: Exercise two namespaces/targets, anonymous/ordinary/admin/service identities, current revocation and trusted-binding forgery. No unauthorized source, target, snapshot, suppression or derived-metadata disclosure; use pluggable AuthN/AuthZ and receiver-bound signed RPCs.
- **PR-003 Capacity**: Proposed production qualification uses >=5M products/10K categories, 30-minute 100 public reads/s with concurrent private authoring, and separate 10K-selected-resource snapshot probes. Target-bound read selection p95/p99 <=250 ms/1 s and unexpected errors <1%; no partial-publication outcomes. State actual data closure/bytes/topology and retain stricter owning gates; these are unmeasured goals.
- **PR-004 Backpressure**: Planning assigns finite selection/closure bytes/depth/count, dependency fan-out, preparation concurrency, retry/deadline, tag-read, pointer-operation and cleanup budgets. At twice offered load, reject/throttle before new acceptance or retain accepted obligations; unrelated private authoring must remain usable.
- **PR-005 Capacity Evidence**: Reuse make capacity TARGET=api PROFILE=endpoint-split, TARGET=namespace PROFILE=watch and TARGET=repository PROFILE=lifecycle with existing modes. New publication probes are proposed. Separate focused implementation/PR readiness and full make pr-ready from deferred production capacity/soak qualification.
- **PR-006 Fault Recovery**: Focused crash-cut fixtures precede implementation. Production qualification uses owned api-restart/controller-restart/journal-interruption where applicable plus non-overlapping Git replacement. Healthy reconciliation resumes <=60 s after dependencies recover; measure backlog drain separately, preserving snapshots/source data and unrelated evidence.
- **PR-007 Recovery and Integrity**: Test publication/expiry/replacement/suppression boundaries with zero partial snapshots, stale overwrites, private fallbacks or erased accepted obligations. Deferred deployed fault/upgrade/load evidence must remain labeled deferred and precede production claims, rather than block every alpha implementation slice.

### Key Entities *(include if feature involves data)*

- **CatalogRelease / Snapshot**: Explicit selected admitted revision and its immutable public content/dependency manifest with reproducibility evidence.
- **Publication / Target Authority**: Authorized release-to-store/channel/market activation window and guarded active snapshot selection.
- **Trusted Storefront Binding**: Server-established namespace/target authority, independent of client filters or response IDs.
- **Suppression**: Current narrowly authorized visibility override over retained public snapshots.
- **Preparation/Activation Receipt**: Recoverable operation identity, ownership and verified outcome, distinct from observational status.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: In every private-A-to-B edit fixture, prepared/public A retains exact pinned content and identities with zero mutable-source hydration.
- **SC-002**: All target-forgery and two-namespace tests disclose zero unauthorized public/private objects or derived metadata.
- **SC-003**: Every concurrent/failed activation fixture exposes exactly one complete permitted snapshot or explicit unavailable state, with zero stale replacement wins.
- **SC-004**: Scheduled activation, expiry, withdrawal and suppression fixtures enforce visibility despite stopped controllers; rollback never moves an immutable tag.
- **SC-005**: Missing/oversized/cyclic dependencies, unknown gates and conflicting tag evidence are rejected explicitly in every corresponding fixture.
- **SC-006**: Accepted preparation and transition work survives the defined crash/lost-response cases with no invented success or deleted required evidence.
- **SC-007**: Production claims require the full declared healthy/overload/replica/recovery goals; implementation reports distinguish focused passing checks from deferred deployed qualification.
- **SC-008**: Public-read fixtures expose the target/snapshot/visibility boundary required by077/078/073, with canonical metadata/status and no alternate publication source.

## Assumptions, Dependencies and Scope

- Base is origin/main86b1c15e, including implemented Translation spec071. Storefront's existing handler remains unavailable; this spec does not claim working published reads.
- Owns implementation requirements for [#132](https://github.com/gitstore-dev/GitStore/issues/132), following [ADR0014](../../docs/ADRs/0014-catalog-release-and-publication.md). Closed #314 is design history, not runtime completion. ADR statuses change only with implementation of their core decisions under the repository lifecycle rules.
- Priority sequence:076 publication authority ->077 context ->078 public read types ->073 cross-surface query alignment. Sibling specs are separate branches, referenced by feature number rather than nonexistent local relative links. They may be designed together; implementation readiness follows their actual supported prerequisites.
- Publication does not freeze inventory, buyer entitlements or request-time price selection. Specs077/078 define their trusted evaluation/disclosure, while snapshots freeze authored templates/content and exact dependencies. No currency conversion or mandatory inventory is implied.
- Full CRD/App/function execution, external provider implementations, cart/checkout, frontend delivery and recommendation serving are out of scope. Applicable declared gates cannot be bypassed to achieve this narrower initial slice.
- This invocation produces requirements/checklist only; concrete SDL/RPC/storage/atomicity protocols, default size budgets and task sequencing belong in speckit-plan. No code, live faults, issue edits, ADR acceptance or production qualification is performed.
