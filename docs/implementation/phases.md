# GitStore Implementation Roadmap

## Executive summary

GitStore already has a meaningful foundation in place. The repository documents a three-service topology around `gitstore-api`, `gitstore-git-service`, and `gitstore-controller-manager`; a Git Smart HTTP front door; GraphQL reads for `Product`, `ProductVariant`, `Category`, and `Collection`; namespace and repository mutations; memdb and ScyllaDB datastore backends; bootstrap scripts; and a canonical PR workflow based on the root `Makefile`, `make pr-ready`, and Conventional Commits. The two strongest signals that this is more than a paper design are the current developer/user guides and API reference, plus several closed issues for namespace lifecycle and catalog resource work. [1]

The backlog, however, is not yet arranged in an implementation order that matches the requested four release phases. The open issue inventory is broad and ambitious: the repository currently shows 156 issues, 27 labels, 2 open milestones, and 0 pull requests, while many important dependencies are only captured in issue bodies rather than in GitHub relationship fields. The result is a roadmap with useful ideas but weak execution sequencing. [2]

My main conclusion is that **Phase 1 alpha should be a focused close-out of the architecture proof that is already partly documented**, not a large feature phase. The highest-value work is to finish the validating admission contract, repository authorization, the remaining core Git-backed catalog contracts, and the lifecycle/versioning guarantees that make watch/reconcile safe. **Phase 2 beta should then complete the read/write control plane and reconciliation substrate**: watch/status semantics, Scylla-backed persistence, authn/authz foundations, and pricing/inventory configuration resources that are missing from the current backlog despite being present in resource docs. **Phase 3 MVP should pivot hard into runtime commerce**: cart, checkout, orders, payment intents, fulfillment basics, storefront compatibility, migration/import paths, and operator workflows. **Phase 4 stable should be about hardening and extensibility**, not about inventing core commerce primitives late. [3]

A second conclusion is architectural: GitStore’s own resource docs separate **Git-backed desired-state resources** from **datastore-only runtime resources**. That means the backlog items that research Git-managed `Order` and `PaymentIntent` frontmatter are now misaligned with the documented architecture. The docs explicitly say orders are datastore-only by default, and they model `PaymentIntent` under datastore-only resources as well. Those issues should be reframed or closed in favor of runtime APIs and persistence work. [4]

## Current state assessment

The clearest “implemented or at least repository-documented” surfaces today are the control plane and read APIs. The API reference documents `createNamespace`, `deleteNamespace`, `createRepository`, `renameRepository`, `transferRepository`, and `deleteRepository`, and the user/developer guides document `make bootstrap` as the normal way to create a namespace and repository. Namespace lifecycle issue #119 is closed, and the earlier namespace umbrella #39 is closed as well. That combination is strong enough to treat namespace bootstrap and basic repository control-plane lifecycle as **implemented/documented foundation**, even if some cleanup remains. [5]

Catalog reads are also beyond the concept stage. The API reference documents GraphQL queries and types for `Product`, `ProductVariant`, `Category`, and `Collection`, and the user guide shows authoring and querying those resources after a Git push. The docs are also consistent on a key commerce rule: `Product` is the non-sellable aggregate, while `ProductVariant` is the sellable SKU. That matches issue #143. I would therefore mark **basic GraphQL reads and the variant-first sellable model as documented and partially implemented**, though the surrounding operational semantics are not complete yet. [6]

The frontmatter migration is mixed. The umbrella issue #40 is still open and only shows 3 of 10 issues completed. `ProductVariant` (#83) is closed, `Collection` (#84) is closed, and both are represented in the API/user docs. `Product` (#77) is still open. `CategoryTaxonomy` (#82) is only partially complete at 4 of 6 issues, with open follow-on tasks for deletion semantics and controller reconciliation. `File` (#79) and `Translation` (#78) are both open with 0 of 4 issues completed. So the right status summary is: **frontmatter foundation is partially implemented, but the core set is not actually complete yet**. [7]

