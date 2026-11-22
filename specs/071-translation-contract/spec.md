# Feature Specification: Translation Frontmatter Support

**Feature Branch**: `071-translation-contract`
**Created**: 2026-11-18
**Status**: Draft
**Input**: User description: "Expand spec-071 to cover the parent #78."
Consider related resources, including #492's inline/reusable options; regard
the envelope in #78 as a proposal.

## Scope and Dependencies

This feature covers the complete Translation frontmatter initiative #78, under
the catalog-frontmatter initiative #40, rather than only its contract slice
#188. It defines the resource, parsing and validation behavior, target-resolution
boundary, integration acceptance and documentation required by #188-#191.
Writing this specification does not implement or close those issues.

| Issue | Delivery covered by this specification | Requirements | Dependency |
| --- | --- | --- | --- |
| #188 | Canonical envelope, fields, ownership, examples and error contract | FR-001 through FR-005, FR-018 | Baseline for #189 and #190 |
| #189 | Field-path grammar, selectors, conflicts and Markdown-body preservation | FR-006 through FR-009 | #188 |
| #190 | Locale validation, target scope, eligible fields and resolution outcomes | FR-010 through FR-017 | #188 |
| #191 | End-to-end ingestion coverage and published authoring documentation | FR-019 through FR-021; PR-001 through PR-007 | #189 and #190 |
| #492 (related) | Inline/shared option design alignment, not its implementation | FR-006, FR-012, FR-015, FR-022, FR-023; SC-008 | Coordinate proposed contracts before finalizing Translation design |

### Proposal Status and Related-Resource Design

The envelope in #78 is a design input, not an accepted or immutable contract.
Its API group/version, field names, patch representation, reference shape,
selector syntax and status fields may be revised where the design justifies it.
Concrete names below describe that candidate, not an obligation to preserve it.
The reviewed design MUST record the selected contract and alternatives, reconcile
affected contracts and examples, and establish the implementation baseline before
#188 is considered complete. No legacy-format compatibility is required.

#492 is a related design dependency, not an implementation prerequisite or a
claim that reusable options already exist. Its proposed `AttributeDefinition`,
keyed option/value members and inline versus referenced definitions must inform
this design; current Product fields alone are not a sufficient target model.

| Related resource or value | Required design consideration |
| --- | --- |
| Product and owned inline options | Address local labels and choice titles using stable member identities; inline objects remain owned values, not separate resources |
| Proposed AttributeDefinition | Address shared labels and choice titles on the defining resource; a Product reference is not ownership or permission to translate it indirectly |
| ProductVariant | Keep presentation separate from option/value selection identity and canonical combinations |
| CategoryTaxonomy and Collection | Declare their own eligible text/body fields and reference boundaries rather than copying Product-specific paths |
| File and media references | Distinguish a File's descriptive body from a consuming resource's presentation; do not assume every media reference supplies alt text |
| Proposed CatalogRelease and Publication | Identify exact translation/dependency inputs for reproducible snapshots; mutable draft edits must not reinterpret historical content |
| Proposed custom catalog resources | Evaluate explicit eligibility and schema-declared text/list identity; being visible or release-eligible alone does not make every string translatable |

Coordinate with #492 and the proposed
[release/publication](../../docs/ADRs/0014-catalog-release-and-publication.md)
and [custom-resource](../../docs/ADRs/0016-custom-resource-definitions.md)
designs. These remain proposals, not capabilities implemented by this feature.
The design must allocate responsibility for override precedence, locale fallback
and missing/stale localized dependencies without implementing localized delivery
or silently expanding this feature into the whole of #492.

**In scope**:

- Authoring, parsing and validating Translation documents through the existing
  catalog ingestion flow using the reviewed envelope; the candidate below uses
  `i18n.gitstore.dev/v1beta1`.
- Common resource metadata, target references and system-owned validation/
  resolution status, including accepted-revision attribution.
- Field-only, body-only and combined translations, nested paths and named
  collection selectors.
