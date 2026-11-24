# API Surface Ownership and Sequencing (Proposed)

The query language, typed predicate meaning, provider interfaces and execution budgets are shared infrastructure. Public schemas, resource representations, access policy and enabled operations belong to each API. Shared infrastructure does not mean identical exposed schemas or shared visibility.

| Concern | Admin | Storefront |
| --- | --- | --- |
| Structured connections | Management resources under existing namespace/parent authorization. | Only published/readable resource representations defined by the first substantive Storefront contract. |
| Lexical/semantic/hybrid/similarity search | Useful for catalog management when explicitly enabled. | Customer-facing discovery over eligible published data when enabled. |
| Recommendation serving | Not exposed by this feature. Owning features may manage models/surfaces/readiness here. | Serving belongs here, under owning surface/context/privacy rules and supported provider capabilities. |
| Generic search | SearchItemConnection with Admin-local canonical resource union members. | Same Relay naming/shape convention, with Storefront-local eligible union members determined by its owning contract. Never import Admin objects. |
| Watch | Existing authorized management/controller CDC journal streams. | This feature does not expose journal watches publicly. Any future Storefront subscription needs its own contract. |
| Scope | Explicit authorized namespace and inherited parent relationships. | Trusted publication/target binding and visibility policy; a supplied namespace/filter cannot establish authority. Argument shape follows the owning Storefront baseline. |
| Result hydration | Authoritative management datastore/resource owner. | Authoritative publication/read model; never hydrate a published hit from current Admin drafts. |

## Shared registry, separate exposure

Registrations include API surface, operation, eligible kinds/fields, scope policy, retrieval/order combinations, representation revision and limits. Public capability discovery projects each surface through spec 072's endpoint IDs. The same provider can support different subsets on each endpoint; installation never grants exposure. A Storefront request cannot address Admin-only kinds, fields, private surfaces or predicates through generic search.

Provider documents/index routes carry audience/publication identity and revision; draft and published sources are isolated. Storefront serving binds trusted publication target/revision and applicable caller/context policy into the plan, cursor and ranked window. Returned nodes, edges and result-derived metadata must all honor published visibility. Sharing an engine does not justify cross-surface cache reuse. Recommendation context authority and any channel/market/customer policy belong to the first Storefront and owning recommendation contracts, not invented arguments here.

## Current source and sequencing

Current `shared/schemas/storefront/schema.graphqls` is boundary-only (`node` and ID-only StorefrontProduct); `gitstore-api/internal/storefront/handler.go` returns Storefront unavailable. These are not a substantive product/search/recommendation contract.

Recommended sequence: first ship the authoritative Storefront read/representation/target-binding contract; then rebase this plan's Storefront schema, publication filtering, recommendation context, cache/cursor identity and endpoint-specific capacity/security fixtures on that contract. The user's possible delay is a sequencing recommendation, not a claim that a date or release has been committed.

`admin.graphqls` remains an explicitly Admin-only replacement overlay for known source types. It is not the schema for the entire feature and must not be mounted on Storefront. Do not fabricate a Storefront SDL overlay before its owning types/arguments exist. The recommendation directive in the shared grammar is reserved design intent and remains unadvertised until its Storefront serving contract is defined and validated. Review that directive/context syntax against the first Storefront contract before freezing its public syntax. Internal parser/index work may be reusable sooner, but do not treat the current Admin overlay as proof the cross-API plan is final or ready for implementation task generation.
