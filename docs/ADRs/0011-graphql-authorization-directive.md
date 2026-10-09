# ADR 0011: Declarative GraphQL Authorization via `@authorize`

**Status**: Proposed

**Date**: 2026-09-28

**Audience**: GitStore API and GraphQL schema authors.

> **Amended by [ADR 0012](0012-admin-storefront-graphql-endpoints.md)** (separate Admin and
> Storefront GraphQL endpoints):
>
> - `@authorize` applies to **both** schemas, the same directive, builder registry (§5) and
>   middleware. Catalog fields on both use `mode: CHECK`: the Admin API's `Product`/`ProductVariant`
>   types are guarded by `product.management.read` / `productVariant.management.read`, and the
>   Storefront API's `Storefront*` types by `product.read` / `productVariant.read`.
> - §9's `SCOPE` rationale no longer covers the Product/ProductVariant catalog split, and the
>   explicit `mode: SCOPE` fields it lists (`Query.product`, `Query.products`, `Category.products`,
>   `Collection.products`, `Product.productVariants`, `ProductWatchEvent.product`) become `CHECK`.
>   The endpoint fixes the scope; no field emits one.
> - **`SCOPE` is removed, not kept unused (2026-10-01).** With the catalog split gone, `mode`/`AuthzMode`
>   had zero live consumers — the only remaining candidate, §7's list-level visibility-band filtering,
>   is still speculative. Per YAGNI, `@authorize` drops `mode` entirely (§1, §6) rather than carry an
>   enum for one unspecified future case. This supersedes the "stays in the grammar" line above; if
>   §7's case becomes concrete, reintroducing a scope-output mechanism is its own future amendment, not
>   a standing feature of this directive today. **ADR-0012's own body text, which currently repeats
>   "`SCOPE` stays in the directive grammar," needs the matching correction.**

## Context

[ADR 0010](0010-authorization-model.md) fixes the action-string vocabulary and scope model passed
to `AuthZProvider.Authorize(ctx, principal, action, ResourceContext)`. [Doc 022](../implementation/022-opa-data-authorization.md)
proposes a server-owned `@authorize` SDL directive and a `CHECK`/`SCOPE` mode for catalog reads, but
scopes it to Product/ProductVariant and leaves the directive's runtime implementation, its behavior
on abstract types, and the migration off the imperative authorizer unresolved.

Today, `gitstore-api/internal/middleware/security/graphql.go`'s `GraphQLFieldAuthorizer` is a single
~500-line `switch` over mutation/query field names, each case hand-decoding arguments, hand-fetching
the resource, and hand-building a `ResourceContext`. It has no structural limit: every new mutation,
every new kind, every new subresource adds one more case. It also has a known gap — Product/ProductVariant
reads through the polymorphic `Query.node`/`Query.nodes` fields are unauthorized, because nothing in the
switch decodes the opaque ID far enough to reach a Product-specific check consistently, and there is no
declarative place on the schema to say "reading a Product requires `product.read`" once, for every path
that can produce one.

This ADR defines the directive's concrete contract, how it behaves for interfaces and unions (`Node`
today; any future union), and the shape of the Go module that replaces the switch — so the module's size
tracks the number of *kinds*, not the number of *fields*.

## Decision

### 1. Directive shape

```graphql
directive @authorize(permission: String) on OBJECT | INTERFACE | UNION | FIELD_DEFINITION
```

This drops doc 022's separate `resource: String!` argument. [ADR 0010 §2](0010-authorization-model.md#2-canonical-action-grammar)
already guarantees the action grammar is unambiguous to parse without a vocabulary lookup
(`<kind>[.<subresource>].<verb>`), so the policy kind is derived from `permission` itself rather than
carried as a second, independently-driftable argument.

### 2. Concrete types declare their permission once, on the type