- Deterministic locale, reference, field-eligibility and diagnostic rules.
- Related-resource design for owned inline content, shared definitions,
  dependency freshness and publication boundaries, with explicit target support.
- Integration coverage, regression fixtures and user/contributor documentation
  for the delivered behavior.
- Authorization isolation, concurrent replicas, bounded work and recovery for
  the ingestion and resolution paths this feature adds.

**Out of scope**:

- Applying or merging translations into a Product or other target's resolved
  representation; selecting a winning translation; localized delivery.
- Implementing locale fallback, a new Locale resource, market localization,
  translation tooling, workflow automation or machine translation.
- Translating infrastructure, identity or authorization resources; introducing
  new target resource kinds merely to make them translatable.
- Legacy-format compatibility, migration tooling, new authentication providers,
  a separate translation service or changes to Git's singleton deployment model.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Submit a valid Translation (Priority: P1)

A catalog author supplies a locale and target, with translated fields, a
translated Markdown body, or both. The normal ingestion flow recognizes and
preserves the document without changing the target's source content.

**Why this priority**: Authors need usable frontmatter support, not only a
description of a future resource shape.

**Independent Test**: Submit the examples for delivered targets, updated to the
reviewed contract, against matching owned targets, then verify the accepted
Translation's identity, locale, reference, patches, body and source revision;
verify the target remains unchanged.

**Acceptance Scenarios**:

1. **Given** a Product and a field-only Translation, **When** the author submits
   the document, **Then** its title patch is accepted without requiring a body.
2. **Given** a target supporting body content, **When** the author supplies only
   a non-whitespace Markdown body, **Then** `patches` may be omitted and the
   complete body is preserved outside `spec`.
3. **Given** patches and body content together, **When** ingestion succeeds,
   **Then** both channels retain their authored values, including an explicitly
   empty patch value where the target field permits empty text.
4. **Given** a File target, **When** the author submits its translated descriptive
   body, **Then** that content is accepted without changing the File's source
   URI, binary content or processing configuration.

### User Story 2 - Address nested text predictably (Priority: P1)

A catalog author addresses a nested textual field or a named collection member
without relying on array position or undocumented conflict resolution.

**Why this priority**: Accepting a path with the wrong meaning can translate the
wrong content even when the document appears structurally valid.

**Independent Test**: Validate a corpus of nested paths and named selectors
against known target shapes; every case resolves to one eligible text field or
produces the documented failure, independent of collection ordering.

**Acceptance Scenarios**:

1. **Given** `spec.title` or `spec.options{color}.title` on a matching Product,
   **When** paths are checked, **Then** they identify the intended textual
   fields; reordering options does not change the selected member.
2. **Given** a media selector such as `spec.media{hero-image}`, **When** its
   grammar is parsed, **Then** it is recognized as a named selector, but it
   cannot replace an entire media object with a string.
3. **Given** duplicate paths, ambiguous selectors, a nonexistent field or a
   non-text terminal value, **When** validation runs, **Then** the affected
   Translation is rejected rather than partially accepted or resolved by
   last-write-wins.
4. **Given** a body containing headings, embedded `---` lines and non-ASCII text,
   **When** ingestion succeeds, **Then** those bytes remain body content and are
   not reparsed as another frontmatter envelope.
5. **Given** #492's proposed inline and reusable option models, **When** the
   design identifies the owner of each translated label, **Then** inline text
   belongs to the Product and shared text to the AttributeDefinition; a path
   does not silently traverse `definitionRef`.
6. **Given** mutable labels and stable option/value keys, **When** labels are
   translated or a collection is reordered, **Then** variant selections and
   combination identity remain unchanged. Extracting an inline definition into
   a reusable resource requires an explicit translation-retargeting rule.

### User Story 3 - Understand locale and target validity (Priority: P1)

An author can distinguish an invalid locale or forbidden reference from a valid
document whose target has not yet become available.

**Why this priority**: Reference timing must not make validity unpredictable or
allow a Translation to cross tenant or resource boundaries.

