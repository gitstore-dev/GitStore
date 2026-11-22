# Translation Authoring and Verification Walkthrough

**Unreleased and activation-gated:** these examples exercise the development
implementation. Do not assume an existing deployment is capable: verify every
enabled target writer is fenced and every read/replay role is capable before
enabling admission consistently. This walkthrough does not establish sustained,
rolling-upgrade or production acceptance.

## Prerequisites

Use an explicitly owned test repository in namespace `acme-store`, matching
Git-backed targets, and an actor allowed to author Translations and read those
targets. For replica verification use two APIs/two controllers with separate
checkpoint directories and exactly one Git service. Use existing repository
setup, authentication and Git remote procedures; no new CLI is required.

The Product `laptop` must have `spec.title` and an option with `name: color`
and a `title` string. The File `laptop-hero` must be a valid existing File.
Neither target needs to be changed by this walkthrough.

## Author the Selected Contract

Create `laptop-details-de-de.md` in that catalog repository:

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

# Tragbarer Computer

Ein Computer mit einer Auswahl an Farben.
```

The blank line immediately after the closing delimiter is part of the body.
To make a field-only document, omit the body; to make it body-only, omit
`patches` and keep meaningful body content. `translation: ""` is explicit
empty text and works only on fields whose own contract permits it.

For descriptive File text, use `laptop-hero-de-de.md`:

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

File has no `spec.title`. Translating its descriptive body does not change
binary bytes, URI, checksum or a consumer's media slot.

After verifying capability and activation, use the repository's normal Git workflow:

```sh
git add laptop-details-de-de.md laptop-hero-de-de.md
git commit -m "feat(catalog): add German descriptive translations"
git push
```

These are ordinary Git commands run in the owned catalog repository, not
commands to execute against this source checkout.

## Verify the Result

Use the typed Admin Translation read/Node/list routes following the
existing authenticated GraphQL workflow; the lifecycle contract defines their
required integration. Inspect both APIs and join the accepted commit/blob
digest to Translation namespace/name, UID, generation, raw locale, patches and
exact body. Do not substitute `__typename`/health checks for domain assertions.

Require `Accepted`, `LocaleValid`, `TargetResolved`, `FieldsValid` and guarded
current `Ready` for the expected observed generation/revision. Inspect
the condition reasons, `resolved.canonicalLocale` and the target UID/material
evidence when authorized. Status uses the same four-field baseline as other
resources; freshness checking does not add a timestamp or a second status copy.
Verify common `status.lastAppliedRevision` matches the Translation source
revision reconciled into that observation, not the target revision; accepted
source attribution remains independently visible before reconciliation.
Ready means validated/resolved, not applied: the original Product and File
source/resolved digests must remain unchanged.

For optional lifecycle checks, create separately named, disposable targets and
Translations in the explicitly owned fixture. Never modify the retained
baseline or the original targets used above. On those disposable resources,
reorder the Product options without changing their identities: the `color`
selector must still identify the same title. Remove the selected `color`
member: the path is now missing and the Translation must become not Ready
without changing its authored generation or reconciled source revision.
Omitting only the option's optional title represents empty text under the
existing Product contract; it does not remove the canonical text leaf.
Restore the member and verify current Ready returns after reconciliation.
Delete/recreate the target name: observe TargetReplaced/not Ready, then
explicitly author the intended new `targetRef.uid` to retarget. Never erase
system status or assume an unrelated Translation edit authorizes replacement.

## Verify Negative and Deferred Cases

In separate rejected candidates, try `en_US`, duplicate paths,
`spec.options{color}` (object terminal), `spec.media{hero-image}.fileRef.name`
(reference identity), an unknown member and authored status. Expect bounded
location-specific errors and no accepted patch subset. Check absent permitted
targets are accepted unresolved and target lookup errors are not mislabeled
as absence.

Under current target contracts, the proposed keyed path
`spec.options{size}.inline.values{m}.title` is not supported. Direct
AttributeDefinition and custom-kind targets remain unsupported. They are
design fixtures for future contract work, not additional commands to make
this walkthrough succeed. Shared-definition extraction and release/custom-kind
handoffs are documented in [data-model.md](data-model.md).

## Contributor Acceptance

Follow [capacity-and-rollout.md](contracts/capacity-and-rollout.md) for the
failing-first test matrix, exact corpus/limits, connected driver extensions,
full acceptance windows, faults and safe activation. Run existing scoped
test/lint/generation commands for implementation and `make pr-ready` before
an implementation PR. No application, fallback or winner selection is included.