gqlgen's codegen (`codegen/field.go`'s `bindField`, `codegen/object.go`'s `buildObject`) copies a type's
own directives onto **every field whose static return type is that type**, as long as the directive's
declared locations include `OBJECT`/`INTERFACE`/`UNION`. This is existing, load-bearing gqlgen behavior
(tracked upstream as [gqlgen#2281](https://github.com/99designs/gqlgen/issues/2281) for the analogous
`INPUT_OBJECT` case), not new machinery this ADR introduces.

```graphql
type Product implements Node @authorize(permission: "product.read") {
  id: ID!
  cost: Money @authorize(permission: "product.cost.read")
}
```

`@authorize` on `type Product` applies automatically to `Query.product`, `Category.products`,
`Product.productVariants`'s edge/node — everywhere `Product` is a field's static return type — with zero
repetition. Only a field-specific override (like `cost`, a narrower permission than the type's own) needs
its own `@authorize`. This is what keeps the permission module's size bound to the number of kinds: a new
kind adds one directive usage on its own type definition, not N call sites.

`mode`/`AuthzMode` has been removed from the directive entirely (§6) — there is nothing to omit here
anymore. §9 keeps the gqlgen mechanical fact that motivated the original CHECK/SCOPE placement rule, as
background for if a scope-output mechanism is ever reintroduced.

### 3. Abstract types never declare a permission; the field that returns them does

Static propagation only reaches a field's *static* type. `Query.node(id: ID): Node` has static type
`Node` — it is never given `Product`'s directive, even when it resolves to a Product at runtime, because
gqlgen cannot know that at codegen time. There is usually no single permission that fits every
implementer of an interface or member of a union (`Node` implementers span eight+ kinds with unrelated
verbs), so `interface Node` and any future `union` declare **no** `@authorize` at all. Instead, the field
returning the abstract type carries `@authorize` with `permission` omitted:

```graphql
type Query {
  node(id: ID!): Node @authorize
  nodes(ids: [ID!]!): [Node]! @authorize
}
```

An omitted `permission` is the signal, checked by the directive implementation, to defer to the
concrete resolved type rather than a static one.

### 4. Directive implementation contract

gqlgen's generated `DirectiveObjName()` returns `nil` for every Root (`Query`/`Mutation`/`Subscription`)
field and the actual parent struct for a nested object field. The implementation branches on that, which
turns out to be the same branch needed for abstract vs. concrete types — not a separate one:

- **`obj != nil`** (a field on an already-resolved parent, e.g. `Product.cost`): the parent is already in
  memory. Build `ResourceContext` from it and call `Authorize` **before** `next(ctx)` — cheapest path,
  denies before computing the field at all.
- **`obj == nil`** (any root field, concrete or abstract return type): call `next(ctx)` first to obtain
  the value, then:
  - if the directive's own `permission` argument is non-empty (propagated from the concrete type, as in
    §2, or written directly on the field), use it as-is;
  - if empty (the `Node`/union case, §3), resolve the returned value's concrete GraphQL type name —
    reusing whatever kind-switch already exists for that field (e.g. `queryResolver.resolveNode`'s
    `kind` string, decoded from the global ID before the fetch) rather than adding a second one — and
    read that concrete type's own `permission` directly off the parsed schema (`ast.Schema.Types[name]`),
    not from a hand-maintained Go table;
  - build `ResourceContext` from the fetched value, call `Authorize`, and only then return the value or
    swap it for an error. Nothing reaches the transport before the check runs, so this leaks nothing.

Denial renders `NOT_FOUND` for reads (preserving the enumeration protection already established by
`repositoryNotFoundError`/`namespaceNotFoundError` and doc 022 §13) and `FORBIDDEN` for mutations, where
there is no pre-existing resource identity to hide.

**Per-listener schema binding (ADR 0012).** With two gqlgen exec packages — Admin and Storefront, each
with its own generated `DirectiveRoot` and its own parsed schema — the "read the concrete type's
permission off the parsed schema" step needs no request-time resolution. Each listener's handler is
constructed once, at bootstrap, from exactly one schema; a request is always routed to exactly one
listener. The directive implementation (or whatever lookup helper it uses) is simply instantiated once
per listener, closed over that listener's own schema — identical in shape to how a single-schema setup
would already have to work, just built twice. There is no point in request handling where "which schema"
is ambiguous, so nothing needs to be threaded through `ctx`.

### 5. The `ResourceContext` builder registry is per-kind and colocated

The directive needs, per GraphQL type name, a function `func(obj any) (auth.ResourceContext, error)`.
This is registered by each kind's own resolver package (`resolver/product.go`, `resolver/repository.go`,
...), not centralized in the directive package:

