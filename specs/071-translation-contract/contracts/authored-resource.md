# Authored Translation Contract

**Selected design, not implemented support.** This contract evaluates and
selects the candidate envelope in #78; the specification's examples remain
compatible. Proposed #492 examples remain explicitly deferred design fixtures.

## Envelope

```yaml
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
```

In a Markdown document, wrap this mapping in the existing `---` frontmatter
delimiters; append optional translated Markdown after the closing line.

| Member | Contract |
| --- | --- |
| apiVersion / kind | Exact strings above; other versions/discriminators fail |
| metadata | Existing author-controlled ObjectMeta subset: name, namespace, labels, annotations; existing name/namespace rules; namespace must match owning repository |
| spec.locale | Required nonempty string, <=255 ASCII bytes; well-formed BCP 47 policy in research.md |
| spec.targetRef | Required common reference subset: apiVersion, kind, name; optional namespace, uid, resourceVersion; strings only, nonempty when present |
| spec.patches | Optional list, 0..256 entries; explicit null is invalid |
| patch.fieldPath | Required nonempty string satisfying the grammar below |
| patch.translation | Required string, including empty where target constraints permit; never null/coerced |
| Markdown body | Exact valid UTF-8 suffix; meaningful body means at least one non-whitespace character |

At least one patch or meaningful body is required. A whitespace-only suffix
may accompany valid patches and is still preserved exactly; it does not make
an otherwise empty document valid. Body-only Translation may omit `patches`.
Unknown envelope/spec/patch/reference members fail. `targetRef.fieldPath` is
forbidden: patches are the only field-addressing channel. Authored UID,
generation, revision, timestamps, finalizers, owner references and status fail.
Ordinary author labels/annotations do not confer system authority.

The target namespace defaults to the Translation namespace. An explicit other
namespace fails before lookup. Target kind/version must be in the table below.
UID/resourceVersion pins retain existing reference meanings: exact UID and
exact opaque resourceVersion, not a Git commit or generation.

## Path Grammar

```text
path       = "spec." segment ("." segment)*
segment    = property selector?
property   = (ALPHA | "_") (ALPHA | DIGIT | "_")*
selector   = "{" identity "}"
identity   = 1*(ALPHA | DIGIT | "_" | "-")
```

ASCII grammar; exact case-sensitive identity comparison; no trimming or
escaping. A descriptor may reject a grammatically valid property/identity.
No numeric indexes, wildcards, slashes, dots inside selector identities,
empty selectors, executable expressions or brace escapes. `{body}` is not an
authored path. Maximum 1,024 UTF-8 bytes/path, 16 segments and eight selectors.
Lists without an explicit stable-identity descriptor cannot be selected.

Resolution walks only the target's authored owned structure. It must find
exactly one member at every selector and an existing eligible string leaf;
absent/null, zero/multiple matches, object/list/non-text terminals fail.
Reference-valued members are hard boundaries. A name match is not permission
to traverse a `fileRef`, `productRef` or `definitionRef`.

Parse all paths before accepting any patch. Reject syntactic duplicates,
ancestor/descendant overlap and distinct addresses resolving to the same leaf.
Patch order is retained as authored but never supplies last-write-wins.
Resolve against a bounded per-target selector index once, not repeated scans
of each list per patch; reject ambiguous identities even after reordering.

## Delivered Target Matrix

All rows are selected implementation targets, not claims of current Translation
support. No target gains new source fields through this feature.

| Target GVK | Eligible field patches | Body | Explicit boundaries |
| --- | --- | --- | --- |
| catalog.gitstore.dev/v1beta1 Product | `spec.title`; `spec.options{NAME}.title`, with exact option `name` identity | Descriptive Markdown | No tags/categoryRef/lifecycle; current option values are strings/selection identities, not translatable choice objects |
| catalog.gitstore.dev/v1beta1 ProductVariant | `spec.title` | Descriptive Markdown | No SKU, selectedOptions, productRef, inventory, pricing, amounts or expressions |
| catalog.gitstore.dev/v1beta1 CategoryTaxonomy | `spec.title` | Descriptive Markdown | No parentRef or hierarchy identity |
| catalog.gitstore.dev/v1beta1 Collection | `spec.title` | Descriptive Markdown | No selector, targetRef or membership |
| storage.gitstore.dev/v1beta1 File | None | Descriptive Markdown only | No spec.title exists; no source URI, provider, contentType, checksum, credentials or binary operation |