**Independent Test**: Exercise valid and invalid locales, same-namespace targets,
missing targets and target replacement; verify deterministic acceptance,
resolution status and unchanged target content.

**Acceptance Scenarios**:

1. **Given** `de-DE`, `fr-FR` or an equivalent valid case variant, **When** the
   locale is validated, **Then** it has a stable canonical comparison form;
   malformed values such as `en_US` are rejected, not silently repaired.
2. **Given** a structurally valid reference to an absent permitted target,
   **When** the Translation is submitted, **Then** its source is accepted
   but target-dependent validation remains unresolved and it is not Ready.
3. **Given** a target created in the same candidate as the Translation,
   **When** validation runs, **Then** candidate contents are considered rather
   than reporting a false missing target based only on previously stored data.
4. **Given** a cross-namespace, unsupported-kind or mismatched identity-pinned
   reference, **When** validation runs, **Then** it fails without disclosing
   another tenant's content.
5. **Given** a previously resolved target is deleted or replaced, **When**
   reconciliation observes that change, **Then** stale resolution is withdrawn
   and a different UID is not silently treated as the original target.

### User Story 4 - Correct invalid documents using useful diagnostics (Priority: P1)

An author receives a document and field location with a bounded explanation of
the violated rule, without needing access to internal logs or other tenants.

**Why this priority**: Parser, schema and runtime disagreement would turn the
documented format into trial-and-error authoring.

**Independent Test**: Submit the negative matrix below through ingestion and
compare the result with standalone validation of the same source documents.

**Acceptance Scenarios**:

1. **Given** wrong version/kind, missing required values or malformed patches,
   **When** validation runs, **Then** it rejects the document at the offending
   location and does not coerce it into a valid Translation.
2. **Given** one valid and one invalid patch, **When** submission is rejected,
   **Then** no accepted Translation revision contains only the valid subset.
3. **Given** an authored status or machine-owned metadata field, **When**
   validation runs, **Then** it rejects the attempt to assert system authority.

### User Story 5 - Rely on tested support and accurate documentation (Priority: P2)

Authors can use published examples with the delivered ingestion flow, while
contributors can verify the same behavior across replicas and after recovery.

**Why this priority**: Contract documentation alone does not demonstrate that
the feature works end to end or that its support boundary is accurately stated.

**Independent Test**: Run all published examples and negative fixtures through
the connected ingestion path; compare accepted source, status and unchanged
target content on both APIs before and after the declared recovery scenarios.

**Acceptance Scenarios**:

1. **Given** the feature is delivered, **When** an author follows its guide,
   **Then** the examples pass the same parser and validation rules as ingestion.
2. **Given** two APIs and two controllers observe duplicate or concurrent work,
   **When** an accepted Translation is projected, **Then** they converge on its
   UID, generation and revision without duplicate resources or stale Ready state.
3. **Given** processing stops after Git acknowledgement, **When** the owned
   service restarts, **Then** accepted source is recovered without another push,
   preserving its body and leaving the target representation unchanged.

### Edge Cases

- Missing, null, blank or incorrectly typed required fields; unknown fields;
  malformed frontmatter, duplicate mapping keys and invalid text encoding.
- Omitted or empty `patches`; whitespace-only body; explicitly empty translated
  text; headings, delimiters, line endings and non-ASCII characters in the body.
- Missing/ambiguous named members, unsupported numeric indexes or wildcards,
  duplicate/overlapping paths and paths ending at objects, lists or non-text data.
- Locale case variants, valid extended tags, malformed tags and multiple named
  Translation documents sharing a target and equivalent locale.
- Missing targets, same-candidate targets, wrong versions/kinds, cross-namespace
  references, stale identity pins, target deletion and same-name recreation.
- Changes to a target's text-field shape after a Translation was accepted.
- Current `name`-based collections versus proposed `key`-based collections;
  nested keyed choices, key removal/reuse and inline-to-reusable extraction.
