# Data Model: Controller Watch API and Status Subresource Contract

No new datastore tables or columns. This spec adds GraphQL-layer types/inputs and one new in-process (non-persistent) server-side entity (the event bus buffer). All persisted fields (`resource_version`, `status` JSON blob) already exist on `CategoryTaxonomy` (see `gitstore-api/internal/datastore/entities.go`).

## GraphQL Types (new)

### WatchEvent

Delivered by both `watchCategoryTaxonomies` and the generic `watchResources`.

| Field | Type | Notes |
|---|---|---|
| `type` | `WatchEventType!` | `ADDED` \| `MODIFIED` \| `DELETED` \| `BOOKMARK` — maps 1:1 to `internal/listwatch.EventType` on the controller-manager side |
| `kind` | `String!` | Resource kind, e.g. `"CategoryTaxonomy"` |
| `namespace` | `String` | Resource namespace |
| `name` | `String!` | Resource name |
| `resourceVersion` | `String!` | Opaque cursor value at this point in the stream; always present, including on `BOOKMARK` |
| `object` | `JSON` | Full resource payload for `ADDED`/`MODIFIED`; `null` for `DELETED`/`BOOKMARK`. For `CategoryTaxonomy`-specific `watchCategoryTaxonomies`, resolvers additionally expose a strongly-typed `category: Category` field alongside `object` so core-kind consumers are not forced through JSON (see contracts/watch-api.graphql) |

### WatchEventType (enum)

`ADDED`, `MODIFIED`, `DELETED`, `BOOKMARK` — direct mirror of `internal/listwatch.EventType` (`Added`, `Modified`, `Deleted`, `Bookmark`).

### LabelSelectorInput / LabelSelectorRequirementInput (new — input mirrors of existing output types)

The existing `LabelSelector`/`LabelSelectorRequirement` (`collection.graphqls`) are output-only `type`s. Watch subscriptions need an **input** variant to accept a selector as an argument. New:

| Type | Field | Type |
|---|---|---|
| `LabelSelectorInput` | `matchLabels` | `[KeyValuePairInput!]` |
| | `matchExpressions` | `[LabelSelectorRequirementInput!]` |
| `LabelSelectorRequirementInput` | `key` | `String!` |
| | `operator` | `LabelSelectorOperator!` (existing enum, reused) |
| | `values` | `[String!]` |
| `KeyValuePairInput` | `key` | `String!` |
| | `value` | `String!` |

### UpdateCategoryTaxonomyStatusInput

| Field | Type | Notes |
|---|---|---|
| `name` | `String!` | Target resource identity (with `namespace`) |
| `namespace` | `String!` | |
| `resourceVersion` | `String!` | Required precondition (FR-009) — mirrors `status.StatusPatch.ResourceVersion` |
| `observedGeneration` | `Int` | Nil = unchanged. Mirrors `status.StatusPatch.ObservedGeneration *int64` |
| `lastAppliedRevision` | `String` | Nil = unchanged |
| `conditions` | `[ConditionInput!]` | Nil = unchanged; non-nil = full replacement of the conditions slice, per existing `StatusPatch.Conditions` partial-merge semantics (spec 026 contract) |
| `resolved` | `ResolvedCategoryTaxonomyInput` | Nil = unchanged. Kind-specific — NOT part of any generic patch shape |

Deliberately has **no** `spec` or author-controlled `metadata` field — see research.md R6 (structural enforcement of FR-010).

### ConditionInput

Mirrors the existing `CategoryCondition` output type and the controller-manager's `internal/status.Condition`: `type: String!`, `status: String!`, `observedGeneration: Int!`, `lastTransitionTime: DateTime!`, `reason: String`, `message: String`.

### ResolvedCategoryTaxonomyInput

Mirrors the existing `ResolvedCategoryTaxonomy` output type: `depth: Int!`, `ancestorPath: String!`, `childCount: Int!`, `productCount: Int!`.

### UpdateCategoryTaxonomyStatusPayload

| Field | Type | Notes |
|---|---|---|
| `category` | `Category` | The updated resource, null on failure |
| `conflict` | `StatusConflict` | Non-null only when the request failed a `resourceVersion` precondition check |

### StatusConflict

| Field | Type | Notes |
|---|---|---|
| `currentResourceVersion` | `String!` | The resource's actual current version, for the caller to re-fetch and retry |
| `current` | `Category` | Current state of the resource, so the caller doesn't need a second round-trip |

A distinct GraphQL error code (`NOT_FOUND` extension) — not this payload shape — is used for the "resource no longer exists" case (FR-012), since that is not a conflict on an existing resource but the absence of one; this follows the existing gqlgen error-extension convention already used elsewhere in the schema (see `gqlerror.Errorf` usage in `graphql.go`).

## Server-Side In-Process Entities (non-persistent)

### EventBus (per-kind ring buffer)

Lives in the new `gitstore-api/internal/eventbus` package (Go, in-memory only — not a datastore entity).

| Field | Type | Notes |
|---|---|---|
| `kind` | `string` | Partition key — one ring buffer per resource kind |
| `events` | ring buffer of `Event` | Bounded size (default large enough to exceed `CheckpointFlushIntervalEvents`, configurable) |
| `subscribers` | set of `chan Event` | One channel per open WebSocket subscription; closed on client disconnect |

### Event (internal, not exposed directly — mapped to `WatchEvent` at the resolver boundary)

| Field | Type | Notes |
|---|---|---|
| `Type` | `EventType` | Added / Modified / Deleted |
| `Kind`, `Namespace`, `Name` | `string` | Resource identity |
| `ResourceVersion` | `string` | The value written by `nextResourceVersion` at admission time |
| `Object` | `any` | The admitted resource (e.g. `*datastore.CategoryTaxonomy`), converted to the GraphQL type / JSON at the resolver boundary, not inside the bus |

## Relationships

- `Event.ResourceVersion` is the same value already written to `datastore.CategoryTaxonomy.ResourceVersion` — no separate versioning scheme (research.md R3).
- `WatchEvent` (GraphQL) is a lossy, stream-oriented projection of `Event` (internal) — the mapping is 1:1 except `Object` is boxed as `JSON` for the generic `watchResources` path and as a strongly-typed `category` field for `watchCategoryTaxonomies`.
- `UpdateCategoryTaxonomyStatusInput` → `status.StatusPatch` (controller-manager side, existing): the mutation's resolver on the `gitstore-api` side and the `StatusClient` implementation on the `gitstore-controller-manager` side are two ends of the same wire contract; field names and optionality are kept identical by design so the mapping is mechanical.

## Validation Rules (from Functional Requirements)

- `UpdateCategoryTaxonomyStatusInput.resourceVersion` MUST be provided and MUST match the resource's current `resourceVersion` at write time, or the mutation returns `UpdateCategoryTaxonomyStatusPayload.conflict` (FR-009).
- A request targeting a `name`/`namespace` with no matching `CategoryTaxonomy` row returns a `NOT_FOUND` error, not a `conflict` payload (FR-012).
- The mutation is rejected before touching the datastore if the calling principal is not authorized for the `categoryTaxonomy.status.write` action (FR-011) — see contracts/status-api.graphql and research.md R5.
- A `watchCategoryTaxonomies`/`watchResources` call whose `resourceVersion` argument is older than the oldest event retained in that kind's ring buffer yields an expired-cursor signal (a `WATCH_EXPIRED`-extension GraphQL error terminating the subscription) rather than a normal event stream (FR-004).
