# Translation authoring

Translation records locale-specific text for another resource. The implemented
contract provides **validation and resolution only; no localized application**.
It does not modify target content or select translated Storefront responses.
Admission is disabled by default. These examples apply only to an explicitly
enabled, compatible deployment; see the [operator guide](../runbooks/translations.md)
before enabling the feature. Sustained and production acceptance remain pending.

## Resource and content channels

Author a namespace-scoped Markdown file with `apiVersion:
i18n.gitstore.dev/v1beta1`, `kind: Translation`, `metadata.name`,
`metadata.namespace` and `spec`. The spec contains `locale`, `targetRef` and
optional ordered `patches`, each with `fieldPath` and `translation`.
Do not author server-owned UID, generation, resource version or status fields.

The field channel is a nonempty patch list. The body channel is translated
Markdown after the closing frontmatter delimiter. Supply either channel or
both; an empty patch list with an empty/whitespace-only body is invalid.
The first closing delimiter ends the frontmatter. Every subsequent body byte,
including blank lines, CRLF and embedded `---` lines, is retained unchanged.
Do not move that content into a `spec.body` field.

### Field-only Product example

This example expects Product `laptop` in namespace `acme-store`. Save it as
`translations/laptop-title-de-de.md` in that namespace's system repository.

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

### Body-only File example

This example expects File `laptop-hero` in the same namespace. Save it as
`translations/laptop-hero-de-de.md`. File does not have a translatable
`spec.title`; use the body channel.

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

Commit and push through normal Git Smart HTTP. Git acknowledgement is not proof
that asynchronous validation has converged: read the accepted source and its
current Ready condition from Admin afterwards. Both examples are checked
byte-for-byte against the corpus used by the real ingestion tests.

## Targets and paths

| Target kind | Target API version | Eligible field paths | Markdown body |
|---|---|---|---|
| Product | `catalog.gitstore.dev/v1beta1` | `spec.title`, `spec.options{<name>}.title` | Yes |
| ProductVariant | `catalog.gitstore.dev/v1beta1` | `spec.title` | Yes |
| CategoryTaxonomy | `catalog.gitstore.dev/v1beta1` | `spec.title` | Yes |
| Collection | `catalog.gitstore.dev/v1beta1` | `spec.title` | Yes |
| File | `storage.gitstore.dev/v1beta1` | None | Yes |

`spec.options{color}.title` selects the Product option whose declared `name`
is exactly `color`. Selectors use declared stable identities, not array
positions. Reordering the target's options does not change that selection.
Zero matches or multiple matches cannot resolve successfully. Numeric indexes,
wildcards, duplicate/ancestor-overlapping paths and implicit traversal through
references are unsupported. A string field is not automatically translatable:
identifiers, prices, reference names, selection values and media references
remain ineligible. Translated text must obey the selected field's constraints;
for example, Product title is nonempty and at most 200 characters.

Targets remain within the Translation's namespace. Omitting
`targetRef.namespace` selects that namespace; an explicit different namespace
is rejected. Optional UID and resource-version pins restrict which target
incarnation/version is acceptable. A version pin also becomes stale after a
status-only target version change.

The controller remembers the first eligible target UID. Deleting and recreating
a target under the same name does not silently rebind an existing Translation.
Changing translated text or toggling pins is not a request to discard that
binding. Intentionally retarget by changing the authored reference; inspect
the resulting resolution rather than assuming a same-name replacement applies.

## Locales and identity

Locales use well-formed BCP 47 tags. Malformed tags and duplicate subtags are
rejected. Canonical locale comparison handles supported aliases and casing
without rewriting the authored locale or translated text. Two independently
named Translations may share equivalent locales and the same target; locale
is not a uniqueness key. Consumers must not infer an automatic winner,
fallback chain or locale registry from that comparison.

## Validation and status

Validation rejects a complete invalid changed document, never a partial patch
list or truncated body. The document ceiling is one MiB with at most 256
patches, 16 path segments and eight selectors per path. Existing lower receive
limits still apply. A known missing target is unresolved, not evidence that
the target exists; denied or unavailable target proof never becomes successful
resolution. Same-candidate target changes are evaluated independently of file
order. A later target change does not retroactively reject an accepted source.

Public status retains the common shape: `conditions`, `observedGeneration`,
`lastAppliedRevision` and `resolved`. `lastAppliedRevision` identifies this
Translation's reconciled source revision, not content applied to its target.
`Condition.lastTransitionTime` records condition transitions, not every check.
There is no separate checked-at timestamp or public target-evidence history.

Read-side freshness checks mask stale Ready evidence without rewriting the
accepted source or leaking private target facts. Check the current generation,
source revision and Ready condition together. A historically delivered watch
event is source history, not authority for current Ready.

## Admin and unsupported extensions

Use the Admin `translation` lookup, Relay `translations` connection and
`watchTranslations` subscription with normal permissions. Authoring remains
Git-driven. Status and binding mutations are reserved for enrolled controllers;
they are not ways for authors to force Ready or change target content.
See the [API reference](../api-reference.md#translations).

AttributeDefinition/custom-kind targets, arbitrary nested keyed fields,
positional paths, localized projections, automatic application and fallback
selection remain proposals, not accepted manifest syntax.