All current media collections contain `fileRef` objects, not local display
text. For grammar/diagnostic resolution, `media{hero-image}` selects by
`fileRef.name` under an explicit descriptor; duplicate File names are ambiguous.
The object and its `fileRef` are not eligible text. Consequently no current
`spec.media{...}` patch succeeds. A future local alt/title field needs an
explicit resource/descriptor update; do not invent one in these fixtures.

Reuse each eligible field's existing semantic validation, including required
titles and Product title's 200-character maximum. Empty option title follows
its existing optional text contract. Translation limits bound fields lacking
a narrower target constraint; do not strengthen unrelated target validation.
Body means descriptive source, not a binary or already rendered representation.

**Deferred**: AttributeDefinition, all custom GVKs, infrastructure, identity,
authorization and Translation itself are unsupported targets. Only the first
two are potential future catalog capabilities; the other categories remain
forbidden. A descriptor cannot activate an absent kind implicitly.

Future #492 descriptors must explicitly select `key` for both option and
choice identity. `spec.options{size}.inline.values{m}.title` addresses
Product-owned text; `spec.values{black}.title` targets a defining
AttributeDefinition directly. These are valid design examples, not current
runtime paths. `spec.options{size}.definitionRef.values{m}.title` is invalid
even after shared definitions ship.

## Parsing and Work Limits

In addition to existing request/candidate/push-policy limits, set these hard
Translation ceilings; a lower existing deployment bound wins.

| Work | Ceiling / outcome |
| --- | --- |
| Raw Translation blob, including body | 1 MiB |
| YAML tree | 65,536 nodes, depth 64; aliases/merge keys rejected before expansion |
| Patches / paths | 256 patches; limits above; no partial evaluation success |
| Translation text | Bounded by total blob and applicable target text constraints |
| Target work | One direct target, existing maximum target-document size, one selector-index construction; at most 65,536 snapshot values, depth 64 and 100 MiB of keys/string/number data; no reference walk or catalog scan |
| Diagnostics | First 32 issues in document/patch order; each message <=256 bytes plus bounded common location; explicit truncation indicator |

Use the existing candidate file/byte limits and cancellation/deadline budgets;
do not increase them to accommodate Translation. Exceeding source limits is a
deterministic rejection. Target lookup failure/timeout/work unavailable is a
bounded operational failure, never a successful field validation or a claim
that a resource is absent. Read paths must not fetch target content at all.

For exact body extraction, support existing LF and CRLF delimiter lines and
preserve the complete suffix after the first closing delimiter line ending.
No suffix is an empty body. Delimiter-adjacent blank lines belong to the body;
embedded later `---` lines are ordinary body bytes. Reject malformed or
non-UTF-8 input; never replace invalid bytes. Test opening/closing delimiter
edge cases against the existing parser's accepted framing without changing
other kinds' body behavior.

## Candidate and Error Semantics

Build the effective candidate view before target validation: changed target
documents override their prior versions; candidate deletions count as missing;
renames use resource identity, not file order. Do not query old stored content
when the candidate contains the target. Duplicate/conflicting target identities
fail under existing admission rules. Pins unavailable until identity allocation
remain unresolved, not falsely validated.

Perform all source/descriptor-decidable checks even if the target is missing.
Accept a permitted absent target as unresolved. When a target is available,
validate pins, field existence/identity/constraints and body eligibility.
Concurrent changes after the validation snapshot are handled by replay-time
revalidation and the status guard, not a promise of atomic cross-resource Git
and datastore admission.

Reuse existing admission diagnostics and `RejectedError` conventions. Include
document path, YAML/field location or `spec.patches[N]`, stable reason and safe
explanation; do not echo translated values. Internally distinguish invalid
locale/path, unsupported target/field, ambiguity, overlap, pin mismatch,
reference boundary, malformed source, unavailable and overload. Externally
conflate forbidden versus nonexistent target detail where required by policy;
never disclose an unauthorized UID, revision, title or existence.