```go
// in resolver/product.go
func init() {
    directive.RegisterResourceContext("Product", func(p *model.Product) (auth.ResourceContext, error) {
        return auth.ResourceContext{
            Kind: "product", Name: p.Name, OwnerSub: p.CreationActor,
            Attrs: map[string]any{"namespace": p.Namespace},
        }, nil
    })
}
```

The directive package holds only the registry's storage and lookup. A new kind touches its own resolver
file and its own `.graphqls` file — never `middleware/security/graphql.go`, never the directive package.

### 6. `mode`/`AuthzMode` is removed from the directive (YAGNI)

Doc 022 and earlier drafts of this ADR carried a `CHECK`/`SCOPE` mode so the directive could, on
`SCOPE`, require `Decision.Scopes` to carry exactly one entry and thread that value through context to
the resolver (doc 022 §5.3/§6.3/§7.2) — the mechanism behind the catalog `PUBLIC`/`MANAGEMENT` split.
[ADR 0012](0012-admin-storefront-graphql-endpoints.md) removes that split's need for a per-request
scope entirely: which view a field serves is now a fact of which endpoint/schema it's compiled into,
checked with a plain `CHECK`-shaped `Authorize` call (`product.read` on the Storefront API,
`product.management.read` on the Admin API). That was `SCOPE`'s only concrete consumer. The remaining
candidate — §7's list-level visibility-band filtering — is still unspecified.

With zero live fields needing anything beyond "require `Outcome == Allow`," `@authorize` drops `mode`
entirely rather than carry an enum for one unspecified future case. `AuthZProvider.Authorize(ctx,
principal, action, resource) (Decision, error)` is unaffected either way — it was always frozen by
ADR 0010 §1 and never took a mode — so this removal is purely a directive-grammar simplification, not a
contract change. `Decision.Scopes` remains defined (doc 022 §7.1) and provider-neutral; the directive
simply never reads it today. If §7's case becomes concrete, reintroducing a scope-output mechanism —
whether by bringing `mode` back or something else — is a future amendment to make at that point, with a
concrete field to design it against, not now.

### 7. ADR 0010 §8 visibility bands need no new mechanism for single-resource reads