- Shared-definition edits with unchanged consuming Product generations,
  dependency deletion/recreation and forbidden implicit reference traversal.
- Missing/stale translations in a future release candidate versus immutable
  content already captured in a publication or historical snapshot.
- Duplicate delivery, concurrent status updates, stale callbacks, cancellation,
  configured size limits and interrupted processing after acknowledgement.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Ingestion MUST recognize the reviewed Translation envelope
  unambiguously and reject unsupported versions or incorrect resource
  discriminators. The proposed `i18n.gitstore.dev/v1beta1`, `Translation`,
  `metadata` and `spec` envelope MUST be evaluated against common storage-group
  conventions and related resources, not treated as a preapproved API.
- **FR-002**: Translation MUST be namespace-scoped, reuse common resource
  metadata and require the owning repository's namespace. Authored documents
  MUST NOT set machine-owned status, UID, revision, generation or lifecycle
  fields; ordinary author-controlled labels and annotations remain supported.
- **FR-003**: The reviewed contract MUST represent a non-empty BCP 47 locale,
  an unambiguous target and field translations and/or body content. In #78's
  candidate these are `spec.locale`, `spec.targetRef` and optional ordered
  `spec.patches` entries containing `fieldPath` and `translation`. Their final
  names and representation MUST be decided in design; required text values
  MUST NOT accept nulls or coerce non-string values.
- **FR-004**: A document MUST contain at least one patch or a non-whitespace
  Markdown body. An empty translation string is an explicit value, not omission;
  it remains subject to the target text field's own validity constraints.
- **FR-005**: Frontmatter parsing MUST reject malformed structures, duplicate
  mapping keys and unknown envelope/spec/patch members. Common metadata and
  reference rules MUST be reused rather than introducing competing formats.
- **FR-006**: Field addressing MUST support nested text and collection members
  selected by schema-declared stable identities, not mutable labels or array
  positions. Dot paths rooted at `spec` and brace selectors are candidate
  syntax, not a frozen grammar. The design MUST cover current `name`-based
  members, media reference identities and #492's proposed option/value `key`
  members, including nested selectors. It MUST NOT guess a key by trying
  arbitrary `name`/`key` properties. Identity comparison is exact and
  case-sensitive; positional indexes, wildcards, executable expressions and
  undeclared escaping MUST be rejected.
- **FR-007**: A selector MUST match exactly one member and a complete path MUST
  identify one existing, eligible textual leaf. Missing or ambiguous members,
  unsupported properties and object/list/non-text terminal values MUST fail
  target-dependent validation. Syntactically valid paths MUST NOT imply that
  their selected values are translatable.
- **FR-008**: Within one Translation, duplicate paths and paths addressing
  overlapping content MUST be rejected, including equivalent paths selecting
  the same leaf. Patch ordering MUST NOT supply last-write-wins semantics.
- **FR-009**: The complete content after the closing frontmatter delimiter MUST
  be preserved as the translated body, including text, whitespace and line
  endings. Ingestion MUST NOT render, trim, merge or sanitize that stored source.
  If retained, the proposed `{body}` marker is for status/diagnostics, not a
  second authored patch path; body content MUST be supplied only through the
  Markdown body channel.
- **FR-010**: Locale validation MUST accept well-formed BCP 47 tags without
  requiring a separate Locale resource or configured market. It MUST provide
  deterministic canonical comparison, while preserving authored source.
  Malformed tags, underscores and surrounding whitespace MUST be rejected.
  Locale equivalence MUST NOT choose a fallback or merge documents.
- **FR-011**: Target references MUST identify resource version, kind and name,
  reusing common namespace/UID/resource-version reference conventions rather
  than freezing #78's proposed `targetRef` shape. An omitted
  namespace means the Translation's namespace; an explicit different namespace
  MUST fail. The reviewed contract MUST have one unambiguous field-addressing
  channel rather than conflicting reference-level and patch-level paths.
