# GraphQL Cost Analysis, Rate Limiting, and Request Limits
**Status**: 🟡 Proposed (architecture only; implementation requires a separate feature specification)

> Extends `020-pluggable_auth_architecture.md` (principal model) and
> `021-controller_service_account_auth.md` (controller identity). Schema-annotation style follows
> [ADR 0011](../ADRs/0011-graphql-authorization-directive.md). This document is authoritative for
> GraphQL operation cost, per-principal cost budgets, the per-IP request limiter, structural
> request limits, and how all of them are reported to clients.

> **Amended by [ADR 0012](../ADRs/0012-admin-storefront-graphql-endpoints.md)** (separate Admin and
> Storefront GraphQL endpoints). Limits are applied as **per-endpoint profiles** (§8.4). Everything
> else in this document describes the **admin profile**, served at `/admin/graphql` on the shared
> listener or `/graphql` on `api.admin.port`. The storefront profile (`/storefront/graphql` or
> `/graphql` on `api.storefront.port`) reuses the same pipeline and `ratelimit.Limiter` with the
> differences in §8.4.

## 1. Executive Decision

`gitstore-api` bounds GraphQL work in four ordered layers, cheapest first:

1. **L0: per-IP request limiter (before authentication).** This is the existing token bucket,
   re-implemented on the new `ratelimit.Limiter` interface. Every HTTP request costs 1. It
   protects authentication and parsing from floods.
2. **Structural request limits (before authentication).** Body size, parser tokens, depth,
   aliases, root fields, directives, fragment visits, validation-error count, page size and
   `nodes(ids:)` size. Violations are request errors, and no resolver runs.