Private/internal/public visibility ([ADR 0010 §8](0010-authorization-model.md#8-visibility-bands-are-abac-conditions-fail-closed))
is an **input** to `Authorize` — `ResourceContext.Attrs["visibility"]`, read off the resource being
checked — not an output the resolver needs to consume. A single `Query.repository(by)` read is fully
covered by §4's existing `obj == nil` path with `mode: CHECK`: the per-kind builder (§5) for `Repository`
adds one more `Attrs` key, `Authorize` is called once, and denial on a non-visible repository already
renders `NOT_FOUND` per §4. Nothing about visibility bands requires `mode: SCOPE` for this case.

Listing (`Query.repositories(namespace)`) is different: filtering private/internal resources a caller
cannot see out of a paginated list cannot be done by calling `Authorize` per row and dropping denials
post-fetch (the same anti-pattern doc 022 §12.2 forbids for catalog reads — it breaks `totalCount`,
cursors, and requires `ALLOW FILTERING`). Unlike Product's publication snapshot, a visibility band is not
a fixed two-way materialized split — "private" is owner-specific. Filtering a list by visibility band is
therefore the same *shape* of problem `catalog.visibility` used to solve, just with a richer payload than
one `PUBLIC`/`MANAGEMENT` enum (a set of allowed bands, plus possibly a `resourceNames` allow-list for
individually-granted private resources). This ADR does not implement that today, and — now that `mode`
has been removed (§6) — doesn't have a scope-output mechanism sitting ready for it either. When this
becomes concrete, it needs: a new entry in doc 022 §7.1's scope registry (e.g. `repository` /
`visibility.bands`), a resolver-side typed query (`AuthorizedRepositoryListQuery`, mirroring
`AuthorizedProductQuery`), and a directive-grammar amendment to carry the scope back out — three things
to design together at that point, against a real field, rather than a mechanism kept warm on spec.

### 8. Forward compatibility with a ReBAC/OpenFGA provider

Per [ADR 0010 §14](0010-authorization-model.md#14-ownership-assignment-and-transfer), a Zanzibar-style
provider evaluates `Authorize` against its own relation-tuple graph rather than reading
`ResourceContext.OwnerSub`/`Attrs` as authority. The directive's `CHECK`-only call maps to a single
`Check(user, relation, object)` — nothing provider-specific. A future scope-output mechanism (§7) would,
under such a provider, answer a small bounded number of `Check`s against a synthetic namespace-scoped
object plus a bounded `ListObjects` call for individually-granted private resources — OpenFGA has no
partial evaluation either, so this would be the adapter's job regardless of which provider ends up
implementing §7, not something to design now.

### 9. `mode: SCOPE` is declared per field, never at `OBJECT`/`INTERFACE`/`UNION` level

> **Amended by ADR 0012 §3, and by §6 above.** The catalog motivation below is historical. With separate
> endpoints the Admin API's `type Product` is a `CHECK` on `product.management.read`, and "a caller
> denied their own just-created resource" is prevented by role design (authoring roles include
> `product.management.read`, ADR 0010 §5) rather than by where `SCOPE` is declared. §6 then removed
> `mode`/`SCOPE` from the directive entirely, so there is no live placement rule to follow today. This
> section is kept as the record of *why* that rule existed — the gqlgen mechanical fact that stacking a
> field-level directive can't suppress one propagated from the return type — in case a scope-output
> mechanism is ever reintroduced (§7) and needs the same placement discipline.

Every core mutation returns a Relay-style payload wrapper that nests the kind rather than returning it
directly — `createProduct(input): CreateProductPayload!` where `CreateProductPayload { product: Product }`
(`shared/schemas/product.graphqls`). §2's static propagation therefore never reaches `Mutation.createProduct`
itself (its static type is the payload, not `Product`) — but it **does** reach `CreateProductPayload.product`,
because that field's static type genuinely is `Product`.

This matters because `SCOPE` mode's fail-closed rule (§6) requires exactly one `DecisionScope` — for
Product/ProductVariant, that means holding `product.management.read` to see an unpublished row. If
`type Product`'s own directive carried `mode: SCOPE`, a caller who just created a Product — necessarily
unpublished at that instant — would have `payload.product` deny with `NOT_FOUND` immediately after a
successful create, unless they separately held the management entitlement. Declaring an explicit
`mode: CHECK` override directly on `CreateProductPayload.product` does not fix this: gqlgen does not
deduplicate directives by name when a field's own explicit directive coexists with one propagated from
its return type (`codegen/field.go`'s `bindField` appends, never replaces) — both directive instances run,
independently, and both must pass. Stacking cannot suppress a propagated `SCOPE` requirement.

**Rule: `@authorize(mode: SCOPE, ...)` is only ever declared explicitly, on the specific field that drives
a scoped datastore query or is itself a scope-sensitive payload/event field** — `Query.product`,
`Query.products`, `Category.products`, `Collection.products`, `Product.productVariants`,
`ProductWatchEvent.product` (§10). The type-level directive on `Product`/`ProductVariant` stays
`mode: CHECK` (the default, as revised in §2), which is always safe to inherit anywhere that type
appears, including every mutation payload. The minor cost is that scope-driving fields end up with two
stacked directive instances — the inherited `CHECK` and the explicit `SCOPE` — calling `Authorize` twice
for the same permission; correctness-wise this is redundant, not wrong, and is preferable to a directive
implementation that tries to guess whether it was invoked via propagation or explicit declaration (it
cannot — both produce an identical call signature at runtime).

### 10. Mutations and Subscriptions

**Mutations.** A mutation root field's own verb (`product.create`, `product.update`, `product.delete`,
`product.status.write`, `product.purge`) is never inherited from anything — it is always an
explicit `@authorize(permission: ..., mode: CHECK)` directly on the `Mutation` field, exactly mirroring
the corresponding case already in `GraphQLFieldAuthorizer`'s switch today. Because mutation fields return
payload wrappers (`CreateProductPayload`, `UpdateProductPayload`, `DeleteProductPayload`,
`UpdateProductStatusPayload`, ...), §2's propagation never touches the mutation field itself — only the
payload's nested kind field, which inherits the type's `CHECK`-only base permission per §9. A role that
grants `product.create` without `product.read` will see the mutation succeed with `payload.product` denied
— an existing consequence of ADR 0010's separately-grantable verbs, not something this ADR changes.

**Subscriptions.** GitStore has two subscription shapes, and they get different guarantees. (Per ADR
0012, both live on the Admin API, where `ProductWatchEvent.product` is a `CHECK` on
`product.management.read` rather than a `SCOPE` field. Storefront subscriptions are deferred.)

- *Dedicated, typed* (`watchProducts(...): ProductWatchEvent!`, `ProductWatchEvent { product: Product }`,
  `shared/schemas/product.graphqls`): the root field needs its own explicit, coarse
  `@authorize(permission: "product.watch", mode: CHECK)`, evaluated once at subscribe time — gqlgen calls
  a field's directive when the field resolver is invoked to *produce* the event stream, not once per
  emitted value. The nested `product` field, however, is resolved fresh for **every emitted event**, so
  it inherits `Product`'s type-level `CHECK` directive (`product.management.read` on the Admin API, per
  ADR 0012) and re-runs that check per event, with no extra directive needed. This gives per-event
  re-authorization for free, stronger than doc 022 §10's stated connection-scoped-decision model: a
  caller whose `product.management.read` grant is revoked mid-stream stops receiving `product` payloads
  without waiting for reconnect.