- **FR-012**: Permitted target kinds and versions MUST be explicitly cataloged.
  Design MUST consider Product, ProductVariant, CategoryTaxonomy, Collection,
  File in its distinct storage group, #492's proposed AttributeDefinition and
  eligible custom catalog resources. The implemented allowlist MUST distinguish
  delivered target contracts from proposed or deferred ones; this list is not
  frozen to today's Product shape. Unsupported kinds/versions MUST fail, not
  acquire support implicitly. Translation, infrastructure, identity and
  authorization resources MUST NOT be targets.
- **FR-013**: Eligible fields MUST be human-facing textual content identified
  by the target's resource contract. Identity, references, selectors, lifecycle,
  permissions, source URIs, checksums and other operational values MUST NOT
  become translatable merely because their representation is a string.
  Option/value keys, offered-value membership, variant selections and canonical
  combination identity MUST NOT be translated; display titles may be eligible.
  Translated values MUST satisfy the eligible field's applicable text limits.
  A body-only Translation is valid only for a target with a body-content contract;
  the parent initiative's File example describes media text, not binary content.
- **FR-014**: Admission MUST check all rules decidable from the authored
  candidate and known target contract, including same-candidate target changes.
  An otherwise valid Translation referencing a permitted but absent target MUST
  be accepted as unresolved; target-dependent field checks MUST NOT be falsely
  reported as passed.
  Once the target is available, all reference and field checks MUST run.
- **FR-015**: Explicit reference pins MUST be enforced when their target is
  available. Observed resolution MUST record the target's actual UID and
  generation/revision and any required dependency inputs using common reference
  conventions. A consuming Product's unchanged generation alone MUST NOT prove
  freshness after a shared definition changes. Deletion, replacement
  or incompatible target changes MUST withdraw stale readiness; a new target
  incarnation requires an explicit authored retargeting change rather than
  inheriting the old resolved identity.
- **FR-016**: System-owned status MUST distinguish admission, locale,
  target-resolution and field-validation outcomes, using common condition,
  observed-generation and source-revision conventions. Retain #40's common
  status baseline: `conditions`, `observedGeneration`, `lastAppliedRevision`
  and kind-specific `resolved` evidence. `lastAppliedRevision` identifies the
  Translation's own source revision successfully reconciled into its observed
  status; it does not identify a target revision or claim localized application.
  Ready means this Translation is validated and its target is resolved, not
  that a localized target has been produced. The design MUST choose or omit
  #78's proposed `resolved.target` and
  `resolved.appliedFieldPaths` fields according to their actual semantics,
  rather than inventing application work to fill them. No status or
  `ProjectionUpdated` outcome may claim target application before that separate
  feature performs it.
- **FR-017**: Multiple named Translations MAY identify the same target and
  equivalent locale. Each MUST validate independently; this feature MUST NOT
  select precedence, combine their patches or claim a conflict-resolution policy
  for a future localized representation.
- **FR-018**: Rejections MUST identify the document, field or patch index and
  violated rule using existing admission/error conventions. Diagnostics MUST
  be bounded and MUST NOT disclose complete translated content, credentials,
  unauthorized target existence or another namespace's data. Rejected documents
  MUST NOT publish a successful partial patch set or advance accepted status.
- **FR-019**: Accepted Translation content and source attribution MUST converge
  through the existing resource ingestion/projection lifecycle. Duplicate or
  delayed callbacks MUST NOT replace a newer accepted revision or mark an old
  generation Ready. No target's authored or resolved content may be changed by
  accepting, updating, resolving or deleting a Translation.
- **FR-020**: Integration coverage MUST include every example and negative
  category below, same-candidate targets, unresolved-to-resolved transitions,
  target replacement, duplicate delivery, concurrent replicas and interrupted
  ingestion. It MUST include related-resource cases for every delivered target
  model and explicit unsupported-target outcomes for deferred models.
  It MUST compare stored source/status with the authored candidate
  and independently confirm that the target remains unchanged.