The runtime architecture is conceptually strong but operationally incomplete. The developer guide describes the full push path: Git push into `gitstore-api`, gRPC forwarding to `gitstore-git-service`, blocking pre-receive validation via `CatalogService.ValidateResources`, post-receive admission via `CatalogService.AdmitResources`, hydration into the datastore, and follow-up controller reconciliation. But the corresponding open backlog for validating admission (#123), Git transport authentication and authorization (#126), controller watch/status semantics (#131, #176–#183, #188), object lifecycle/versioning (#137), hydrated storage (#136), and generic Scylla resource storage (#140–#141) is still open. That is the actual gap between the current repository and a beta-grade control plane. [8]

The biggest backlog mismatch is in runtime commerce. GitStore’s docs clearly classify `Cart`, `Checkout`, `Order`, `PaymentIntent`, inventory ledger entries, reservations, allocations, fulfillment orders, shipments, and tracking events as **datastore-only runtime resources**, while Git-backed resources cover desired-state catalog/configuration such as `Product`, `ProductVariant`, `CategoryTaxonomy`, `Collection`, `Translation`, `File`, `PriceList`, `PriceSet`, and `PriceRule`. Yet the backlog still carries Git-managed `Order` and `PaymentIntent` research/frontmatter issues. In contrast, the doc-defined runtime work that an MVP actually needs—cart APIs, checkout sessions, order persistence, payment provider abstraction, stock reservations, availability snapshots, and fulfillment runtime—is much thinner or only newly added in storefront-related issues from June 17. [9]

## Release-phased roadmap

| Phase              | Goal                                                                                     | Included issue numbers and titles                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         | Missing issues to create                                                                                                                                                                                                                                     | Dependencies                                                                                                                                                     | Exit criteria                                                                                                                                                                                                                      |
|--------------------|------------------------------------------------------------------------------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| **Phase 1 alpha**  | Finish the architecture proof for developer-facing foundation                            | #123 API Admission Controller; #126 Git Transport Authentication and Repository Authorization; #67 API driven repository creation; #77 Product frontmatter; #82 CategoryTaxonomy remaining work; #79 File frontmatter baseline; #134 Separate Markdown Parsing Layer from Catalog Validation; #137 Object Lifecycle Versioning Contract; #174 Namespace Validation and Admission Matrix                                                                                                                                                                                                   | Push rejection diagnostics contract; repository authorization test matrix; PriceSet/PriceList placeholders as non-blocking design stubs; initial File upload/object-storage decision ADR                                                                     | Closed namespace lifecycle foundation (#39, #119), current bootstrap/API/memdb surfaces, current GraphQL reads, and closed `ProductVariant` / `Collection` work  | Git push is admission-controlled end-to-end in local/dev; all core alpha resources have stable contracts; GraphQL reads remain correct after admitted pushes; repository auth is enforced; local test/build/pr-ready gate is green |
| **Phase 2 beta**   | Complete core control-plane, reconciliation, Scylla, and pilot-grade catalog/config APIs | #131 Controller Watch API; #176 Namespace Watch Contract; #177 Status subresource schema; #178 Status write boundaries; #179 Status concurrency semantics; #182 Status integration tests; #183 startup resume; #188 controller integration tests/runbook; #135 stable catalog resource identity; #136 hydrated catalog view; #140 universal Scylla resource table; #141 migration plan; #226/#227/#228/#229/#230/#239 auth foundation and migration; remaining `CategoryTaxonomy` controller tasks #244 and #246; `Translation` tasks #188–#190 if localization is needed for pilot users | PriceList; PriceSet; PriceRule; CurrencyPolicy; stock-location and inventory-config resources; MediaAsset resource; auth service-token and SSH-key management issues; controller conformance suite by resource kind                                          | Alpha admission/lifecycle baseline; variant-first contract (#143); Scylla abstraction docs/tests; watch/status semantics must sit on monotonic `resourceVersion` | Pilot users can run against Scylla with stable-ish APIs; controllers can list/watch/resume safely; authn/authz foundation is in place; pricing and inventory configuration resources exist; operational runbooks exist             |
| **Phase 3 MVP**    | Deliver a usable headless commerce product                                               | #24 Basket Management; #26 Checkout Process; #28 User Profiles; #268 storefront initiative and child tasks #271–#282; #280 cart API and persistence; #281 checkout session API; #274 cart compatibility; #275 checkout handoff; #276 order and customer/session integration; #279 compatibility test suite; #278 demo catalog/deployment docs; admin/backend slice #300–#307 where needed for operator workflows                                                                                                                                                                          | Datastore-only Order API; datastore-only PaymentIntent API; payment gateway abstraction; webhook ingestion/reconciliation; cart/order idempotency; reservation/allocation runtime; fulfillment/shipment baseline; import/migration and import-dry-run issues | Beta watch/status/auth/config foundation; runtime storage model; variant-first contract; file/media support for storefront images; revalidation path             | Storefront-ready catalog and cart/checkout flows work end-to-end; orders and payments are persisted in datastore-only runtime resources; admin/operator workflows exist for MVP operations; docs and compatibility tests pass      |
| **Phase 4 stable** | Harden for enterprise readiness and extensibility                                        | #120 CLI-First Platform Management; #121 Dual-Mode Admin; #132 Tag-Gated Sync Controller; #139 gRPC GitEvent Notification Stream; #240/#243 Git protocol upgrades/optimization initiatives; #29 Product Recommendations and extension/WASI direction; #68 npm-first distribution; #69 release images; #134 Organization Storage Modes; any remaining #140/#141 CRD/universal-table hardening                                                                                                                                                                                              | CRD registration/discovery API; backup/restore; disaster recovery drills; scale/load tests; security review and threat model; audit/compliance policy layer; tenant quotas/governance; compatibility/versioning policy; upgrade/migration guarantees         | MVP production profile; operable eventing path; CRD storage substrate; policy/authz model mature enough for enterprise rollout                                   | Compatibility guarantees defined; backup/restore proven; scale/security reviews passed; extension model supported; multi-tenant governance and observability complete                                                              |

Phase 1 should be intentionally narrow because a lot of the “alpha” story is already in the docs. Local bootstrap, GraphQL reads, memdb, Scylla abstraction, and control-plane mutations are already documented; the missing alpha work is what makes those surfaces trustworthy for developer use: validation contracts, auth on Git transport, lifecycle/versioning, and completing the unfinished core catalog resources rather than diving into checkout too early. [10]

Phase 2 is where GitStore becomes pilotable. The critical backlog in this phase is not UI; it is **safe reconciliation**. The watch/status family (#131, #176–#183, #188), identity/versioning (#135, #137), hydrated storage (#136), and generic Scylla storage (#140–#141) are exactly the dependencies for reliable controllers, stable list/watch behavior, and resume after failure. The auth track (#226–#230, #239) also belongs here because the phase definition calls for authn/authz foundations, not just demo logins. [11]

Phase 3 should be treated as the first time GitStore ships as a product rather than a control plane. The June 17 storefront epic (#268) is useful because it breaks the MVP into independently mergeable slices and explicitly calls out dependencies on `ProductVariant` as the sellable unit, media resources, basket, checkout, and revalidation. That storefront issue set is a good Phase 3 scaffold, but it still needs foundational runtime work for datastore-only orders, payments, reservations, allocation, fulfillment, and operator APIs. [12]

Phase 4 should not still be defining what a cart, order, or payment is. It should instead absorb the backlog that is about enterprise-grade packaging, compatibility, extensions, and operational safety: CLI-first management, dual-mode admin, release promotion, event streams, Git protocol hardening, product recommendations, release/distribution, and storage governance. The milestone setup in GitHub does not currently reflect this phase structure: the repository only shows `v0.0.1` and `0.0.2`, with the older milestone overdue, so milestone hygiene should be fixed alongside the roadmap. [13]

## Dependency graph and ordered backlog

The critical path is best understood as a control-plane chain rather than a feature checklist:

```text
Namespaces and repository control plane
  (#39 closed, #119 closed, #67 partial/documented)
    ->
Git transport authz and validating admission
  (#126, #123, #174)
    ->
Core Git-backed resource contracts complete
  (#77, #82, #79, optionally #78)
    ->
Parser and validation separation
  (#134)
    ->
Stable object identity and versioning semantics
  (#137, #135, #143)
    ->
Hydrated persistence model and Scylla shape
  (#136, #140, #141)
    ->
Watch, status, resume, and controller runtime reliability
  (#131, #176, #177, #178, #179, #182, #183, #188)
    ->
Pilot-grade auth runtime and policy migration
  (#226, #227, #228, #229, #230, #239)
    ->
Runtime commerce APIs
  (new Order/PaymentIntent runtime issues, plus #280, #281, #274, #275, #276)
    ->
Storefront compatibility and operator workflows
  (#268, #271-#282, #300-#307)
    ->
Stable release promotion, events, extensions, scale, security
  (#132, #139, #29, #68, #69, new backup/load/security work)
```

This ordering follows the repository’s own documented dataflow. A push enters through the API, is forwarded to the Git service, passes pre-receive validation, triggers post-receive admission, hydrates resources into the datastore, and is then reconciled by controllers. That means **admission, identity/resourceVersion semantics, hydrated storage, and watch/resume are the actual critical path**, not storefront code. Storefront work should start only after the underlying control-plane semantics are safe enough to avoid rebuilding APIs around unstable invariants. [14]

The recommended ordered backlog, optimized for small independently mergeable increments, is:

1. **#123 API Admission Controller contract** — stabilize the validating request/response envelope first. [15]  
2. **#174 Namespace Validation and Admission Matrix** — make control-plane validation explicit before deepening catalog work. [16]  
3. **#126 Git Transport Authentication and Repository Authorization** — Git push admission is not meaningful without repo auth. [17]  
4. **#67 API driven repository creation close-out** — the docs show this mostly exists, so close the gap and resolve ambiguity. [18]  
5. **#77 Product frontmatter** — finish the still-open core resource. [19]  
6. **#82 CategoryTaxonomy completion** — especially the remaining deletion and controller semantics. [20]  
7. **#79 File frontmatter baseline** — storefront media primitives depend on this; `MediaAsset` remains a separate follow-on layer. [21]  
8. **#134 Separate Markdown Parsing Layer from Catalog Validation** — prevents validator complexity from swallowing resource evolution. [22]  
9. **#137 Object Lifecycle Versioning Contract** — make rename/edit/delete/recreate semantics explicit. [23]  
10. **#143 ProductVariant as sellable unit** — force downstream flows to target the right primitive. [24]  
11. **#136 Hydrated Catalog View in ScyllaDB** — establish the materialized read side. [25]  
12. **#140 Universal ScyllaDB Resource Table** — start the extensible persistence substrate after identity semantics are stable. [26]  
13. **#131 Watch API contract** — now the event cursor semantics can be built on the stable read model. [27]  
14. **#177 / #178 / #179 status contract and conflict semantics** — status writes and controller authorship must be strict before pilots. [28]  
15. **#182 / #183 / #188 resume/runbook/integration tests** — convert semantics into pilot reliability. [29]  
16. **#226 / #228 / #229 / #230 / #239 auth foundation** — establish login surface, capability contracts, policy migration, and regression coverage. [30]  
17. **New pricing resource issues** — `PriceList`, `PriceSet`, `PriceRule`, `CurrencyPolicy`. These are required by the documented resource model and by beta scope, but the current roadmap coverage is thin. [31]  
18. **New datastore-only runtime commerce issues** — `Order`, `PaymentIntent`, cart snapshots, inventory reservation/allocation, fulfillment. The docs already classify these as runtime resources. [32]  
19. **#280 / #274 cart backend + compatibility** — after runtime primitives exist. [33]  
20. **#281 / #275 checkout session + handoff** — then move from cart to checkout. [34]  
21. **#276 customer/session integration** — layer auth-aware order status and checkout on top of the runtime path. [35]  
22. **#282 / #277 / #279 / #278 storefront revalidation, tests, docs** — finish the MVP storefront packaging. [36]  
23. **#300–#307 admin backend/operator APIs** — build operator workflows only after the runtime APIs exist. [37]  
24. **#132 / #139 / #68 / #69 / #29** — stable-grade release gating, eventing, distribution, and extension work. [38]

## Missing implementation areas

The missing work is not random; it clusters around specific subsystems.

### Control plane

The control plane still lacks a first-class issue track for **namespace defaults, limits, quotas, and governance enforcement**, even though `Namespace` is already modeled as a Git-backed control-plane resource with fields such as `defaults` and `limits`. There is also no strong issue coverage for **tenant lifecycle beyond create/delete**, such as soft delete, suspension, or namespace policy inheritance. Since current namespace issues are labeled `area/auth`, this domain is also under-labeled conceptually. citeturn21view1turn18view0turn14view0turn36view0

### Git service and admission

The admission framework for the post-receive path lives in `gitstore-api/internal/admission/`. It provides a four-phase Kubernetes-style chain (`MutatingAdmissionPolicy → MutatingAdmissionWebhook → ValidatingAdmissionPolicy → ValidatingAdmissionWebhook`), implemented by `admission.Chain` in `admission/chain.go`. Per-kind policy implementations (ProductVariant CEL/options validation, CategoryTaxonomy cycle detection, and Product/Collection stubs) are in `admission/catalog/`. The pre-receive schema validator (`cataloggrpc.ValidateResources`) remains separate and is not part of this chain.

The validate/admit split is documented, but the backlog still needs explicit work for **push rejection diagnostics**, **quarantine object lifecycle and cleanup**, **ref update policy matrices**, and **conformance tests across Git clone/fetch/push over Smart HTTP and SSH**. The Git protocol implementation notes also suggest future work around packet-line handling, receive-pack, hook integration, and Git protocol upgrades, which belongs after the alpha contract but before stable hardening is declared complete. citeturn30view0turn14view4turn14view5turn26view5

### Catalog resources

The resource docs enumerate more Git-backed resources than the roadmap materially covers. In particular, **pricing configuration** (`PriceList`, `PriceSet`, `PriceRule`, `CurrencyPolicy`) is explicitly documented as Git-backed, and so are `Locale`, `MediaAsset`, and several extension resources. Yet the visible issue structure is still dominated by frontmatter migration for core catalog kinds, not by the pricing/configuration layer that beta and MVP actually need. That is a significant backlog gap. citeturn21view1turn21view2turn17view4turn17view5

### Datastore and Scylla

The Scylla abstraction is documented, but there is no complete phase-aligned plan yet for **runtime commerce tables and contract tests** beyond catalog-centric storage. The current implementation doc lists initial Scylla migration files around product/category/collection/namespace/repository tables. That is not enough for MVP commerce, which needs runtime persistence for carts, checkout sessions, orders, payment intents, reservations, allocations, and fulfillment records. citeturn25view0turn20view0turn22view1turn22view2

### GraphQL and API

Catalog reads are documented, but several API families are still missing or only newly proposed: **cart mutations and persistence**, **checkout session APIs**, **order status APIs**, **payment-intent APIs**, **file/media APIs**, and **admin/operator APIs**. The API reference explicitly says catalog writes should use Git rather than CRUD mutations, which is correct, but the corresponding runtime APIs must be expanded aggressively for MVP. citeturn28view0turn32view2turn32view3turn40view0

### Reconciliation, status, and watch

This is one of the largest remaining gaps. The repository has strong issue coverage for generic status/watch semantics, but it still needs **kind-specific reconcilers** and **controller conformance tests per resource kind**, not just runtime plumbing. Category work shows the right shape—deletion semantics, reconciliation, conditions—but equivalent controller tracks for `Product`, `ProductVariant`, `Collection`, `File`, and `Translation` are not yet visible enough. citeturn19view3turn11view1turn10view2turn10view3

### Authn and authz

The auth track is promising but incomplete. The repository has a pluggable-auth direction, a closed namespace foundation, and open issues for identity/access contracts, login-surface consolidation, capability validation, policy migration, and runtime regression coverage. Missing pieces include **Git service tokens**, **SSH key lifecycle management**, **JWKS rotation and issuer trust policy**, **service-to-service auth between controllers and API**, and **operator-grade diagnostics for permissions failures**. citeturn25view2turn10view3turn11view0turn40view0

### Checkout, orders, payments, inventory, files, extensions, docs, and operations

The MVP/stable gap is concentrated here:

- **Checkout/orders/payments:** the docs already define datastore-only `Order` and `PaymentIntent`, but the current issue set still leans on Git-managed research instead of runtime implementation. citeturn20view0turn21view3turn38view0turn39view2
- **Inventory/fulfillment:** the docs define `StockLevel`, `InventoryLedgerEntry`, `Reservation`, `Allocation`, `FulfillmentOrder`, `Shipment`, and `TrackingEvent`, but the older inventory issue still assumes stock belongs in product frontmatter. That issue needs replacement by runtime inventory work. citeturn22view1turn22view2turn34view1turn14view7
- **Files/media/object storage:** the docs clearly separate Git-backed manifests from payload storage and recommend Git LFS or object storage with indirect credential references, but the backlog still needs uploads, signed URLs, processing pipelines, and media derivatives. citeturn22view3turn22view4turn17view4
- **Extensions/CRDs:** the universal resource-table direction is present, and recommendations point toward CRD/WASI extensibility, but there is still no clearly named issue family for CRD registration/discovery and schema lifecycle. citeturn19view7turn34view3
- **Docs/developer experience:** bootstrap and local guides exist, but import/migration dry-runs, production deployment profiles, and operator runbooks are not yet rounded out. citeturn29view0turn30view1turn21view5turn40view1
- **Tests/CI/operations/security:** `make pr-ready` exists, but stable-phase artifacts such as load testing, backup/restore, security review, and upgrade guarantees are not clearly represented in the current backlog. citeturn35view0turn35view2turn35view5turn26view6

## Issue hygiene recommendations

The current issue hygiene problem is not lack of ideas; it is **semantic drift**.

The highest-priority stale or wrong-model issues are **#30 Inventory Management**, **#80 Order frontmatter research**, **#81 PaymentIntent frontmatter research**, and their follow-on tasks (#196–#203). Issue #30 still assumes stock lives in product frontmatter and that inventory adjustments are auditable through Git history, but the repo’s resource docs now model inventory as datastore-only runtime resources such as `StockLevel`, `InventoryLedgerEntry`, `Reservation`, and `Allocation`, and the sellable primitive is `ProductVariant`, not `Product`. Issues #80 and #81 have the same problem: the docs place `Order` and `PaymentIntent` in datastore-only runtime resources and explicitly caution against Git-backed orders for the main purchase lifecycle. These issues should not remain on the critical path as frontmatter work. They should be closed with ADR outcomes, or rewritten as runtime API/persistence epics. citeturn34view1turn22view1turn14view7turn38view0turn39view2turn20view0turn21view3

Several issues are duplicates or should be normalized into parent/child structures rather than living as peers. `#274` and `#280` clearly overlap, with #280 explicitly the backend server-side counterpart of storefront cart compatibility. `#275` and `#281` have the same relationship for checkout handoff. `#277` and `#282` appear to describe the same revalidation problem at different abstraction levels. `#300`–`#307` are also really children of the broader admin direction in `#121`, but they are currently floating as independent top-level issues. These are not harmful duplicates if normalized properly, but they are confusing in the current state. citeturn32view1turn32view2turn32view3turn32view5turn40view1turn40view0turn19view2

A second hygiene problem is overlapping cross-cutting identity work. `#135` Stable Catalog Resource Identity and `#137` Object Lifecycle Versioning Contract overlap heavily; `#137` is the broader cross-resource lifecycle matrix, while `#135` is the catalog-focused manifestation. Those should be explicitly parent/child or merged into one epic plus implementation subtasks, otherwise version/identity semantics will fragment across resource kinds. citeturn10view1turn19view6

Many issues need sharper acceptance criteria for implementation, especially the older umbrella initiatives. `#24` Basket Management, `#26` Checkout Process, `#27` Order Tracking, `#29` Product Recommendations, and `#30` Inventory Management read like product goals more than engineering epics. By contrast, the newer June 2026 storefront issues are much better sliced and dependency-aware. I would keep the older umbrellas as epics only if their child structure is cleaned up; otherwise I would split them aggressively and move the actual work into concrete backend/runtime issues. citeturn34view0turn31view0turn31view1turn34view3turn34view1turn33view0turn32view0turn32view2turn32view3

Labels and project fields also need an update. The current label set has area and priority labels, but it lacks **phase labels**, **storage-source labels** such as `git-backed` vs `datastore-only`, and **workflow/state labels** such as `blocked`, `needs-adr`, `needs-acceptance-criteria`, or `control-plane`. That is why namespace issues ended up under `area/auth`, and why runtime-vs-desired-state confusion has persisted. The milestone setup is likewise disconnected from the requested alpha/beta/MVP/stable release model; the repository only shows `v0.0.1` and `0.0.2`. Finally, several issues document dependencies in body text while GitHub relationship fields still show “None yet,” which is a maintenance smell. citeturn36view0turn37view0turn18view0turn14view1turn39view0

## Suggested new GitHub issues

The missing issues below would make the roadmap materially more executable:

| Title                                                                     | Phase          | Area label                             | Priority | Dependencies                         | Acceptance criteria                                                     |
|---------------------------------------------------------------------------|----------------|----------------------------------------|----------|--------------------------------------|-------------------------------------------------------------------------|
| Define `PriceList` Git-backed resource contract                           | Phase 2 beta   | `area/catalog`                         | p1       | #40, #143                            | JSON Schema, docs examples, validation errors, GraphQL read type        |
| Define `PriceSet` Git-backed resource contract                            | Phase 2 beta   | `area/catalog`                         | p1       | #40, #143                            | Variant-targeted pricing schema, examples, validation tests             |
| Define `PriceRule` and `CurrencyPolicy` configuration resources           | Phase 2 beta   | `area/catalog`                         | p2       | PriceList/PriceSet                   | Selector semantics, currency handling, docs, tests                      |
| Implement datastore-only `Order` API and persistence                      | Phase 3 MVP    | `area/checkout`                        | p1       | Beta auth/runtime storage            | CRUD-lite runtime API, idempotency, order status query, tests           |
| Implement datastore-only `PaymentIntent` API and provider abstraction     | Phase 3 MVP    | `area/checkout`                        | p1       | Runtime Order API                    | Provider-agnostic contract, sandbox provider, idempotency, tests        |
| Implement inventory runtime baseline                                      | Phase 3 MVP    | `area/catalog` or new `area/inventory` | p1       | #143, pricing runtime                | `StockLevel`, ledger, reservation, allocation, availability snapshot    |
| Implement fulfillment runtime baseline                                    | Phase 3 MVP    | `area/checkout`                        | p2       | Runtime Order API, inventory runtime | FulfillmentOrder, Fulfillment, Shipment, tracking query path            |
| Add object-storage upload and signed URL workflow for `File` | Phase 2 beta   | `area/catalog`                         | p2       | #79                                  | Signed upload/download flow, checksum enforcement, processing kickoff   |
| Add CRD registration and schema discovery API                             | Phase 4 stable | `area/crd`                             | p2       | #140                                 | Register schema, validate instance, list CRDs, docs, tests              |
| Add backup/restore and disaster-recovery runbook                          | Phase 4 stable | `area/infra`                           | p1       | Scylla production profile            | Backup procedure, restore drill, RPO/RTO documented, test evidence      |
| Add load and scale test suite for admission/watch/cart flows              | Phase 4 stable | `area/infra`                           | p1       | Beta+MVP core complete               | Reproducible load tests, latency/error budgets, report docs             |
| Add security threat model and authz review                                | Phase 4 stable | `area/auth`                            | p1       | Auth foundation complete             | Threat model, trust boundaries, token handling review, remediation list |

The acceptance pattern above should become the default issue template for new engineering work: a narrow title, explicit phase, explicit dependency list, and concrete merge-ready criteria. That would be a substantial improvement over several of the older umbrella issues, whose acceptance criteria are product outcomes rather than implementation checkpoints. citeturn34view0turn31view0turn34view3turn36view0

## Risks, sequencing concerns, and the next 10 issues

The biggest sequencing risk is trying to ship storefront and admin work before the control-plane invariants are stable. The docs already assume an architecture where admission, hydration, status, and reconciliation work together; if those semantics are still moving, every cart/checkout/storefront integration will be built on sand. The variant-first sellable model is another non-negotiable dependency: if downstream flows still accept product-only semantics, inventory, pricing, and cart correctness will fragment. citeturn30view0turn14view7turn32view0turn32view1

A second risk is backlog inconsistency around storage source of truth. GitStore’s docs are already careful about desired-state vs runtime-state separation. If the roadmap continues to mix Git-backed `Order`/`PaymentIntent` research with datastore-only runtime plans, implementation will stall in design debates and duplicate contracts. The right move is to close the ADR-style debates quickly and move runtime commerce into concrete MVP issues. citeturn20view0turn20view3turn38view0turn39view2

A third risk is under-scoping pricing and inventory configuration. The beta definition requires pricing config and inventory config, and the resource docs already sketch those resources, but the visible issue roadmap does not yet elevate them as first-class work. That gap will eventually block MVP cart and checkout semantics even if storefront integration starts moving. citeturn21view1turn22view1turn32view2turn32view3

The most practical “next 10 issues to implement” list is:

1. **#123 — API Admission Controller**  
2. **#174 — Namespace Validation and Admission Matrix**  
3. **#126 — Git Transport Authentication and Repository Authorization**  
4. **#67 — API driven repository creation**  
5. **#77 — Support Kubernetes-style Product frontmatter**  
6. **#82 — Finish remaining CategoryTaxonomy work**  
7. **#79 — Support Kubernetes-style File frontmatter**  
8. **#134 — Separate Markdown Parsing Layer from Catalog Validation**  
9. **#137 — Object Lifecycle Versioning Contract for UID and resourceVersion**  
10. **#131 — Controller Watch API with resourceVersion Resume**  

That sequence is the shortest route to a credible alpha-to-beta path because it closes the core architecture proof, then stabilizes the event/versioning substrate needed for everything else. 
After those ten, the next block should be status/resume/reliability (`#177`, `#178`, `#179`, `#182`, `#183`, `#188`), then auth foundation (`#225`, `#226`–`#230`, `#239`), 
then newly created pricing/runtime commerce issues, and only then the bulk of the storefront/admin backlog.