- *Generic* (`watchResources(kind: String!, ...): WatchEvent!`, `WatchEvent { object: JSON }`,
  `shared/schemas/schema.graphqls`): `object` is untyped JSON — there is no field for a return-type
  directive to attach to, so no per-event re-authorization is structurally possible. The only gate is the
  root field's explicit `@authorize`, dispatched on the `kind: String!` **argument** itself (available
  synchronously, before `next(ctx)` — a third, simpler dispatch variant alongside §3's post-resolve
  type-name lookup and §4's parent-object case), checked once at subscribe time. This is a permanent
  ceiling, not a gap in this design: **any kind whose events are catalog-visibility-sensitive
  (Product, ProductVariant) must be watched through its dedicated typed subscription, never the generic
  `watchResources` path, if publication-eligibility-aware filtering matters.**

### 11. Non-goals

- No OPA partial evaluation or residual-to-CQL translation — doc 022 §2 already rules this out; OPA (or
  any provider) returns discrete decision data, never a query filter, and this ADR does not revisit that.
- No change to `AuthZProvider.Authorize`'s signature or to `ResourceContext`'s fields.
- No new scope names/values are added to doc 022 §7.1's registry by this ADR; §7's `visibility.bands`
  sketch is illustrative of the pattern, not a commitment to implement it now.
- No `mode`/`AuthzMode` or scope-output mechanism remains in the directive (§6, removed 2026-10-01 for
  YAGNI) — reintroducing one is deferred until §7's case is actually specified.
- No per-event visibility filtering for the generic `watchResources` JSON path (§10) — this is a
  structural ceiling of an untyped payload, not something a future revision of this directive can lift.

## Migration path off `middleware/security/graphql.go`

1. Land the directive, the registry, and the per-kind builders additively — both the directive and the
   existing `GraphQLFieldAuthorizer` enforce in parallel; nothing is deleted yet. Per ADR 0012, this
   happens once per listener (Admin, Storefront), each instance closed over its own schema (§4).