- **FR-021**: The canonical resource guide, examples, validation reference and
  existing localization overview MUST agree on version, fields, path/locale
  rules, target eligibility and actual support status. Contributor documentation
  MUST explain the boundary between validation/resolution and future application.
- **FR-022**: The design MUST distinguish owned inline values, references to
  shared resources and reverse dependency indexes. Translating an inline label
  MUST address its owning resource; translating a shared label MUST address the
  defining resource. Field addressing MUST NOT implicitly dereference another
  resource, bypass its authorization or make shared definitions owned by a
  consuming Product. Any product-local override requires an explicit contract.
- **FR-023**: Before implementation, the reviewed design MUST record how
  Translation relates to #492's option/value identities, extraction and
  dependency lifecycle, and to release/publication and custom-resource
  proposals. It MUST specify dependency identity/freshness, bounded change
  propagation/traversal, cycle diagnostics, same-candidate ordering and
  missing/stale outcomes. Related-resource summaries MUST be materialized
  asynchronously into system-owned status; GraphQL resolvers MUST NOT scan
  related resources or calculate their aggregates live. The design MUST allocate
  override/fallback and snapshot responsibilities to the appropriate feature,
  without implementing localized merging here or allowing later Translation/
  shared-definition edits to rewrite historical snapshots.

### Validation and Outcome Matrix

| Case | Required outcome |
| --- | --- |
| Wrong version/kind, malformed frontmatter or duplicate key | Reject the Translation with its document location |
| Missing/null/blank locale or required target member | Reject at the offending member |
| Non-list patches, malformed patch or non-string translation | Reject at `spec.patches` or its indexed member |
| Neither a patch nor meaningful body content | Reject the empty translation document |
| `en_US`, malformed tag or locale with surrounding whitespace | Reject at `spec.locale`; do not silently repair |
| `de-de` and `de-DE` | Accept valid spelling; compare as the same locale without choosing precedence |
| Invalid path grammar, duplicate or overlapping paths | Reject at the offending patch; publish no subset |
| Valid path to an object, non-text field or operational identifier | Fail field validation, not a string conversion or object replacement |
| Path attempting to traverse a reference into a shared definition | Reject implicit traversal; require the explicit defining-resource target |
| Translated option/value key, offered membership or variant selection | Reject identity/selection mutation rather than treating it as display text |
| Missing/ambiguous selector or field on an available target | Reject at admission if observable there; otherwise mark not Ready |
| Cross-namespace, forbidden-kind/version or observed pin mismatch | Reject without leaking unauthorized target details |
| Permitted target absent | Accept source as unresolved; target/path conditions and Ready do not claim success |
| Target and Translation in one valid candidate | Validate against that candidate's target, independent of file order |
| Target deleted/replaced after resolution | Withdraw old readiness; never silently reuse the old resolved UID |
| Required shared dependency changes while consuming Product generation is unchanged | Revalidate affected observations using dependency identities/revisions, not Product generation alone |
| Authored status or machine-owned metadata | Reject the attempted system-owned field |
| Size/work budget exceeded | Reject or report bounded unavailability under existing overload conventions |

### Production Requirements

- **PR-001 Replica Safety**: Two APIs and two controllers with independent
  writable checkpoints MUST converge on the same accepted Translation identity,
  content, source revision and current-generation conditions during duplicate
  work, concurrent updates and rolling replacement. Git remains exactly one
  active process with retained storage and non-overlapping replacement.
- **PR-002 Multi-User Security**: Reuse pluggable authentication and authorization
  for authoring, reading and status changes. Permission to author a Translation
  MUST NOT grant permission to mutate or inspect a forbidden target. Test both
  allowed and denied principals, namespace isolation and stale reference pins.
- **PR-003 Capacity**: Preserve the existing profiles' dataset, rate, latency,
  error and full-duration gates. Translation coverage MUST add at least 1,000
  documents across ten owned namespaces and two locales, exercise every
  supported target kind, both translation channels and near-limit documents,
  and include concurrent submissions through both APIs. Report actual completed
  work separately from offered work; accepted source loss and partial ingestion
  have a zero-tolerance correctness gate.
