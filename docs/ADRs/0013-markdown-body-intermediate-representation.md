# ADR 0013: Markdown Body Contract and Intermediate Representation

**Status**: Proposed

**Date**: 2026-09-30

**Audience**: GitStore API, schema, storefront, admin UI, and catalog-publication authors.

## Context

Every Git-backed kind carries a Markdown body under its YAML frontmatter: Product, ProductVariant,
Category, Collection, Namespace, Repository and File. Today the body is an opaque string:

- `gitstore-api/internal/catalog` splits the frontmatter from the body and stores the rest
  unchanged (`datastore.*.Body string`).
- The GraphQL schema exposes it as `body: String`, and says it's Markdown only in the field
  descriptions.
- No component parses Markdown. Neither `gitstore-api/go.mod` nor `gitstore-git-service/Cargo.toml`
  has a Markdown dependency.

Two upcoming needs change this:

1. **Interpolation.** Bodies will reference data (for example a variant's SKU, a media asset, a
   linked Product) instead of repeating it. Unevaluated template syntax must not reach buyers, and
   the result must not change underneath a published release.
2. **Storefront control.** The Storefront API ([ADR 0012](0012-admin-storefront-graphql-endpoints.md))
   serves custom storefronts. These want to control the look of the description and how inline
   media, links and product references render. Pre-rendered HTML takes that control away.

Other platforms handle this in different ways:

| Platform                                                  | Body type                                        | Model                                                |
|-----------------------------------------------------------|--------------------------------------------------|------------------------------------------------------|
| Shopify                                                   | `descriptionHtml: HTML!`, `description: String!` | server-rendered HTML plus plain text                 |
| Saleor                                                    | `JSONString`                                     | Editor.js block JSON authored in a WYSIWYG dashboard |
| Shopify rich-text metafields, Contentful, Hygraph, Sanity | JSON tree plus typed references                  | structured document the client renders               |

GitStore's source of truth is Markdown in Git, so the structured document has to be derived from
Markdown rather than authored in a JSON editor.

## Decision

### 1. Three representations, one authority

| Representation | What it is                                            | Authority                    | Where it lives                                                             |
|----------------|-------------------------------------------------------|------------------------------|----------------------------------------------------------------------------|
| **Source**     | Markdown as authored                                  | authoritative                | Git, and the admitted row's `Body`                                         |
| **IR**         | intermediate representation: the parsed document tree | derived, never authoritative | computed by `gitstore-api`; resolved IR is stored in publication snapshots |
| **Output**     | HTML, native views, email, …                          | client-owned                 | storefront, admin UI or other client                                       |

- The IR is never written to Git and never edited directly. It is reproducible from the source, a
  parser version and (once resolved) a release's dependency closure.
- **GitStore does not render output.** No endpoint returns server-rendered HTML for a body.
  Clients render the IR with their own components.

### 2. `Markdown` scalar

```graphql
# shared/schemas/common/scalars.graphqls
"""
CommonMark 0.31.2 with GitHub Flavored Markdown extensions (tables, task lists, strikethrough,
autolinks). Serialized as a JSON string.
"""
scalar Markdown @specifiedBy(url: "https://github.github.com/gfm/")
```

- Every `body: String` in the admin schema becomes `body: Markdown`. On the wire this is the same
  JSON string, but schema-diff tools and typed code generators treat it as a breaking change. It
  ships together with ADR 0012's move to `shared/schemas/admin/`, and `gitstore-admin/codegen.yml`
  maps `Markdown` to `string`.
- The pinned flavor is the contract for every parser that must agree with GitStore's: the
  admission parser, the admin UI's live preview and any client that re-parses `body`.
- Interpolation syntax is an extension of this flavor. Its grammar is decided by the interpolation
  spec (§6) and added to the scalar description when it ships.

### 3. The IR

**Wire format:** JSON, exposed through the existing `JSON` scalar. This is independent of the node
vocabulary, which isn't decided yet.

**Vocabulary requirements.** The chosen node vocabulary must:

1. map every construct of the §2 flavor to a node, and serialize back to Markdown for every
   construct that has no GitStore extension;
2. support **extension nodes** for interpolation expressions and for references to other
   resources and media;
3. carry source positions (line/column) for admin diagnostics, which are removable for the
   storefront;
4. serialize deterministically (stable key order, no insignificant whitespace), because the
   resolved IR is part of a snapshot digest;
5. be versioned as a whole (`schemaVersion`, §5);
6. have maintained Go tooling (the producer) and JavaScript tooling (the most common consumer).

