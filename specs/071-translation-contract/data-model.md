# Translation Data Model

All structures below are planned contracts. Git owns authored content; system
fields are never accepted from frontmatter.

## Entities

| Entity | Fields / invariant |
| --- | --- |
| Translation source | Common GVK and ObjectMeta; `spec.locale`, `spec.targetRef`, ordered patches; exact UTF-8 body suffix; original accepted blob digest and repository/branch/commit attribution |
| Translation identity | Namespace UID + resource GVK + metadata.name; durable resource UID, generation and resourceVersion use existing lifecycle rules; locale/target changes do not create another resource |
| Patch | `fieldPath: string`, `translation: string`; explicit empty string distinct from absent/null; one eligible existing text leaf; no duplicate or overlapping leaf |
| Target descriptor | GVK/version, eligible paths, list identity functions, text constraints, body capability, immutable digest; compiled into supported binaries, not author controlled |
| Reference epoch | Normalized target GVK/namespace/name and authored UID/resourceVersion pins; changes only for an actual authored reference change, not locale/body/status changes |
| Sticky target binding | Translation UID + reference epoch -> first observed target UID; CAS-created, durable, retained across invalidation/restart; new UID requires explicit authored retargeting |
| Validation evidence | Translation generation/source revision/content digest, canonical locale, descriptor digest, conditions and bounded validated field-path list |
| Resolved target evidence | Target GVK/namespace/name/UID, authored generation, source revision/content digest and observed resourceVersion; no target content copied into status |
| Dependency edge | Translation UID/reference epoch -> target-name key and, after binding, target UID; derived/recoverable, never an owner reference |
| Invalidation work | Durable target-change cursor, last processed journal token, target evidence/version and pending dependent IDs; idempotent and resumable |
| Target evidence fence | Per target namespace/GVK/name, resource-sized CAS token and STABLE/UPDATING evidence/write intent; all material target writers participate; no cross-resource lock |

Raw authored locale and accepted raw blob remain recoverable. Canonical locale
is derived status, not a rewrite of source and not part of uniqueness.

## Status

Use the common condition type and status CAS. Conditions are `Accepted`,
`LocaleValid`, `TargetResolved`, `FieldsValid` and `Ready`, with standard
True/False/Unknown, reason, bounded message, transition time and observed
generation. `Ready=True` means validated source against the recorded target
snapshot; it never means applied localization.

Retain #40's common Resource status baseline: `conditions`,
`observedGeneration`, `lastAppliedRevision` and kind-specific `resolved`.
`lastAppliedRevision` records the Translation's own source revision successfully
reconciled into observed status (for example `main@sha1:a1b2c3d`), not the target's
revision and not proof that translated content was applied. Publish it with
the matching `observedGeneration` and evidence under status CAS; unresolved or
invalid target outcomes may still be successfully reconciled observations.
Failed or stale reconciliation must not advance it. Do not introduce a parallel
`observedRevision` field for the same source-revision role.

Put kind-specific results in `resolved`: `canonicalLocale`, `target` and
`validatedFieldPaths`. Persist the validation fingerprints `descriptorDigest`
and `sourceDigest` alongside that resolved evidence for the service guard, but
do not expose those bookkeeping fields through typed, generic or watch outputs.
Controller-only resolved-status inputs carry the fingerprints under `resolved`.
Use `{body}` only as a diagnostic/status marker for validated body content.
Omit proposed `resolved.appliedFieldPaths` and `ProjectionUpdated`: this feature
performs no target application. Accepted source attribution remains available
independently of controller progress and must not be inferred from
`lastAppliedRevision`.

The public status has exactly the four common fields above, with no separate
freshness timestamp, classification field or nested historical status copy.
Its conditions mask `Ready` and target/field success when evidence is not current;
stale, unavailable and forbidden outcomes use condition statuses and reasons.
Historical watch payloads are explicitly labeled at the event level and remain
subject to target-read redaction. The service guard in the
[lifecycle contract](contracts/lifecycle-and-storage.md) is mandatory, including
generic status reads/writes. Persisted historical True must not escape through
another schema route as an unqualified current True.

## State Transitions