3. **Static cost analysis (before execution).** The IBM
   [GraphQL Cost Directives](https://ibm.github.io/graphql-specs/cost-spec.html) `@cost` and
   `@listSize` are declared in the SDL. A server-side walker computes an upper-bound
   `requestedQueryCost`. Operations above a fixed per-operation ceiling are rejected.
4. **L1: per-principal cost bucket (after authentication).** The requested cost is taken from
   the principal's bucket before execution. After execution the difference between requested
   and actual cost is refunded (Shopify's model). If the bucket is short, the response is
   HTTP 429 `THROTTLED`.

Of the IBM spec's three analysis methods, GitStore uses **Static Query Analysis** as the only
enforcement point. It uses **Query Response Analysis**, measured incrementally while fields
resolve, only for accounting (the refund). It does **not** use **Dynamic Query Analysis** to
abort operations mid-execution. Mutations perform Git commits through admission, and a partial
abort would leave committed side effects behind a partial response. Once pagination is capped,
Static analysis already bounds the worst case.

For alpha, both limiter layers keep their state in process memory on each replica, behind a
pluggable `Limiter` interface. A shared (Valkey) provider is a planned provider with no
implementation yet, the same posture as the AuthN/AuthZ provider registry. GitStore's own
control-plane controller (`gitstore-controller-manager`) is exempt from L1. Third-party
controllers and apps, including future CRD controllers and GitStore apps, are not exempt.

## 2. Goals and Non-Goals

### Goals

- Reject unbounded or pathological operations before any datastore or git-service call.
- Give third parties a predictable, documented budget that rewards cheap, well-paginated queries.
- Report cost and remaining budget on every response, with opt-in per-field breakdown and a
  dry-run mode.
- Use one limiter abstraction, one header vocabulary and one error vocabulary for both the
  per-IP and the per-principal layers.
- Declare weights on the schema, in a format other implementations understand (IBM spec, as
  used by Hot Chocolate and Apollo Router demand control), not in hand-written Go complexity
  functions.

### Non-goals

- Exact global budgets across replicas in alpha (see §9).
- Charging per subscription event (bounded by a concurrency cap instead, §6.5).
- Cost-based limiting of Git smart-HTTP (L0 only, §8.3).
- Plan- or app-tier billing. Tiers are static config until a policy-driven source exists (§12).

## 3. Verified Current-State Constraints

| #   | Finding                                                                                                                                                                                                                                                                                                    | Location                                                                   |
|-----|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|----------------------------------------------------------------------------|
| C1  | `first`/`last` are not capped. `PageParams.Limit()` only substitutes `DefaultPageSize = 100` when the value is `0`, so `first: 1000000` reaches the datastore.                                                                                                                                             | `gitstore-api/internal/datastore/datastore.go:128-143`                     |
| C2  | No complexity, depth, alias or root-field limit. The handler registers only `Introspection` and `AutomaticPersistedQuery`.                                                                                                                                                                                 | `gitstore-api/internal/app/server.go:382-410`                              |
| C3  | No parser token limit. gqlgen's executor defaults to unlimited (`parserTokenNoLimit`), and `SetParserTokenLimit` is never called. This is the mitigation for CVE-2023-49559 / [GHSA-2hmf-46v7-v6fx](https://github.com/advisories/GHSA-2hmf-46v7-v6fx), directive overloading.                             | gqlgen `graphql/executor/executor.go:44,188`                               |
| C4  | No request body limit. gqlgen's POST transport does `io.ReadAll(r.Body)`.                                                                                                                                                                                                                                  | gqlgen `graphql/handler/transport/http_post.go:88`                         |
| C5  | Introspection is enabled for every caller, including anonymous.                                                                                                                                                                                                                                            | `server.go:399`                                                            |
| C6  | L0 is a per-IP `x/time/rate` bucket in a per-replica map. It returns `{"error":"rate limit exceeded"}`, which is not GraphQL-shaped, and sends no rate-limit headers.                                                                                                                                      | `gitstore-api/internal/middleware/security/secure.go:100-196,489-504`      |
| C7  | `gin.New()` is used without `SetTrustedProxies`. gin's default trusts every proxy, so `c.ClientIP()` honours a client-supplied `X-Forwarded-For` and L0 can be bypassed by rotating that header.                                                                                                           | `server.go:428`, `secure.go:490`                                           |
| C8  | The Git smart-HTTP server (`api.git_port`) has no limiter at all.                                                                                                                                                                                                                                          | `server.go:291`                                                            |
| C9  | Authentication runs in `AroundOperations`, after gqlgen's `CreateOperationContext` (parse, validate, `MutateOperationContext`). A cost computed in `MutateOperationContext` is therefore principal-agnostic. The bucket charge must happen in an operation interceptor registered after the authenticator. | `server.go:406-409`, gqlgen `executor.go`                                  |
| C10 | gqlgen chooses the HTTP status itself: 200, or 422 (`application/json`) / 400 (`application/graphql-response+json`) for `KindProtocol` errors. There is no hook that produces 429.                                                                                                                         | gqlgen `transport/http_get.go:112-129`                                     |
| C11 | Controllers page with `first: 100` today.                                                                                                                                                                                                                                                                  | `gitstore-controller-manager/internal/listwatch/*.go`                      |
| C12 | Connections expose `edges` and `totalCount: Int!` (no `nodes` shortcut).                                                                                                                                                                                                                                   | `shared/schemas/*.graphqls`                                                |
| C13 | Controller principals are service accounts issued by the API itself, with `Subject = "serviceaccount:controllers:gitstore-controller-manager"`.                                                                                                                                                            | `config/policy.yaml:35`, `auth/provider/serviceaccountjwt/provider.go:159` |

C1, C3, C4 and C7 are exploitable today, whatever happens to cost analysis. They make up Phase 1 (§13).

## 4. Request Pipeline

```text
HTTP request
 │
 ├─ gin: RequestId → CORS → trusted-proxy ClientIP (C7 fix)
 ├─ L0  Limiter.Take(key="ip:<addr>", policy=ip, cost=1)            → 429 RATE_LIMITED
 ├─ http.MaxBytesReader(max_body_bytes)                              → 413 REQUEST_BODY_TOO_LARGE
 │
 ├─ gqlgen CreateOperationContext
 │    ├─ parse (SetParserTokenLimit)                                 → 422 GRAPHQL_PARSE_FAILED
 │    ├─ validate (default rules + GitStore limit rules, §7)         → 422 MAX_*_EXCEEDED
 │    └─ MutateOperationContext: static cost walker (§6)             → 422 MAX_COST_EXCEEDED
 │         stores requestedQueryCost (+ field breakdown if requested) in request cost state
 │
 ├─ gqlgen DispatchOperation → AroundOperations chain
 │    ├─ GraphQLAuthenticator (existing)
 │    ├─ GraphQLAuthorizer    (existing)
 │    └─ CostAdmission (new, registered last)
 │         ├─ `GitStore-GraphQL-Cost: validate` → return cost only, no execution
 │         ├─ exempt principal? → skip L1
 │         └─ L1 Limiter.Take(key=principal, policy=tier, cost=requested) → 429 THROTTLED
 │
 ├─ execute; AroundFields CostMeter counts resolved composite values → actualQueryCost
 └─ ResponseInterceptor: Limiter.Refund(requested − actual); attach extensions.cost;
    throttleWriter sets RateLimit/RateLimit-Policy/Retry-After headers and rewrites 429
```

Structural limits and static cost are computed **before** authentication, on purpose. An
unauthenticated attacker can't make GitStore spend JWT verification or policy evaluation on
an operation that is going to be rejected anyway.

## 5. Unified Limiter Contract

Package `gitstore-api/internal/ratelimit`:

```go
// Limiter is a weighted token bucket keyed by an opaque string. Implementations
// must make Take and Refund atomic per key. Providers: "memdb" (alpha),
// "valkey" (planned, not implemented).
type Limiter interface {
    Take(ctx context.Context, key string, policy Policy, cost int64) (Decision, error)
    Refund(ctx context.Context, key string, policy Policy, amount int64) (Status, error)
}

type Policy struct {
    Name             string  // "ip", "anonymous", "default", …; emitted in RateLimit headers
    MaximumAvailable int64   // bucket capacity
    RestoreRate      float64 // units per second
}

type Decision struct {
    Allowed    bool
    Status     Status
    RetryAfter time.Duration // zero when Allowed
}

type Status struct {
    MaximumAvailable   int64
    CurrentlyAvailable int64
    RestoreRate        float64
}
```

**Why not `x/time/rate`.** `Reservation.CancelAt` restores only whole reservations, only
before `timeToAct`, and minus tokens that later reservations have already consumed
(`golang.org/x/time@v0.15.0/rate/rate.go:166-200`). It can't express "refund 55 of the 101
reserved units after execution". The `memdb` provider is a small bucket per key
(`tokens float64`, `last time.Time`, under a mutex) with lazy refill:
`tokens = min(max, tokens + elapsed·rate)`. A request is allowed when `tokens ≥ cost`.
`RetryAfter = ceil((cost − tokens)/rate)`.

**Bounded memory.** The provider keeps at most `max_tracked_keys` buckets, with LRU eviction
plus the existing 10-minute idle sweep. Evicting a key resets it to full. That is fail-open for
fairness, and acceptable because anonymous keys are IP-scoped and already bounded by L0.

**Provider errors** (the future Valkey provider being unreachable) follow `on_error = "allow" | "deny"`,
default `allow`. Each such decision increments `gitstore_api_ratelimit_provider_errors_total`
and logs at warn with rate-limited sampling. The limiter protects availability, not
confidentiality, so the default avoids turning a limiter outage into an API outage.

**Future Valkey provider.** It needs an atomic weighted take and an atomic refund, which means
a Lua GCRA or token-bucket script. Candidates to evaluate: `go-redis/redis_rate` (GCRA) and
redis-cell. Neither is adopted by this document.

**Both layers use the same interface.** L0 becomes `Take(key="ip:"+ClientIP, policy=ip, cost=1)`.
The existing `api.rate_limit_per_second`/`rate_limit_burst` keys map to
`Policy{RestoreRate, MaximumAvailable}`, so current deployments keep their behaviour.

## 6. Cost Model

### 6.1 Directives

The IBM definitions are adopted verbatim. They are server-only, and gqlgen skips them at
runtime:

```graphql
directive @cost(weight: String!) on
  ARGUMENT_DEFINITION | ENUM | FIELD_DEFINITION | INPUT_FIELD_DEFINITION | OBJECT | SCALAR

directive @listSize(
  assumedSize: Int
  slicingArguments: [String!]
  sizedFields: [String!]
  requireOneSlicingArgument: Boolean = true
) on FIELD_DEFINITION
```

```yaml
# gitstore-api/gqlgen.yml
directives:
  cost:     { skip_runtime: true }
  listSize: { skip_runtime: true }
```

At startup the walker reads `ExecutableSchema.Schema()` once. It builds an immutable table of
weights and list sizes keyed by schema coordinate. Directive arguments are never parsed per
request.

### 6.2 Default weights

| Element                                            | Weight                | Source                                                                                           |
|----------------------------------------------------|-----------------------|--------------------------------------------------------------------------------------------------|
| Scalar, enum, and their fields/arguments           | 0                     | IBM §7.1; Shopify                                                                                |
| Composite (object) output value                    | 1                     | IBM §7.1; Shopify                                                                                |
| `Mutation` root field without explicit `@cost`     | 10                    | GitStore default (IBM defines no operation defaults; Shopify uses 10). Each admits a Git commit. |
| `Subscription` root field without explicit `@cost` | 10                    | GitStore default. Covers journal replay and registry setup.                                      |
| Abstract type (`Node`, unions)                     | max over member types | IBM: `@cost` is forbidden on interfaces and unions; use the member maximum                       |
| `totalCount`                                       | `@cost(weight: "10")` | Count queries are not O(page). Calibrate in Phase 2.                                             |
| Fields whose resolver calls git-service            | `@cost(weight: "5")`  | Calibrate from capacity runs                                                                     |

### 6.3 Lists and pagination

Every connection field declares its size:

```graphql
products(first: Int, after: String, last: Int, before: String, …): ProductConnection!
  @listSize(slicingArguments: ["first", "last"], sizedFields: ["edges"],
            assumedSize: 100, requireOneSlicingArgument: false)
```

- **`first`/`last` are hard-capped.** `max_page_size`, default **250**, rejects rather than
  silently clamping. A value `< 0` or `> max_page_size` is a request error, `MAX_PAGE_SIZE_EXCEEDED`.
  The cap is enforced twice: in the cost walker (before execution) and in `PageParams`
  normalisation (defence in depth for gRPC and internal callers). `DefaultPageSize` stays 100,
  so controllers (C11) are unaffected.
- **Multiple slicing arguments.** When `first` and `last` are both given, the walker uses the
  larger value. IBM: "static analysis should consider their largest value to ensure producing
  upper bounds." `requireOneSlicingArgument: false` because both arguments are optional in
  GitStore's Relay contract. When neither is given, `assumedSize` (= `DefaultPageSize`) applies.
- **`nodes(ids:)`** is limited to `max_node_ids`, default **50** (Hot Chocolate's
  `MaxAllowedNodeBatchSize` default). It carries `@listSize(assumedSize: 50)`. The actual cost
  is refunded to the real number of ids.
- **Schema lint (build-time test).** Every list-typed output field outside introspection must
  have `@listSize` or an explicit `@cost`. Every connection field must name its slicing
  arguments. This mirrors the ADR 0011 coverage test, so no new list can ship unbounded.

Cost of a field = `weight(field) + multiplier × cost(children)`, where the multiplier is the
list size for sized fields and 1 otherwise. This is Shopify's `requestedChildrenCost`
composition, and it is what the per-field breakdown reports (§10.2).

### 6.4 Fragments, abstract types, and introspection

- Fragment spreads are expanded while walking. Each expansion counts towards
  `max_fragment_visits` (§7), so the walk is bounded even for reused fragments. gqlparser's
  default `NoFragmentCycles` rule already rejects cycles.
- For a selection on an abstract type, the cost is the maximum over its possible concrete types
  of the selections that apply to that type. This keeps the result an upper bound.
- `__schema`/`__type` selections are charged a flat `introspection_cost`, default 100, instead
  of being walked. `introspection = "authenticated"` (the default) rejects introspection from
  anonymous principals in `CostAdmission`. gqlparser's default `MaxIntrospectionDepth` rule stays
  enabled.

### 6.5 Subscriptions

- `watch*` subscriptions are charged their static cost once, at subscribe time, through the
  same `CostAdmission` interceptor. gqlgen dispatches WebSocket operations through the same
  executor, and principals are already attached by `webSocketInitFunc`.
- Events are not charged. Load is bounded by `max_subscriptions_per_principal` (default 20),
  tracked in the existing `wsregistry`, and by the existing watch-journal bounds.
- Nothing is refunded for subscriptions, because the actual cost isn't known until the stream ends.

### 6.6 Actual cost (Query Response Analysis)

An `AroundFields` `CostMeter` adds, for each resolved field of composite type, the field's
weight times the number of non-null values it actually produced (list length, or 1). The count
goes into an atomic counter in the request's cost state. It never aborts. The result is the same
number a post-hoc response walk would produce, without re-parsing the serialized JSON. In the
`ResponseInterceptor`, `actualQueryCost = min(actual, requested)` and
`Refund(requested − actual)` is issued.

## 7. Structural Request Limits

Applied through `SetParserTokenLimit`, `SetValidationRulesFn` (default gqlparser rules plus the
GitStore rules below), `http.MaxBytesReader`, and a context deadline. gqlparser exposes no global
rule registry, so `SetValidationRulesFn` (gqlgen ≥ 0.17.79) is the injection point.

| Limit                                      | Default | Enforced by                                           | Code                           | Prior art                             |
|--------------------------------------------|---------|-------------------------------------------------------|--------------------------------|---------------------------------------|
| `max_body_bytes` (`application/json` POST) | 1 MiB   | gin `MaxBytesReader`                                  | `REQUEST_BODY_TOO_LARGE` (413) | —                                     |
| `parser_token_limit`                       | 15 000  | `SetParserTokenLimit`                                 | `GRAPHQL_PARSE_FAILED`         | GHSA-2hmf-46v7-v6fx                   |
| `max_depth`                                | 15      | validation rule                                       | `MAX_DEPTH_EXCEEDED`           | HC `MaxExecutionDepth` (10)           |
| `max_aliases`                              | 30      | validation rule                                       | `MAX_ALIASES_EXCEEDED`         | alias overloading                     |
| `max_root_fields`                          | 20      | validation rule                                       | `MAX_ROOT_FIELDS_EXCEEDED`     | —                                     |
| `max_directives_per_location`              | 4       | validation rule                                       | `MAX_DIRECTIVES_EXCEEDED`      | HC `MaxAllowedDirectives` (4)         |
| `max_fragment_visits`                      | 1 000   | validation rule (shared counter with the cost walker) | `MAX_FRAGMENT_VISITS_EXCEEDED` | HC `MaxAllowedFragmentVisits` (1 000) |
| `max_validation_errors`                    | 5       | error truncation in the rules fn                      | —                              | HC `MaxAllowedValidationErrors` (5)   |
| `max_page_size`                            | 250     | cost walker + `PageParams`                            | `MAX_PAGE_SIZE_EXCEEDED`       | —                                     |
| `max_node_ids`                             | 50      | cost walker                                           | `MAX_NODE_IDS_EXCEEDED`        | HC `MaxAllowedNodeBatchSize` (50)     |
| `max_operation_cost`                       | 1 000   | cost walker                                           | `MAX_COST_EXCEEDED`            | Shopify single-query cap (1 000)      |
| `execution_timeout` (queries only)         | 30 s    | context deadline in `CostAdmission`                   | `EXECUTION_TIMEOUT`            | HC `ExecutionTimeout` (30 s)          |

- **Execution timeout.** It deliberately does not apply to mutations. Cancelling a mutation's
  context mid-commit has the same "maybe committed" ambiguity as dynamic abort (§1). Mutations
  keep their existing git-service gRPC deadlines.
- **Error codes.** gqlparser rule failures carry `gqlerror.Error.Rule`. The error presenter maps
  each rule name to the stable `extensions.code` values above. gqlgen already classifies
  validation failures as `KindProtocol`, which gives 422 (`application/json`) or 400
  (`application/graphql-response+json`). `MAX_COST_EXCEEDED` and `MAX_PAGE_SIZE_EXCEEDED` are
  registered with `errcode.RegisterErrorType(code, errcode.KindProtocol)` so they get the same
  status mapping.
- **Parser token limit.** Phase 1 measures the largest operation shipped in-repo (admin,
  controller-manager, capacity profiles) and confirms the default leaves at least 4× headroom.
- **Batching.** gqlgen's POST transport decodes a single `RawParams`. Array batching is not
  supported and must stay unsupported.

## 8. Principals, Keys, Tiers, and Exemption

### 8.1 Bucket key

| Principal                          | L1 key                      | Tier             |
|------------------------------------|-----------------------------|------------------|
| Anonymous (`AuthMethod == "none"`) | `anon:<trusted ClientIP>`   | `anonymous`      |
| Service account issued by this API | `sa:<ServiceAccountUID>`    | `serviceaccount` |
| Any other authenticated principal  | `sub:<Issuer>\x00<Subject>` | `default`        |

Keying service accounts by UID means a rename or re-issue doesn't reset the budget. Keying
other principals by `Issuer + Subject` stops two OIDC issuers from sharing a bucket through
colliding `sub` values.

### 8.2 Exemption

```toml
[api.graphql.cost]
exempt_subjects = ["serviceaccount:controllers:gitstore-controller-manager"]
```

A principal is exempt only if **both** of these hold:

- `Subject` matches an entry exactly.
- `Issuer` is the API's own service-account issuer.

An external OIDC token whose `sub` happens to equal the controller's subject is not exempt.
Exemption is not role-based: granting a third-party service account the `controller` role must
not grant it an unlimited budget.

Exempt principals skip L1 and `max_subscriptions_per_principal`. They are still subject to L0,
the structural limits and `max_operation_cost`. That ceiling doubles as a regression check that
GitStore's own controller queries stay reasonable. Their `extensions.cost` omits `throttleStatus`.

### 8.3 L0 scope

L0 runs before authentication, so it can't exempt by subject. Its defaults (50/s, burst 100)
already carry controller traffic today. L0 is also mounted on the Git smart-HTTP server (C8)
with its own `git` policy, because clone/fetch/push are heavy per request. Charging cost for Git
traffic is out of scope.

### 8.4 Endpoint profiles (ADR 0012 §6)

Each GraphQL handler is built by `newGraphQLHandler(schema, profile)` with its own profile. The pipeline
in §4 is identical; the profile decides which layers run and with which values.

| Layer | Admin profile | Storefront profile |
|---|---|---|
| L0 per-IP | `ip` policy (50/s, burst 100) | `storefront_ip` policy, higher default. Keyed on `GitStore-Storefront-Buyer-IP` when the request carries a valid storefront access token, otherwise on the trusted-proxy client IP. |
| Structural limits (§7) | yes | yes, same defaults |
| Static cost and `max_operation_cost` | 1 000 | 1 000 |
| L1 per-principal bucket | yes; §8.2 exemption | **none**. Buyer reads aren't budgeted (Shopify Storefront model). `extensions.cost` still reports `requestedQueryCost`/`actualQueryCost`, without `throttleStatus`. |
| Write throttle | — | cart/checkout creation per buyer and per storefront token, on the same `Limiter` with a `checkout` policy; `THROTTLED` via `throttleWriter` (HTTP 429) |
| Introspection | `authenticated` | `all`; the storefront schema is a public contract |
| `GitStore-GraphQL-Cost` | `report`/`validate` | `report`/`validate` |

- The buyer-IP header is ignored on any request without a storefront access token, so it can't be
  used to spoof L0 keys (same class as C7).
- The storefront handler exists only when `api.storefront.enabled = true` (ADR 0012 §1).
- Bot and crawler protection stays with the operator's CDN or WAF in alpha.

## 9. Multi-Replica, Restart, and Rolling-Upgrade Semantics

- **Per-replica budgets (alpha, `memdb` provider).** With N replicas behind a load balancer,
  a principal's effective budget is up to N× the configured one, and `currentlyAvailable`
  describes only the replica that served the request. This is documented behaviour. Operators
  size `maximum_available`/`restore_rate` per replica. The `valkey` provider will remove the
  multiplier.
- **Restart and rolling replacement.** Buckets are not persisted. A new replica starts every
  bucket full. This is fail-open for fairness only, and every structural limit and the
  per-operation ceiling still apply.
- **Mixed-version rollout.** The directives are additive SDL with `skip_runtime`. Old replicas
  ignore them and new ones enforce them.
- **Mixed-version behaviour change.** During a rollout, `first > max_page_size` succeeds on old
  replicas and fails on new ones. This is a release-noted behaviour change (breaking for any
  caller using > 250). In-repo callers use ≤ 100.
- **Enforcement off switch.** `enforce = false` computes and reports cost but never throttles
  (Hot Chocolate's `EnforceCostLimits` analogue). Operators can observe real costs before
  turning on L1.

## 10. Client Reporting

### 10.1 Always (non-exempt principals)

```json
{
  "data": { … },
  "extensions": {
    "cost": {
      "requestedQueryCost": 101,
      "actualQueryCost": 46,
      "throttleStatus": {
        "maximumAvailable": 1000,
        "currentlyAvailable": 954,
        "restoreRate": 50
      }
    }
  }
}
```

The field names are Shopify's, adopted as-is because they are the most widely known.
`throttleStatus` is replica-local in alpha (§9). Hot Chocolate's `fieldCost`/`typeCost` split is
not adopted: GitStore weights one dimension. If serialization cost ever needs its own weight,
`typeCost` can be added under `extensions.cost` without breaking existing clients.

### 10.2 Debug and dry run: `GitStore-GraphQL-Cost` request header

| Value      | Behaviour                                                                                                                                                                                                                          | Precedent                                                  |
|------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------|
| `report`   | Executes normally and adds `extensions.cost.fields[]`, the per-field breakdown                                                                                                                                                     | `Shopify-GraphQL-Cost-Debug: 1`; HC `GraphQL-Cost: report` |
| `validate` | Runs parse, validation and the static walker only, **no execution** and no L1 charge (L0 still counts). Returns `{"extensions":{"cost":{"requestedQueryCost":…,"fields":[…]}}}`. Useful for pricing a mutation without committing. | HC `GraphQL-Cost: validate`                                |

```json
"fields": [
  { "path": ["products"], "definedCost": 1, "requestedTotalCost": 101, "requestedChildrenCost": 100 },
  { "path": ["products", "edges"], "definedCost": 1, "requestedTotalCost": 100, "requestedChildrenCost": 0 }
]
```

**Header name.** It is vendor-scoped (`GitStore-`) and has no `X-` prefix, following
[RFC 6648](https://www.rfc-editor.org/rfc/rfc6648) (BCP 178), which deprecates `X-` for new
parameters, and matching both precedents (`Shopify-GraphQL-Cost-Debug`, `GraphQL-Cost`). It
combines Shopify's vendor prefix with Hot Chocolate's two modes. The header is allowed through
CORS `AllowHeaders`. `fields[]` is bounded by the fragment-visit limit.

### 10.3 Rate-limit headers (both layers)

The fields follow [draft-ietf-httpapi-ratelimit-headers-11](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/)
(May 2026). A token bucket is expressed as `q = maximumAvailable` and
`w = ceil(maximumAvailable / restoreRate)`, the time to refill from empty.

```http
RateLimit-Policy: "ip";q=100;w=2, "default";q=1000;w=20;qu="cost"
RateLimit: "default";r=954;t=1
```

- Only policies that applied to the request are listed.
- `Retry-After` (seconds) is sent on every 429. Per the draft it takes precedence over
  `RateLimit` and never points earlier than the effective window.
- WebSocket frames can't carry headers. Subscription clients read `extensions.cost` on the first
  `next` payload, or the error on rejection.

## 11. Errors

### 11.1 `THROTTLED` (L1) and `RATE_LIMITED` (L0): HTTP 429

GraphQL-over-HTTP forbids `application/graphql-response+json` for responses where "errors …
completely prevent the generation of a well-formed GraphQL-over-HTTP response". A 429 is
therefore sent as `Content-Type: application/json`, with a GraphQL-shaped body so clients can
use one parser:

```json
{
  "errors": [{
    "message": "Throttled: requested cost 101 exceeds currently available 40.",
    "extensions": {
      "code": "THROTTLED",
      "retryAfterSeconds": 2,
      "cost": {
        "requestedQueryCost": 101,
        "throttleStatus": { "maximumAvailable": 1000, "currentlyAvailable": 40, "restoreRate": 50 }
      }
    }
  }]
}
```

L0 uses the same shape with `code: "RATE_LIMITED"` and no `cost` block. This replaces today's
`{"error":"rate limit exceeded"}` (C6).

**How the 429 is produced (C10).** The gin handler puts a request-scoped `*costState` in the
context and wraps `gin.ResponseWriter` in a `throttleWriter`. When `CostAdmission` rejects an
operation, it marks the state and returns the error response. On `WriteHeader` or the first
`Write`, the writer substitutes 429, forces `Content-Type: application/json`, and sets
`Retry-After` and the `RateLimit*` fields. No gqlgen fork is needed.

On WebSocket the rejection is a `graphql-transport-ws` `error` message carrying the same
`extensions`.

### 11.2 `MAX_COST_EXCEEDED`: request error, never retryable

```json
{ "errors": [{ "message": "Operation cost 1450 exceeds the maximum of 1000.",
  "extensions": { "code": "MAX_COST_EXCEEDED", "maxCost": 1000, "requestedQueryCost": 1450 } }] }
```

Stable UPPER_SNAKE codes follow the existing API vocabulary (`FORBIDDEN`, `NOT_FOUND`,
`WATCH_EXPIRED`), not opaque identifiers like Hot Chocolate's `HC0047`.

## 12. Configuration

```toml
[api]
rate_limit_per_second = 50        # L0 "ip" policy (unchanged keys)
rate_limit_burst = 100
trusted_proxies = []              # CIDRs; empty = trust none (C7 fix)

[api.graphql.limits]
max_body_bytes = 1048576
parser_token_limit = 15000
max_depth = 15
max_aliases = 30
max_root_fields = 20
max_directives_per_location = 4
max_fragment_visits = 1000
max_validation_errors = 5
max_page_size = 250
max_node_ids = 50
max_operation_cost = 1000
execution_timeout = "30s"
introspection = "authenticated"   # all | authenticated | none
introspection_cost = 100

[api.graphql.cost]
provider = "memdb"               # memdb | valkey (planned)
enforce = true
on_error = "allow"                # allow | deny
max_tracked_keys = 100000
max_subscriptions_per_principal = 20
exempt_subjects = ["serviceaccount:controllers:gitstore-controller-manager"]

[api.graphql.cost.tiers.anonymous]
maximum_available = 200
restore_rate = 10

[api.graphql.cost.tiers.default]
maximum_available = 1000
restore_rate = 50

[api.graphql.cost.tiers.serviceaccount]
maximum_available = 2000
restore_rate = 100

[api.storefront.graphql.limits]   # storefront profile (§8.4); unset keys inherit [api.graphql.limits]
rate_limit_per_second = 200       # L0 "storefront_ip" policy
rate_limit_burst = 400
max_operation_cost = 1000
introspection = "all"

[api.storefront.graphql.checkout] # write throttle, per buyer and per storefront token
rate_limit_per_second = 1
rate_limit_burst = 5

[api.git.ratelimit]               # L0 "git" policy on the smart-HTTP server
rate_limit_per_second = 20
rate_limit_burst = 40
```

- All values are validated at startup (`validate:` tags), and `make check TARGET=config` covers
  them.
- Tier selection is static in alpha. A later policy-driven source (per app, per namespace)
  plugs in behind a `TierResolver` interface without changing `Limiter`.

## 13. Implementation Phases

1. **Hardening (no new public contract).**
   - Cap `first`/`last` in the walker precursor and `PageParams`.
   - Add `max_node_ids`, the body limit, `SetParserTokenLimit`, `SetTrustedProxies`, the
     structural validation rules with the error-code mapping, and the query execution timeout.
   - Rebuild L0 on `ratelimit.Limiter` with the GraphQL-shaped 429 and `RateLimit` headers.
   - Mount L0 on the Git smart-HTTP server.
2. **Cost reporting.**
   - Add the directives and weights in `shared/schemas`, plus the schema lint test.
   - Build the walker, `MAX_COST_EXCEEDED`, `CostMeter`, `extensions.cost` and
     `GitStore-GraphQL-Cost: report|validate`.
   - Ship L1 with `enforce = false` and calibrate weights from `make capacity` runs.
3. **Cost enforcement.**
   - L1 `Take`/`Refund`, tiers, exemption, subscription concurrency cap, `THROTTLED` via
     `throttleWriter`.
   - Anonymous introspection gate.
   - Docs: `docs/api-reference.md` (limits, headers, errors) and `docs/configuration.md`.
4. **Future (not scheduled).**
   - `valkey` provider (Lua GCRA/token bucket with atomic refund).
   - Policy-driven tiers for third-party apps and CRD controllers.
   - Per-namespace budgets.
   - Storefront profile (§8.4), shipped with the Storefront API behind `api.storefront.enabled`.

## 14. Observability

| Metric                                                | Type      | Labels                                                                        |
|-------------------------------------------------------|-----------|-------------------------------------------------------------------------------|
| `gitstore_api_graphql_operation_cost`                 | histogram | `operation_type`, `measure` = `requested\|actual`                             |
| `gitstore_api_graphql_request_limit_rejections_total` | counter   | `limit` (the codes in §7)                                                     |
| `gitstore_api_ratelimit_decisions_total`              | counter   | `layer` = `ip\|git\|cost`, `policy`, `outcome` = `allowed\|throttled\|exempt` |
| `gitstore_api_ratelimit_provider_errors_total`        | counter   | `provider`, `on_error`                                                        |
| `gitstore_api_ratelimit_tracked_keys`                 | gauge     | `layer`                                                                       |
| `gitstore_api_graphql_subscriptions_rejected_total`   | counter   | `reason`                                                                      |

- No principal or IP labels, to bound cardinality. Throttle decisions are logged at debug with
  the subject and request ID.

## 15. Validation and Acceptance Matrix

| Area                  | Must be proven by                                                                                                                                                                                                                              |
|-----------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| Pagination cap        | Contract tests on both backends: `first: 251`, `last: -1`, both `first`+`last` (largest wins) are rejected before any datastore call. `first` absent → 100. Controller list/watch unchanged.                                                   |
| Structural limits     | One test per §7 row, including directive overloading (GHSA-2hmf-46v7-v6fx PoC), alias fan-out, fragment re-use explosion, oversized body (413), spoofed `X-Forwarded-For` with no trusted proxies.                                             |
| Static cost           | Golden costs for representative queries: connection nesting, `nodes`, abstract `Node` selections (member max), fragments, mutations (+10), subscriptions. The schema lint fails on an unsized list.                                            |
| Refund                | Requested 101 / actual 46 leaves the bucket at `max − 46` (±restore). `validate` never charges L1.                                                                                                                                             |
| 429 contract          | HTTP 429, `application/json`, GraphQL-shaped body, `Retry-After`, `RateLimit` fields. WebSocket rejection message. Both `application/json` and `application/graphql-response+json` `Accept` values.                                            |
| Pluggable AuthN/AuthZ | Key derivation and tier selection for static-users, oidc-jwt, service-account JWT/assertion, anonymous. Exemption denied for an external-issuer token carrying the controller's `sub`, and for a third-party SA holding the `controller` role. |
| Multi-replica         | Two API replicas: per-replica budgets are independent and documented. Exempt controller keeps reconciling under a throttled third-party flood.                                                                                                 |
| Rolling upgrade       | Mixed old/new replicas: additive SDL, and only the documented `first > 250` difference.                                                                                                                                                        |
| Bounded work          | Walker time/allocs bounded by `max_fragment_visits`. `max_tracked_keys` eviction under key churn. No goroutine per key.                                                                                                                        |
| Sustained load        | `make capacity TARGET=api PROFILE=readiness` with L1 enforced: throttling rate and p95/p99 latency overhead of walker + meter recorded in the evidence bundle.                                                                                 |

## 16. References

- IBM, [GraphQL Cost Directives Specification](https://ibm.github.io/graphql-specs/cost-spec.html) (first draft, 2021): `@cost`/`@listSize`, default weights, largest-slicing-argument rule, abstract-type maximum, Static/Dynamic/Response analysis.
- ChilliCream, [Hot Chocolate cost analysis](https://chillicream.com/docs/hotchocolate/security/cost-analysis) and [request limits](https://chillicream.com/docs/hotchocolate/security/request-limits).
- Apollo, [GraphOS Router demand control](https://www.apollographql.com/docs/graphos/routing/security/demand-control): an IBM-directive implementation with estimated vs actual cost.
- Shopify, [GraphQL Admin API rate limits](https://shopify.dev/docs/apps/build/apis/graphql-admin/rate-limits): cost table, refund, `throttleStatus`, `Shopify-GraphQL-Cost-Debug`, 1 000-point single-query cap, 250-item input arrays.
- gqlgen, [complexity reference](https://gqlgen.com/reference/complexity/) and [`handler` package](https://pkg.go.dev/github.com/99designs/gqlgen/graphql/handler) (`SetParserTokenLimit`, `SetValidationRulesFn`).
- GitHub Advisory [GHSA-2hmf-46v7-v6fx](https://github.com/advisories/GHSA-2hmf-46v7-v6fx) / CVE-2023-49559.
- IETF, [draft-ietf-httpapi-ratelimit-headers-11](https://datatracker.ietf.org/doc/draft-ietf-httpapi-ratelimit-headers/).
- GraphQL Foundation, [GraphQL over HTTP (draft)](https://graphql.github.io/graphql-over-http/draft/).
- IETF, [RFC 6648](https://www.rfc-editor.org/rfc/rfc6648): deprecating the `X-` prefix.
- [`golang.org/x/time/rate`](https://pkg.go.dev/golang.org/x/time/rate): `Reservation.CancelAt` semantics.