2. Port the most uniform switch cases first (`serviceAccount.*` mutations), deleting each case as its SDL
   equivalent lands.
3. Port `product`/`products`/`node`/`nodes` next — the actually-polymorphic paths — since they are the
   highest-value and highest-risk slice (the currently-ungated Product-via-`node` gap from the Context
   section) and prove out §4's dispatch before the rest follow the same pattern.
4. Port `repository`/`namespace`/`category`/status-mutations similarly. Each mutation field gets its own
   explicit verb directive per §10, independent of whatever its payload's nested kind field declares.
   Dedicated typed subscriptions (`watchProducts`, `watchCategories`, ...) get a coarse root-field
   directive plus rely on their nested kind field's already-ported directive for per-event enforcement;
   the generic `watchResources` keeps its existing `kind`-argument dispatch, ported as an explicit
   `@authorize` rather than a switch case.
5. Once every switch case has an SDL equivalent, delete `GraphQLFieldAuthorizer`,
   `isRepositoryQueryField`, `isProductQueryField`, `graphqlFieldRequiresAuthorization`, and
   `authorizeRepositoryField`/`authorizeProductQueryField`/`authorizeProductNodeField`. `GraphQLAuthenticator`
   (authentication, unrelated) and `GraphQLResponseAuthorizer` (terminal audit log) are unaffected.
6. Move the `authorizationLedger`'s `begin()`/`finish()` calls from the current
   `graphqlFieldRequiresAuthorization` allowlist into the directive implementation itself, so a new
   `@authorize`d field is self-registering for the audit ledger instead of silently invisible if a future
   author forgets to add it to a switch.

## Consequences

- The permission module's size tracks the number of GraphQL *kinds*, not the number of fields or
  mutations: a new kind adds one `@authorize` usage on its type and one small builder function in its own
  resolver package, never a new case in a shared file.
- The Product-via-`node`/`nodes` authorization gap identified in doc 022 §3 has a concrete, general
  mechanism to close, rather than a per-field patch.
- The directive carries only `permission` (§1, §6) — a mutation payload or any other field that merely
  echoes an already-fetched or already-written resource always inherits the type's plain `CHECK`
  permission; there is no second mode that could deny a caller their own just-created resource.
- Dedicated typed subscriptions get per-event re-authorization as a side effect of ordinary field
  propagation (§10), with no extra mechanism; the generic `watchResources` path does not and structurally
  cannot, which is now an explicit, documented constraint rather than an implicit assumption.
- The directive contains no provider-conditional code; behavior under `rbac-local`, OPA, or a future
  ReBAC provider differs only in what each provider's adapter populates in `Decision.Scopes`.
- **Not addressed here (deferred):** the concrete `visibility.bands` scope registry entry and
  `AuthorizedRepositoryListQuery` type for list-level visibility filtering (§7); the OpenFGA adapter
  itself; the per-field migration PRs described above.

## References

- [ADR 0010 — Authorization Model](0010-authorization-model.md)
- [ADR 0009 — Credential and Secret Material Boundary](0009-credential-secret-boundary.md)
- [Doc 020 — Pluggable AuthN/AuthZ Architecture](../implementation/020-pluggable_auth_architecture.md)
- [Doc 022 — OPA Data-Aware Authorization](../implementation/022-opa-data-authorization.md)
- [Publication Lifecycle](../products/publication-lifecycle.md)
- Live GraphQL authorization module: `gitstore-api/internal/middleware/security/graphql.go`
- gqlgen directive/type-directive propagation:
  `codegen/field.go` (`bindField`, `ImplDirectives`), `codegen/object.go` (`buildObject`) in
  `github.com/99designs/gqlgen@v0.17.90`; see also
  [gqlgen#2281](https://github.com/99designs/gqlgen/issues/2281)
- gqlgen directives reference: [Schema Directives](https://gqlgen.com/reference/directives/)
- OpenFGA APIs: [Check](https://openfga.dev/api/service#/Relationship%20Queries/Check),
  [ListObjects](https://openfga.dev/api/service#/Relationship%20Queries/ListObjects)