- **PR-004 Backpressure**: Reuse existing configured candidate/document limits,
  worker, queue, retry and request-deadline controls. Path resolution MUST be
  bounded by the submitted document and referenced target, not a catalog-wide
  search. Oversize input, cancellation and overload MUST terminate without
  publishing partial Translation content or falsely successful status.
- **PR-005 Capacity Evidence**: Extend existing
  `make capacity TARGET=namespace PROFILE=validation MODE=alpha` coverage for
  bounded validation, and `make capacity TARGET=api PROFILE=readiness MODE=alpha`
  for deployed behavior. Translation-specific corpora and correctness joins are
  implementation prerequisites, not capabilities claimed to exist today.
  Helper-process validation alone does not prove deployed ingestion, replica
  safety or authorization correctness.
- **PR-006 Fault Recovery**: Exercise an owned API/controller restart, callback
  loss and interruption after Git acknowledgement through existing reusable
  fault runners. Within their original recovery deadlines, accepted source,
  target-resolution fences and current-generation status MUST converge without
  resubmission. Record faults actually exercised; do not infer physical outages
  from simulated failures.
- **PR-007 Recovery Integrity**: Recovery MUST preserve accepted Git history,
  resource UIDs, unmodified target content and unrelated tenant data. Tests MUST
  cover lost replies and duplicate replay and retain failed evidence separately
  from successful recovery. Retained capacity datasets MUST NOT be reseeded or
  removed to make a run pass.

### Key Entities

- **Translation document**: A namespace-scoped resource containing a locale,
  target reference, textual field patches and/or a translated Markdown body.
- **Translation entry**: A field address and translated text; the proposed
  `fieldPath`/`translation` pair is subject to contract review.
- **Target reference**: Resource kind/version/name plus optional same-namespace
  and identity/version pins; observed resolution is system-owned.
- **Named selector**: A stable name-based address into a target collection, not
  an array index or executable expression; the schema may declare a `key`
  rather than the current Product contract's `name`.
- **Owned inline definition / shared definition**: A Product-owned value versus
  #492's proposed independent AttributeDefinition; these have different target
  identities, authorization and dependency lifecycles.
- **Validation/resolution status**: Current-generation observations of source,
  locale, target and field validity; not proof of localized target application.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every example for a delivered target, revised to the reviewed
  envelope and target contract, passes the connected ingestion path
  against matching targets, retaining their exact authored translation values
  and body content while leaving target source and resolved content unchanged.
  Examples for deferred target kinds produce explicit unsupported-target
  outcomes instead of implying runtime support.
- **SC-002**: Every negative matrix category has automated coverage and the same
  documented classification through standalone validation and ingestion;
  zero malformed documents or partial patch sets are accepted as successful.
- **SC-003**: Reordering a named target collection changes zero selector results;
  duplicate/ambiguous selections and ineligible terminal values never produce
  a successful validated Translation.
- **SC-004**: Every accepted Translation's current status distinguishes valid,
  unresolved and invalidated target states, with zero Ready observations
  attributed to an older generation or replaced target UID.
- **SC-005**: Both API replicas converge on every accepted Translation and its
  current source/status during the declared rolling/recovery scenarios within
  existing runner deadlines; no recovery requires reauthoring acknowledged data.
- **SC-006**: The declared sustained corpus completes the unchanged acceptance
  windows and thresholds, with zero unauthorized disclosure, cross-namespace
  resolution, accepted-source loss or target mutation and no configured
  resource ceiling exceeded.
- **SC-007**: The authoring guide, examples and localization overview have zero
  conflicting field names, semantic rules or support claims, and all four child
  issues map to requirements and acceptance coverage in this specification.
- **SC-008**: The reviewed design records eligibility, identity, ownership and
  dependency rules for every row in the related-resource table. It includes
  valid/invalid inline and shared-definition translation cases, an extraction
  walkthrough, and explicit release/custom-resource handoffs. No proposal-only
  envelope or resource is advertised as implemented support.