| Input / event | Accepted source | Observation / current outcome |
| --- | --- | --- |
| Structural/locale/known-field violation at validation | No accepted revision | Bounded rejection; publish no subset |
| Permitted missing target | Full document accepted | Accepted/LocaleValid True; TargetResolved False/Missing; FieldsValid Unknown; Ready False |
| Available, authorized, valid target | Full document accepted | Bind UID and validate all content; Ready only through matching current evidence |
| Available permitted target, invalid fields | Source retained | Bind the observed eligible UID; not Ready, no partial validated-path success; replacement still requires explicit retargeting |
| Translation authored update | New generation/revision under existing CAS | Prior observation masked until its generation/digest matches |
| Target material change | Translation source unchanged | Metadata guard immediately detects mismatch when read; durable fan-out schedules revalidation |
| Target status-only update | Source/evidence otherwise unchanged | No invalidation unless an explicit resourceVersion pin no longer matches |
| Target removed | Source retained | Missing/not Ready; sticky UID retained |
| Same name, different UID | Source retained | TargetReplaced/not Ready; never adopt the new UID silently |
| Explicit target name/GVK change or new UID pin | New reference epoch | Revalidate; bind only the explicitly selected incarnation |
| Same UID, stale explicit resourceVersion | Source retained | PinMismatch/not Ready; visible mismatch at admission rejects the candidate |
| Lookup transport/storage failure | Source retained | Unknown/unavailable, retry durably; not Missing and never Ready |
| Translation deleted | Existing common deletion policy | Remove its binding/derived edges recoverably; never mutate/delete its target |

Changing an unrelated patch or locale does not authorize retargeting. For
same-name replacement, author `targetRef.uid` with the new UID; removing a pin
or erasing status is not a request to adopt another incarnation. UID history
is retained for unchanged target identity so toggling optional pins cannot
evade this rule.

## Ownership and Related-Resource Cases

| Resource | Owner / dependency behavior |
| --- | --- |
| Product inline options | Product owns local option titles; current `name` identity, future explicit `key` identity; no separate resource edge for inline members |
| Shared AttributeDefinition (deferred) | Independent target, permissions and UID/generation; Product `definitionRef` is not ownership; no implicit translation path through it |
| ProductVariant | Translate title/body only; SKU, selected options and canonical combinations are invariant |
| CategoryTaxonomy / Collection | Translate their own title/body; do not traverse parent/category membership/selector/target references |
| File | Translate descriptive body, not binary content, URI, checksum or credentials; consumers do not gain shared File write ownership |
| Release / Publication (deferred) | Freeze exact selected content and dependency inputs; no mutable Translation lookup can reinterpret a historical snapshot |
| Custom catalog resource (deferred) | Requires declared translation capability, text eligibility and stable structural list keys before allowlist activation |

### Inline-to-shared extraction walkthrough (design fixtures)

1. Under #492's proposed keyed model, Product `tee` owns
   `spec.options{size}.inline.values{m}.title`. A Translation targets the
   Product UID and that path. Keys `size` and `m` are not translated.
2. An author extracts the definition to independent AttributeDefinition
   `garment-size` and replaces the inline block with `definitionRef`. The old
   Translation must become invalid (`FieldMissing`/`ReferenceBoundary`), not
   follow the reference or silently keep Ready.
3. After that kind ships, the author explicitly creates/retargets a Translation
   on the defining UID with `spec.values{m}.title`, with defining-resource
   permission. The Product's offered `valueKeys` remain unchanged.
4. If the definition changes but Product generation does not, the direct
   definition Translation is invalidated by that definition's own evidence.
   A future localized Product materializer must invalidate its independent
   dependency vector too. This feature does not claim that materializer exists.
5. If the shared definition is deleted/recreated, its old Translation remains
   bound to the old UID until explicit authored retargeting. Missing, stale,
   forbidden and unsupported are distinct internal outcomes with safe external
   diagnostics.

Current runtime design fixtures accept
`spec.options{color}.title` on the existing Product contract; keyed
`spec.options{size}.inline.values{m}.title` is rejected until the keyed Product
contract ships. Direct `AttributeDefinition` references are currently
UnsupportedTarget. A path through `definitionRef` is always invalid.

## Storage Relationships

The authoritative resource row is single-resource sized. Thin name,
namespace-list and reverse-dependency indexes refer to its UID/resourceVersion;
they never duplicate the complete body/patch payload. Sticky binding is
authoritative system state, not a rebuildable guess from the current target.
Derived indexes are rebuilt from accepted Translation rows plus binding state.
Scylla co-locates the bounded binding snapshot with the full authoritative
resource row: one private coordination CAS fences source/status/binding writers,
and one final row CAS publishes source and binding together. A separate binding
table is not an independent authority or a recovery fallback. Memdb preserves
the same invariant with its shared transaction.

Index writes interrupted between tables are repaired using durable maintenance
intent and idempotent version comparisons. Listing/fan-out must not trust an
index whose repair watermark is behind its declared authoritative snapshot.
Segmented thin indexes roll over under CAS-allocated capacity; cursors retain
shard/segment position and each maintenance intent retains edge locations.
Physical keys, page/partition ceilings and CDC are specified in the
[lifecycle contract](contracts/lifecycle-and-storage.md).