**Candidate (not decided): [mdast](https://github.com/syntax-tree/mdast).** It is a specified tree
for CommonMark/GFM. The directive convention (`mdast-util-directive`) is a ready fit for
interpolation and reference nodes, and the JavaScript tooling is the most widely used. Portable
Text, ProseMirror and Editor.js are editor-first models. They assume a WYSIWYG editor is the
source, and they don't map one-to-one to Markdown. The final choice belongs to the interpolation
spec. It must satisfy the requirements above and doesn't change §4–§5.

### 4. Pipeline: parse at admission, resolve at publication

```text
Git push ─▶ admission: parse source ─▶ unresolved IR ─▶ syntax, limit and reference validation
                                                         (diagnostics; the IR itself isn't stored)
publish  ─▶ resolve IR against the release's dependency closure ─▶ resolved IR in the snapshot
read     ─▶ Storefront returns the snapshot's resolved IR; per-request work only for contextual nodes
```

- **Admission** parses every body so that malformed interpolation, unknown references and
  oversized documents are rejected at push time with source positions. Nothing new is persisted
  for the admitted row. The source remains the only stored form.
- **Publication** evaluates interpolation against the same immutable dependency closure the
  snapshot already captures ([Publication Lifecycle](../products/publication-lifecycle.md)). It
  drops raw-HTML nodes (§5) and strips source positions. It also collects the references, stores
  the resolved IR in the snapshot and includes it in the snapshot digest. A published body can't
  change without a new release.
- **Contextual values stay nodes.** Values that depend on the buyer, market, currency or time, such
  as price or availability, can't be fixed in a snapshot. Publication leaves them as typed
  reference nodes that clients resolve through ordinary Storefront GraphQL fields. Nothing is
  evaluated inside the IR at request time.
- **Multi-replica and rolling upgrade.** The parser and resolver are pinned by version. Replicas
  running different versions may parse differently, but that affects only:
  - admission diagnostics, which are request-scoped and not persisted;
  - releases published by that replica, which record the `schemaVersion` they were built with.

  An existing snapshot is never re-resolved by a newer replica. A parser change that alters the IR
  for unchanged source requires a new `schemaVersion` (§5).

### 5. Exposure per endpoint

|                                  | Admin API                                                                                              | Storefront API                                                                                             |
|----------------------------------|--------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------|
| `body: Markdown`                 | the **source** as authored, including interpolation syntax                                             | the **resolved** IR serialized back to Markdown. No template syntax. For clients that want plain Markdown. |
| `bodyDocument: MarkdownDocument` | **deferred.** Unresolved IR with positions and diagnostics, for editor preview and linting. Read-only. | **the primary body contract.** Resolved IR from the snapshot.                                              |
| HTML                             | none                                                                                                   | none                                                                                                       |

```graphql
# shared/schemas/common/markdown.graphqls
type MarkdownDocument {
  "IR contract version, e.g. \"gitstore.dev/markdown/v1\"."
  schemaVersion: String!
  "The IR tree (§3)."
  ast: JSON!
}

# shared/schemas/storefront/*.graphqls
type StorefrontMarkdownDocument {
  schemaVersion: String!
  ast: JSON!
  "Entities the tree references, selected through GraphQL rather than embedded in `ast`."
  references(first: Int, after: String): StorefrontMarkdownReferenceConnection!
    @listSize(slicingArguments: ["first"])
}

union StorefrontMarkdownReference = StorefrontMedia | StorefrontProduct | StorefrontProductVariant
  | StorefrontCollection | StorefrontCategory
```

- **Why the tree is `JSON`.** GraphQL can't select a tree of arbitrary depth without fixed-depth
  fragments, which is why Contentful and Hygraph use a JSON scalar too. The typed part of the
  document is `references`. Reference nodes in `ast` carry the referenced entity's storefront
  global ID ([ADR 0012 §5](0012-admin-storefront-graphql-endpoints.md#5-global-ids-are-endpoint-specific)),
  and clients join on it.
- **Why references are a connection.** Linked entities stay subject to the storefront's own
  types, authorization and [doc 038](../implementation/038-graphql-cost-and-rate-limits.md) cost
  analysis. Embedding them in `ast` would hide them from all three. A reference to an entity that
  isn't in the active snapshot is absent from `references`, with the same `NOT_FOUND`
  indistinguishability as a direct lookup.
- **Why no raw source on the Storefront.** Once interpolation exists, the source contains
  unevaluated expressions that can name management-only data or reveal template logic. The
  Storefront's `body` is therefore always derived from the resolved IR.

**IR contract rules** (the Storefront IR is a public API):

- **Additive evolution.** New node types and new optional node properties don't change
  `schemaVersion`.
- **Unknown node types must not break clients.** A client renders the node's children, or skips
  the node if it has none. It never fails the document. This rule is part of the published
  contract.
- **Breaking changes bump `schemaVersion`.** Removing or renaming a node type or property, or
  changing its meaning, is breaking. So is a parser change that alters the IR of unchanged source.
  Old snapshots keep their version.
- **No raw HTML in the Storefront IR.** Raw HTML in the source is kept as an `html` node in the
  admin IR (so authors see it) and dropped at publication. Clients never receive markup from
  GitStore, and there is no server-side sanitizer to keep correct.
- **Bounded size.** Admission enforces a maximum source size, IR node count, nesting depth and
  reference count. `JSON` weighs 0 under doc 038's defaults, so these bounds are what keep a
  body's response size proportional to its cost. The values are set by the implementation spec.

### 6. Interpolation boundary

This ADR fixes where interpolation runs, not its syntax:

- Expressions are IR extension nodes, parsed at admission and evaluated at publication (§4).
- Evaluation is pure and reads only the release's dependency closure. There's no I/O and no
  wall-clock time, and it can't see management-only fields.
- Anything that can't be evaluated deterministically at publication is a contextual reference
  node (§4), not an expression evaluated per request.
- The expression grammar, the variables available to it and its error semantics belong to a
  separate interpolation spec.

## Alternatives Considered

1. **Pre-rendered HTML (Shopify `descriptionHtml`).** Rejected. It takes rendering control away
   from custom storefronts: typography, inline media, component-based product links. It also makes
   GitStore responsible for an HTML sanitizer on a public surface.
2. **Raw Markdown only.** Rejected once interpolation exists. Every storefront would re-implement
   the parser and evaluator, and unevaluated expressions would be public. It is kept as the Admin
   API contract and as a derived convenience on the Storefront.
3. **Fully typed GraphQL node tree** (`union MarkdownNode = Paragraph | Heading | …`). Rejected.
   Recursive documents can't be selected without fixed-depth fragments, and every new node type
   would be a schema change.
4. **Editor-first IR as the source (Saleor's Editor.js, Portable Text, ProseMirror).** Rejected as
   the source of truth, because Git holds Markdown. These remain possible vocabularies only if they
   meet §3's requirements, which they don't do one-to-one today.
5. **Store the IR in Git or in the admitted row.** Rejected. A derived artifact next to its source
   drifts from it. Only the snapshot stores the IR, because only a release needs it fixed.

## Consequences

- Storefronts get structure and typed references, and decide their own presentation.
- A published body is as immutable and digest-covered as the rest of the snapshot, including the
  results of interpolation.
- The admin contract changes only in type (`String` → `Markdown`). The source stays in the same
  place.
- Costs:
  - A Go Markdown parser becomes a dependency of `gitstore-api`, chosen by the implementation spec.
    It must support the §2 flavor and produce the §3 vocabulary.
  - Admission does more work per body, bounded by §5's size limits.
  - Snapshots grow by one resolved IR per body.
  - The IR contract has to be versioned and documented for storefront authors.
- **Follow-up edits:**
  - [ADR 0012 §2](0012-admin-storefront-graphql-endpoints.md#2-the-schemas-are-disjoint-types-are-not-shared-across-the-boundary):
    `Markdown` and `MarkdownDocument` are `common/` types.
  - [Publication Lifecycle](../products/publication-lifecycle.md): the snapshot includes each
    body's resolved IR, and the snapshot digest covers it.
  - `docs/products/*-spec.md`: the body is CommonMark + GFM per §2, and raw HTML isn't published.
- **Deferred:**
  - the IR vocabulary (mdast is the candidate);
  - the interpolation grammar and evaluation spec;
  - the Admin API's `bodyDocument`;
  - concrete size limits;
  - the Go parser library.

## References

- [ADR 0012 — Separate Admin and Storefront GraphQL Endpoints](0012-admin-storefront-graphql-endpoints.md)
- [Doc 038 — GraphQL Cost Analysis, Rate Limiting, and Request Limits](../implementation/038-graphql-cost-and-rate-limits.md)
- [Publication Lifecycle](../products/publication-lifecycle.md)
- [Product spec](../products/product-spec.md)
- [CommonMark 0.31.2](https://spec.commonmark.org/0.31.2/), [GitHub Flavored Markdown](https://github.github.com/gfm/)
- [mdast](https://github.com/syntax-tree/mdast) and [mdast-util-directive](https://github.com/syntax-tree/mdast-util-directive)
- Shopify, [`HTML` scalar](https://shopify.dev/docs/api/storefront/latest/scalars/HTML) and rich-text metafields
- Saleor, `JSONString` (Editor.js) rich text
- Contentful, [Rich Text](https://www.contentful.com/developers/docs/concepts/rich-text/) (JSON document plus `links`)
- Hygraph, [Rich Text field](https://hygraph.com/docs/api-reference/schema/field-types#rich-text) (`json`/`raw` plus `references`)
- Live code: `gitstore-api/internal/catalog` (frontmatter/body split), `gitstore-api/internal/datastore/entities.go` (`Body string`)