## Authoring Examples

These illustrate candidate contracts, not an approved envelope or implemented
Translation support. The design may revise them, including the envelope and
paths, before they become acceptance fixtures. Each delivered target must use
its reviewed resource contract, be accessible to the author and expose the
referenced content. Proposed-only targets are design fixtures, not runtime
support claims. Authored examples omit system-owned metadata and status.

### Product field only

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: laptop-title-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    name: laptop
  patches:
  - fieldPath: spec.title
    translation: Tragbarer Computer
---
```

### Product body only

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: laptop-body-fr-fr
  namespace: acme-store
spec:
  locale: fr-FR
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    name: laptop
---

# Ordinateur portable

Un ordinateur pour le travail et les voyages.
```

### Combined fields, named selector and body

The current Product shape has one option whose stable `name` is `color`.
This example does not require the new design to retain that shape.

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: laptop-details-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    name: laptop
  patches:
  - fieldPath: spec.title
    translation: Tragbarer Computer
  - fieldPath: spec.options{color}.title
    translation: Farbe
---

Ein Computer mit einer Auswahl an Farben.
```

### Proposed keyed inline choice

Under #492's candidate model, the Product owns option `size`, whose inline
definition contains a choice with stable `key: m`. Neither key is translated.

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: tee-inline-size-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: Product
    name: personalized-tee
  patches:
  - fieldPath: spec.options{size}.inline.values{m}.title
    translation: Mittel
---
```

### Proposed shared-definition choice

Under #492's candidate model, the shared definition owns the `black` choice.
The target is the AttributeDefinition itself, not a Product path through
`definitionRef`. This kind and its translation eligibility require design review.

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: garment-black-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: catalog.gitstore.dev/v1beta1
    kind: AttributeDefinition
    name: garment-color
  patches:
  - fieldPath: spec.values{black}.title
    translation: Schwarz
---
```

### File descriptive body

```markdown
---
apiVersion: i18n.gitstore.dev/v1beta1
kind: Translation
metadata:
  name: laptop-hero-de-de
  namespace: acme-store
spec:
  locale: de-DE
  targetRef:
    apiVersion: storage.gitstore.dev/v1beta1
    kind: File
    name: laptop-hero
---

Ein silberner Laptop auf einem Schreibtisch.
```

## Assumptions and Delivery Boundary

- The scope is the whole of #78, not the broader #40 roadmap. #188 remains the
  contract baseline; #189 and #190 can proceed after it, and #191 integrates
  both. These are delivery dependencies, not reasons to defer their requirements
  to another feature specification.
- #78's envelope and #492's related resource shapes are proposals. Existing
  contracts constrain integration, not the right to revise proposed formats.
  The design must justify the selected envelope and synchronize affected
  contracts/examples; no legacy compatibility obligation is introduced.
- #492's implementation is not pulled into this feature. Its owned/shared
  distinction and stable keys must nevertheless be considered before selecting
  Translation targets, selectors and dependency/status semantics. Unsupported
  proposed targets remain explicitly unsupported until their contracts ship.
- Accepting an unresolved permitted reference supports independent Git
  authoring. It never makes a missing target Ready or bypasses target-dependent
  validation once that target exists.
- Application, winner selection and locale fallback are explicitly separate.
  Source preservation and validation status MUST NOT be presented as a
  functioning localized Product or media-delivery feature.
- Reuse common metadata/reference/error and lifecycle mechanisms. Any necessary
  changes to related contracts must be explicit design decisions, not accidental
  consequences of freezing a proposed Translation envelope.
- Implementation planning must identify affected shared contracts and resource
  paths, the bounded workload configuration and required runner extensions,
  resolve the envelope and related-resource decisions above, and update the
  examples to the selected contract. This document is ready for design/planning,
  not a claim that those decisions are settled, runtime support exists or
  production acceptance has shipped. No ADR status changes merely because the
  specification has been expanded.
