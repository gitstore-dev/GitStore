# GitStore Admin — Product Requirements

**Status:** Proposed product direction; not a statement of shipped functionality

**Date:** 2026-10-29

**Audience:** Product, design, frontend, commerce backend, and platform contributors

**Design reference:** [GitStore Admin II in Stitch](https://stitch.withgoogle.com/projects/12282348560453912861)

**Brand reference:** [`gitstore-assets/branding/concept-v2`](../public/branding/)

> Admin-only branch note: this PRD preserves the broader product plan. Backend source links point to the original GitStore checkout revision; backend services are intentionally absent here. The shell, navigation, layouts, Catalog / Products, and Home / Overview, My work, Recent activity, and Setup guide pages with shared per-store local demo sessions are implemented. The other 74 destinations remain placeholders; mock behavior does not establish backend capabilities.

## 1. Product direction

Build a complete commerce administration experience for merchants, catalog teams,
operators, developers, and their agents. Familiar commerce navigation provides the
entry point. GitStore's distinctive experience makes catalog changes visible,
reviewable, attributable, and recoverable, with resource health understandable
without requiring Git or Kubernetes expertise.

**Creative direction: Split / Return — a living commerce workspace.** Mineral
surfaces, forest-black typography, vermilion paths, and precise angular geometry
give the interface its identity. Cards respond to interaction, state changes
travel through relationships, and navigation preserves visual continuity.
HTML-in-Canvas and WebGPU are product design tools, not performance features
looking for a use case.

The signature interaction is **a resource card opening into its working context**:
its image and title remain continuous while related resources and the inspector
arrive around it. An operator can move from a warning halo to its cause, inspect
the proposed correction, and follow the confirmed result.

### Goals

- Support the complete merchant journey: configure a store, build a catalog,
  sell, fulfill, support customers, understand results, and extend operations.
- Make catalog preparation, review, publication, and recovery first-class jobs.
- Expose implemented GitStore strengths: canonical Git-backed authoring,
  admission diagnostics, resource provenance, desired versus observed state,
  durable watches, concurrency protection, and dependency-aware deletion.
- Deliver expressive motion, spatial exploration, and resource-state lighting
  alongside efficient tables, keyboard navigation, and accessible forms.
- Let teams and agents work through the same supported lifecycle and permissions.
- Establish the full navigation now while delivering working domains in phases.

### Boundaries

This PRD defines the destination, not a promise to implement every commerce
backend in the first Admin release. It does not authorize Git replication,
sharding, autoscaling, payment infrastructure, or a hosted billing business.
Neither a design mockup nor a schema declaration establishes production readiness.
Detailed API contracts and implementation plans are separate follow-up work.

## 2. Users and primary jobs

| User | Primary jobs | Default emphasis |
| --- | --- | --- |
| Merchant / store owner | Launch assortments, handle exceptions, understand sales, manage staff | Home, Orders, Catalog, Insights |
| Catalog editor / merchandiser | Author products and variants, organize taxonomy, curate collections and media | Catalog, Changes & releases |
| Reviewer / release manager | Review differences, assess impact, approve and publish eligible content | Changes & releases |
| Fulfillment / support operator | Allocate stock, fulfill orders, resolve returns and refunds | Orders, Inventory, Customers |
| Platform administrator | Manage namespaces, repositories, identities, permissions, and resource failures | Control plane, Settings |
| Developer / integration operator | Diagnose admission, build extensions, inspect jobs and integration failures | Apps & automation, Control plane |
| Agent acting for a team | Prepare bounded changes and report outcomes with attributable identity | Same permitted resources and lifecycle as human callers |

These are UX profiles, not hard-coded authorization roles. The configured
AuthN/AuthZ provider remains authoritative.

## 3. Competitive references and design decisions

Use the references for established jobs and interaction conventions; retain
GitStore's own brand and lifecycle model. No competitor artwork is reused.

| Reference | Relevant pattern | GitStore decision |
| --- | --- | --- |
| [Medusa Admin](https://docs.medusajs.com/user-guide) | Domain sidebar, command search, saved table views, bulk work, theme and profile controls | Make everyday commerce tasks fast and discoverable; keep technical details contextual |
| [Saleor channels](https://docs.saleor.io/developer/channels/overview) and [Dashboard extensions](https://docs.saleor.io/recipes/extending-dashboard) | Channel-aware commerce and extensions embedded in working context | Keep channel scope visible; allow permission-aware widgets, routes, and actions |
| [Vendure navigation](https://docs.vendure.io/current/framework/reference/dashboard/extensions-api/navigation) | Catalog, sales, customers, marketing, system, and settings domains | Give commerce and system operations clear homes; provide extension placement without navigation sprawl |
| [Shopify Admin](https://help.shopify.com/en/manual/shopify-admin) | One operational hub spanning commerce, logistics, store configuration, and merchant workflows | Support the full merchant journey with a coherent shell and task-oriented labels |

GitStore's opportunity is the combination of repository-backed catalog provenance,
declarative desired state, admission feedback, and controller-observed health in
the same merchant workspace. This is a positioning decision, not a claim that
other platforms lack all Git tooling, audit histories, or declarative features.
For example, Saleor documents a Git-oriented configuration workflow; generic
“Git integration” alone is not sufficient differentiation.

## 4. Verified starting point and capability boundaries

The following is a source inspection baseline for this PRD, not a completed
runtime acceptance audit. The inspected checkout has ongoing implementation work.
Revalidate behavior and permissions against the target release before enabling UI.

| Area | Existing foundation in this checkout | Admin requirement / remaining dependency |
| --- | --- | --- |
| Products | Typed list/read, Git-driven create/update/delete, status, category resolution, typed watch | Product list, grid, editor, diagnostics, stale-write handling; broader merchandising tools remain future work |
| Categories | Read/list and lifecycle mutations, taxonomy relationships and watch surface | Tree, dossier, bounded relationship workspace; advanced drag/reparent needs supported validation and mutation semantics |
| Variants | Typed list/read and product relationship; authoring through Git push | Start with inspection; in-app variant authoring requires an explicit supported write path |
| Collections | Typed list/read, declarative label selectors, related-product view; Git-push authoring | Inspect selectors and membership; authoring and scalable membership previews require backend work |
| Files / media | File lookup, typed durable watch, source/checksum/processing and resolved-variant schema | Inspect known files; searchable asset library, upload, processing guarantees and editors need separate capability verification |
| Namespaces / repositories | Typed lifecycle surfaces, metadata, status and watches | Context selection, management, desired/observed comparison, deletion blockers |
| Identity | Pluggable local/OIDC/service-account providers, RBAC provider, ServiceAccount operations | Provider-aware login and identity controls; general staff invitation, role editing and account listing are not implied |
| Live status | Durable resource watch journal and resource-version contracts | List/bootstrap/watch, reconnect, expiry recovery, fresh/stale indicators; watch support is verified per kind |
| Concurrency | Resource versions, generation, expected-version checks and controller ownership | Preserve user edits on conflict; never present controller status as merchant-editable fields |
| Releases / publication / workflows | Contributor designs exist | Review queues, approvals, immutable releases, target publication, history and recovery UX need implementation; no simulated production actions |
| Orders / customers / checkout / payments / fulfillment | Not established by the inspected Admin schema | Future commerce domains; catalog inventory/pricing declarations do not establish operational ledgers |
| Global search / analytics / extension registry | Not established as complete services | Design shell entry points now; add bounded search, reporting, registry and permission contracts before activation |

Variant relationships and collection membership currently include slice-based
connection paths. Their presence must not be interpreted as scalable server-side
pagination. Large relationship workspaces require bounded query paths first.
Do not compensate by downloading the complete catalog into the browser.

Source anchors: [`shared/schemas`](https://github.com/gitstore-dev/GitStore/tree/1465aa9414ad87a94b328c20935726db57f1956a/shared/schemas),
[`resolvers`](https://github.com/gitstore-dev/GitStore/tree/1465aa9414ad87a94b328c20935726db57f1956a/gitstore-api/internal/graph/resolver),
[`catalog CDC registration`](https://github.com/gitstore-dev/GitStore/blob/1465aa9414ad87a94b328c20935726db57f1956a/gitstore-api/internal/datastore/scylla/catalog_cdc.go),
[`auth providers`](https://github.com/gitstore-dev/GitStore/tree/1465aa9414ad87a94b328c20935726db57f1956a/gitstore-api/internal/auth/provider), and
[`controller list/watch`](https://github.com/gitstore-dev/GitStore/tree/1465aa9414ad87a94b328c20935726db57f1956a/gitstore-controller-manager/internal/listwatch).
Publication and workflow designs are described in
[`catalog release and publication`](https://github.com/gitstore-dev/GitStore/blob/1465aa9414ad87a94b328c20935726db57f1956a/docs/ADRs/0014-catalog-release-and-publication.md)
and [`commerce workflows`](https://github.com/gitstore-dev/GitStore/blob/1465aa9414ad87a94b328c20935726db57f1956a/docs/implementation/037-custom-commerce-workflows.md).

## 5. Full navigation and route structure

### Shell and context

Use one collapsible primary sidebar, a compact global header, and a contextual
inspector. Expanded navigation shows the selected domain's children; do not expose
the entire tree at once. Support favorites and recent destinations without
duplicating canonical pages.

The header contains the GitStore identity, workspace/namespace selector, command
search, activity inbox, and contextual channel/target selector. Account, theme,
motion preferences, shortcuts, and help live in the sidebar footer.

Namespace is the current resource boundary. A future merchant-facing “store” may
map to one or more namespaces only after that model is defined; do not silently
equate namespaces, stores, repositories, markets, channels, and publication
targets. Repository and branch context appears on repository-backed work.
Switching namespace clears incompatible selections and subscriptions, protects
unsaved edits, and checks access again. Never reuse one workspace's cached data
in another workspace.

### Target navigation inventory

Routes below are proposed UI routes, not existing API endpoints. Namespace-scoped
routes sit under `/w/:namespace`; cluster-scoped control-plane pages sit under
`/platform`. Every collection route has index, create (where supported), and
`:id` detail destinations, with permission checks and explicit unavailable states.

| Primary destination | Child destinations | Route family | Delivery |
| --- | --- | --- | --- |
| Home | Overview, My work, Recent activity, Setup guide | `/home` | Foundation; real operational metrics only |
| Orders | All orders, Draft orders, Abandoned checkouts, Fulfillments, Returns & exchanges, Refunds | `/orders`, `/checkouts`, `/fulfillments`, `/returns`, `/refunds` | Future commerce |
| Catalog | Products, Variants, Categories, Collections, Attributes & options, Product types, Media | `/catalog/{products,variants,categories,collections,attributes,types,media}` | Existing resource inspection first; authoring gated by kind |
| Inventory | Stock overview, Locations, Reservations, Transfers, Adjustments, Incoming stock | `/inventory/{stock,locations,reservations,transfers,adjustments,incoming}` | Future operational inventory |
| Customers | Customers, Groups & segments, Companies / B2B | `/customers`, `/customer-groups`, `/companies` | Future commerce |
| Pricing & promotions | Price lists, Promotions, Discount codes, Campaigns, Gift cards & store credit | `/pricing`, `/promotions`, `/discounts`, `/campaigns`, `/gift-cards`, `/store-credit` | Future commerce; variant declarations inspectable in Catalog |
| Content | Pages, Navigation menus, Structured content, Translations | `/content/{pages,menus,models,translations}` | Future content capabilities |
| Channels & markets | Sales channels, Markets & regions, Currencies, Catalog assignments, Publication targets | `/channels`, `/markets`, `/currencies`, `/assortments`, `/publication-targets` | Future commercial and publication models |
| Changes & releases | My changes, Review queue, Admission reports, Releases, Publications, Compare & recover, Activity | `/changes`, `/reviews`, `/admission`, `/releases`, `/publications`, `/compare`, `/activity` | Diagnostics first; durable review/release/history services required |
| Insights | Overview, Sales, Products, Customers, Inventory, Saved reports, Exports | `/insights/{overview,sales,products,customers,inventory,reports,exports}` | Future reporting; no mock GMV |
| Apps & automation | Installed apps, Connections, Workflows, Runs, Webhooks & deliveries, Jobs, Developer tools | `/apps`, `/connections`, `/workflows`, `/runs`, `/webhooks`, `/jobs`, `/developer` | Future registries and operations; existing provider configuration is not an app marketplace |
| Control plane | Namespaces, Repositories, Resource explorer, Reconciliation health, Service accounts, Policies, System health | `/platform/{namespaces,repositories,resources,reconciliation,service-accounts,policies,health}` | Existing lifecycle inspection first; missing list/health/policy-edit surfaces gated |
| Settings | Store details, Team & access, Shipping & delivery, Payments, Taxes & duties, Checkout, Notifications, Domains, Languages, Data & privacy | `/settings/:section` | Future settings by domain; provider-aware account controls first |

Cluster-scoped pages may offer a namespace filter, but remain explicitly platform
scoped. ServiceAccounts are cluster scoped; a namespace switch must not imply
that their identity or credentials belong to that namespace.

### Navigation behavior and availability

- Production navigation shows authorized, enabled capabilities. Hide unauthorized
  destinations without leaking their counts or names through search or recents.
- Authorized users see disabled actions with a reason when a resource is blocked,
  stale, terminating, or read-only; lack of write permission produces a read-only
  experience where read access is allowed.
- Unimplemented domains appear in the design prototype with a clear “Planned”
  label. Production may expose an owner-facing roadmap separately; do not fill
  the daily sidebar with dead links or empty fake dashboards.
- Settings links to canonical location, channel, identity, and integration pages;
  do not create competing editors under multiple navigation groups.
- All destinations support deep links, browser Back/Forward, and reload. Preserve
  supported filters, sorting and selected view in the URL; never place secrets
  or sensitive free-form customer queries in URLs.
- Command search distinguishes navigation, resource lookup, and actions. An
  unavailable global resource search must not be implemented as a catalog scan.
- On narrow screens use a navigation drawer and full-screen inspector. No task
  requires pointer hover, dragging, or a spatial canvas.

## 6. Domain requirements

### Home and onboarding

Home prioritizes work requiring attention: rejected changes, unresolved references,
stale observed state, pending reviews, and later order/stock exceptions. Let users
pin permitted modules. Before commerce reporting exists, show resource activity
and setup progress rather than sales widgets populated with zero or fabricated
data. Setup covers identity, namespace, repository, first catalog resource, and
later channel/provider activation. Distinguish no data, no access, disconnected,
and not yet configured.

#### Recent activity and shared Home demo sessions

Visual reference: [P03 Home — Recent activity, GitStore Admin II](https://stitch.withgoogle.com/projects/12282348560453912861?node-id=f78ec057372d4f48b906da5ac5de68d9).
The implementation uses the timeline/inspector composition without its invented
mesh, signing, deployment, broker, export, or live-polling claims.

- Atelier Kyoto starts with 24 immutable dated events; Studio Berlin starts empty.
  Show 10 matching entries initially and add 10 per Load more. Filter title/file
  name, actor, type, category, and UTC date; group newest-first by recorded date.
- Inspect historical Before/After fields and distinguish historical outcomes from
  current readiness/publication. Keep source type and `.md` path in Technical details.
- Recover complete authored fixture snapshots for Product, CategoryTaxonomy,
  Collection, and File metadata. Product has no SKU. File-backed catalog types,
  including ProductVariant, use **Title (`spec.title`)** rather than Name. File
  display names are separate. Product snapshots include title, description,
  category/media references; Category snapshots include title, description, parent;
  Collection snapshots include title, description, ordered products; File snapshots
  include display name, alt text, and caption. These are local UI models, not API contracts.
- Recovery compares the current snapshot with the selected event's After version,
  validates references/category cycles, and requires confirmation. It atomically
  updates the demo catalog, appends history, and adds a read-only Completed recovery
  entry to My work. Preserve existing draft bases, proposals, and unsaved buffers.
  Do not change identity, paths, readiness, publication state, or media bytes.
- Overview, My work, and Recent activity share one in-memory session per store,
  retained across all routes and store switches until reload or a confirmed reset.
  Reset restores all Home state for only the selected store. Draft buffers persist;
  stale draft bases are disclosed rather than silently overwritten. Explicit saves,
  submissions, discards, reviews, and resolution actions enter the shared history.
  These actions do not imply publication or supported backend capabilities.
- Keep controls page-local, use fixed fixture/revision-based timestamps, retain a
  selected historical entry after recovery, and explicitly offer View recovery event
  when filters hide a new entry. Errors, restrictions, and stale-feed views are
  Storybook presentation specimens. No authentication, API calls, persistence, or export.

#### My work: implemented local demo

Visual reference: [P02 Home — My work, GitStore Admin II](https://stitch.withgoogle.com/projects/12282348560453912861?node-id=4bc79cb5c65045cba3bbe9a3c29b4e9e).
Use P02's personal work list and adjacent change preview within the existing shell.
Its Git commands, mesh claims, staging workflows, offline storage, and invented
production controls are not requirements for this increment.

- Four counted views: **Drafts**, **Submitted**, **Assigned to me**, **Completed**.
  Search item titles within the selected view and sort by last updated or title.
  Counts always describe the complete view, independent of search.
- Atelier Kyoto: two drafts, two submitted changes, two assigned reviews, one
  completed review. Studio Berlin: empty workspace with a link to Overview.
  Product identities match Overview examples and share the current authored demo catalog. Draft bases and proposals remain independent snapshots.
- Inspector: author/reviewer, fixed update time, field-by-field Before/After values,
  outcome/review note, and `.md` source path under Technical details.
- Draft editing is limited to product Title (`spec.title`) and description. Titles must contain
  non-whitespace text; descriptions may be empty. Save updates only the local
  draft; cancel restores saved text. No pricing, inventory, variants, or creation.
- Confirm before discarding unsaved edits for selection, inspector close, tab
  changes, or reset. Page exits preserve saved demo work and draft buffers. Submit
  only saved, changed drafts; confirmed submission moves them to Submitted.
  Confirmed discard removes the draft.
- Assigned reviews can be approved or returned with a required review note after
  confirmation. Both move to Completed with the actual review outcome. Approval
  does not mean publication or readiness. Submitted/completed items are read-only.
- Clearly disclose Demo data and provide Reset demo. Saved work and draft buffers survive page exits and namespace switches; reload or a confirmed reset restores fixtures. Announce outcomes and restore keyboard
  focus. No persistence, authentication, network traffic, or backend writes.
- Reuse the Home card choreography and demand-driven decorative lighting, with
  CSS fallback and reduced-motion support. Workflow colors are decoration, not
  resource-readiness claims. State specimens belong in Storybook.

#### Shared Home composition

Overview and My work share the same responsive content frame, namespace/Home
context mark, title treatment, Demo data badge, reset action, and plain reset
notice without a “Demo workspace” prefix. Summary cards share their count,
state edge, description, and interaction lighting; My work retains tab semantics.
Work cards show merchant-facing resource types and share title/body spacing,
footer divider placement and View details controls. Sort triggers retain their
size when selected values change. Confirmation dialogs must remain above the
inspector at desktop, tablet and mobile sizes and accept keyboard and pointer
input without obstruction.

#### Merchant-facing language

The daily Admin is for merchandisers and store owners. Describe their products,
store setup, changes, and problems to fix. Kubernetes machinery and Git remain
implementation foundations; users do not need their vocabulary to operate a store.
Keep canonical resource kinds and state values in contracts and code. Translate
presentation labels without changing their meaning, routing, filters, or counts.

Home uses **Updates and issues**, **Store status**, and **Store setup** instead of
Resource work, Resource health, and Your foundations. Remove decorative captions
that do not help users understand or act. Use **View details** for item inspection.
Use familiar kind labels such as Product variant, Category, and Media file; present
Namespace as Workspace and Repository as Data source. This does not equate every
namespace with a store or imply a source is connected. Home totals still include
store content and configuration and must say so; they are not product/SKU totals.

| Canonical readiness | Merchant-facing label |
| --- | --- |
| ready | Ready |
| reconciling | Processing |
| blocked | Needs review |
| failed | Has errors |
| stale | Out of date |

Never relabel readiness as Published, Active, Connected, or Available for sale.
Write diagnostics around the issue and the user's next action. Show related items
rather than raw reference terminology. Keep source paths and canonical resource
types behind the inspector's **Technical details** disclosure; leave them available
for debugging and specialist workflows. The control plane may use technical terms
where the user's task requires them. Carry this language policy into future pages.

#### Deferred: store-status drill-down links

Implement these links when the individual Resource index pages support filtering;
keep the current Overview matrix noninteractive until those destinations are usable.

- A resource-kind label and its Total count open that kind's index without a
  readiness filter. In a shared index, retain the kind filter.
- Ready and Processing counts open the same index with the corresponding
  canonical readiness filter. Needs attention includes failed, blocked, and stale resources.
- The Store status heading opens the shared Resource index without kind or
  readiness filters, within the user's authorized scope.
- Preserve the current namespace for namespace-scoped destinations. Respect
  platform scope for platform resources rather than silently broadening scope.
- Encode supported filters in the URL and display them on the destination;
  verify direct links, reload, and browser Back/Forward.
- Use real keyboard-accessible links with descriptive names, such as
  “View 2 Products needing attention.” Keep zero counts and dashes as
  plain text.

### Catalog and media

Provide table/grid toggle, saved views, filters, sort, selection, and a persistent
inspector. Bulk work is bounded, reports each result, and never implies transaction
atomicity across resources without a backend guarantee. “Select all” states
whether it means loaded items or a server-defined result set.

Product details include merchant copy, category, labels/tags, media, variants,
readiness and provenance. Product is the parent descriptor; ProductVariant is the
sellable SKU. Show pricing and inventory declarations at the appropriate variant
level without treating them as an operational stock ledger or final checkout price.

Categories offer tree, table, and spatial views with the same underlying selection.
Collections expose selector definitions, membership and freshness. Previewing a
selector must identify whether results are current or proposed. Reparenting,
batch editing and drag assignment require admission validation and concurrency
checks before persistence.

Media begins with supported File inspection. A later asset library adds upload,
search, derivatives, attachment and reuse. Decode and processing support is
explicit per format; a RAW-looking image in Stitch does not imply RAW decoding,
color-managed editing, transcoding, or delivery infrastructure.

### Changes, review, publication, and recovery

Expose separate milestones: **local draft → submitted/admitted → reconciling →
ready**, and, when available, **release prepared → reviewed → published to target**.
Admission is validation of a write, not approval by a reviewer and not publication.
Publication belongs to the release/target snapshot, not a generic `Published`
condition on source resources.

Every proposed edit should explain changed fields, scope, affected relationships,
and validation results. Show human/agent identity and revision provenance where
available. Durable cross-session review queues, branches, history browsing and
approvals are new capabilities, not UI-only interpretations of the watch journal.
Local drafts must be labeled local and must not claim collaborative persistence.

Compare & recover proposes a new validated catalog change from a prior revision.
It does not force-push, promise reversal of side effects, or roll back orders,
payments, shipments, and refunds. A target snapshot switch is a distinct future
publication operation with eligibility checks and an explicit confirmation.
Reviewers need a readable summary plus optional manifest/technical differences.

### Commerce operations

Orders must support search, filters, customer and line-item context, payment and
fulfillment status, action history, draft orders, cancellation and exception work.
Returns, exchanges, capture and refunds require provider-backed outcomes,
authorization, duplicate-submission protection and clear pending/failure states.

Inventory separates on-hand, reserved and available quantities by location, with
reasoned adjustments and transfer history. Customers support contact data,
addresses, consent, order context and access-controlled export/deletion. B2B,
segments, gift cards and credits remain explicitly phased capabilities.

Pricing, promotions and markets show scope, currency, validity periods, eligibility
and a preview before activation. Transactional and customer data remain private
datastore-only records; they must never be committed into catalog Git repositories.
Content and channel tools expose exactly what reaches each storefront target.

### Extensions and control plane

Extensions may contribute routes, inspector widgets and contextual commands with
declared permissions, placement and capability requirements. A failing extension
must not prevent core navigation. External content requires isolation, explicit
trust boundaries and a documented identity handoff; never pass platform credentials
to arbitrary embedded content.

Control plane provides desired/observed comparisons, admission diagnostics,
condition explanations, last-observed revision and time, and actionable dependency
blockers. Advanced technical identifiers are expandable rather than the default
merchant vocabulary. Standard editors cannot write controller-owned status,
complete finalizers, or invoke provisioning internals as ordinary merchant actions.
Secrets and private keys never appear in cards, canvas textures, logs, search,
screenshots, or persisted browser preferences. Credential issuance uses a dedicated
protected flow and distinguishes public key identifiers from secret material.

## 7. Brand, layout, and interaction system

### Brand foundation

The concept-v2 assets supersede older Stitch visual rules where they conflict.
Use exact capitalization **GitStore**; keep `gitstore.dev` lowercase. Use the
clean SVG symbols for production branding; generated sculptures are reference
art, not a reason to redraw or distort the identity.

| Token / element | Direction |
| --- | --- |
| Forest-black | `#162923`: primary ink and structural emphasis |
| Mineral ivory | `#F4F2E9`: light foundation |
| Deep forest | `#101B18`: dark foundation |
| Vermilion | `#ED572E`: primary action, directional paths, change emphasis |
| Acid sage | `#C8D89D`: restrained secondary accent |
| Typography | Proposed: Archivo Narrow for large display moments, IBM Plex Sans for working UI, IBM Plex Mono for identifiers; verify scripts, font licensing and real data density before implementation |
| Geometry | Angular split/return paths, clipped accents and precise dividers; compact 2–4px card corners, generous spacing where attention needs room |
| Imagery | Merchant-owned product media; brand sculpture for onboarding and empty states, not behind dense operational data |

Do not reuse the current mock storefront photograph as the GitStore logo.
Tenant branding is secondary to the product identity. Do not replace the interface
with monochrome beige, generic purple gradients, constant neon, or an imitation
of a competitor dashboard. Expressiveness comes from composition and deliberate
motion as much as color.

### Resource state lighting

Permit colored halos on resource cards and relationship nodes. This explicitly
replaces the old Stitch prohibition on all colored glows. Keep the logo flat and
crisp: illumination belongs to resource feedback, not the brand mark.

| Resource state | Proposed color | Motion |
| --- | --- | --- |
| Ready / healthy | Emerald `#3D9B72` | Low-intensity steady edge |
| Reconciling | Blue `#4387D6` | Slow directional perimeter sweep |
| Needs attention / blocked | Amber `#D89B35` | One arrival pulse, then steady |
| Failed | Red `#CE493F` | Defined edge; brief transition on new failure |
| Unknown / stale | Slate `#83908A` | Muted broken edge; explicit freshness label |
| Terminating | Amber `#D89B35` | Retreating edge, then steady if waiting on blockers |

These are candidate semantic colors requiring contrast validation, not approved
text colors. All states have a text label and icon. Hover changes elevation and
localized light; selection uses a separate crisp outline. Keyboard focus remains
visually distinct from both selection and health.

Keep authoring, readiness, publication, inventory and transport state independent.
Never show a single “green” halo as proof that all five are healthy. Proposed
health priority: unknown/disconnected when freshness cannot be established,
terminating, failed, blocked, reconciling, ready. Preserve the underlying condition
list and timestamp; terminating resources may still show a blocker badge. A
condition referring to an older generation cannot establish readiness for new edits.

### Motion specification

- Hover/focus feedback: 100–160ms; card lift no more than 3px and tilt no more
  than 2 degrees. Editable fields and reading surfaces stay planar.
- Card/row-to-Product-detail transition: 300ms, with continuous image/Title placement. The full detail route replaces the earlier inspector-only proposal.
- Focus Mode: 240–360ms; navigation retracts while content remains anchored.
- Relationship expansion: 180–300ms after results arrive, with a bounded stagger.
- Reconciliation sweeps: roughly 2.5–4s, only for visible active work. Static
  readiness uses no perpetual pulse. Errors never flash repeatedly.
- A confirmed result may produce one short settling effect. Never animate
  completion before the server confirms the relevant lifecycle milestone.
- Respect reduced motion and an explicit user motion setting. Disable parallax,
  tilt, sweeps and spatial travel; retain instant state/label updates. Pause all
  nonessential rendering in hidden tabs and offscreen scenes.

### Home / Overview motion treatment (approved implementation)

Home keeps resource cards and summary cards in semantic HTML. Filtering, sorting,
reset, and confirmed local demo resolution animate the resource grid using measured
before/after positions and the Web Animations API. Surviving cards travel for about
300 ms; outgoing decorative copies fade/scale for 140 ms; arriving cards settle for
220 ms with a capped 24 ms stagger. Animate the grid's height so the health section
follows the change. Rapid input starts from current visual positions; cancel old
animations and remove inert copies. Keep keyboard focus on the initiating control.
These animations apply to explicit demo actions, not unsolicited live reordering.

A single lazy-loaded WebGPU canvas draws a shared light field behind summary and
work-queue cards. Card geometry and readiness colors drive soft perimeter halos;
hover, keyboard focus and explicit changes produce a bounded directional settling
effect. No perpetual pulsing or simulated progress. The HTML label/icon remains
authoritative, and the halo describes readiness only, not reference/publication
health. CSS state edges and halos remain available when WebGPU is absent or lost.
HTML-in-Canvas capture is not needed for lighting or grid reflow; reserve it for
future effects that manipulate actual card imagery and the existing Focus effect.

The renderer follows card movement, resize, theme and scroll; it stops when idle,
offscreen or hidden, caps resolution, and releases its device/buffers on unmount.
Reduced motion uses static CSS lighting and immediate grid updates. Suspend Home
lighting and finish grid choreography before the shell Focus transition; recalculate
geometry afterward. Storybook includes interactive filter/sort choreography,
state lighting, CSS fallback and reduced-motion specimens. Verify rapid changes,
resolution/reset, inspector layouts, focus preservation and device-loss fallback.
Overview does not include the decorative brand/workflow footer.

## 8. HTML-in-Canvas and WebGPU requirements

HTML-in-Canvas renders actual HTML within a canvas scene; WebGPU supplies the
rendering/effects pipeline. They are separate capabilities. Neither is required
for a plain colored halo or a useful animated admin.

| Experience | HTML-in-Canvas opportunity | WebGPU opportunity | Standard experience |
| --- | --- | --- | --- |
| Resource grid | Real card content, badges and controls inside a composited scene | Shared light field, perimeter effects and limited depth | HTML cards, CSS halos and transitions |
| Spatial taxonomy / resource explorer | Semantic node cards and labels within a zoomable workspace | Lit paths, relationship emphasis and spatial transitions | Accessible tree/table with expand and inspect |
| Product Focus Mode | Preview/relationship cards accompanying the authoring surface | Background depth and controlled scene transitions | HTML editor and View Transitions where supported |
| Revision comparison | Rich before/after and timeline cards | Change propagation along affected relationships | Semantic diff and chronological list |
| Media studio | Interactive annotations anchored to media | Detail lens and derivative comparison effects | Image viewer, controls and annotation list |

### Rendering and interaction contract

- Build semantic components first and preserve one authoritative data/interaction
  model across renderers. Do not maintain two independently editable copies.
- Capability-detect HTML-in-Canvas, WebGPU adapter/device availability and required
  features independently. No browser-name assumptions. Device loss or initialization
  failure returns to the standard experience without losing drafts or selection.
- Keep global navigation, command palette, destructive confirmations, credentials,
  long-form editors and transactional forms in ordinary HTML. Canvas enhancement
  is scoped to selected workspaces, not the entire application shell.
- Maintain element geometry and hit-test order throughout transforms. Shader
  effects must not move a control's apparent clickable location away from its
  actual bounds. Avoid nonlinear distortion of interactive text and controls.
- Preserve focus order, labels, text selection, copy/paste, browser search,
  zoom, high-DPI text and screen-reader semantics. Test these behaviors rather
  than assuming DOM-backed drawing guarantees correct accessibility.
- Relationship exploration fetches bounded neighborhoods incrementally. Initial
  proposal: up to 50 cards per page/expansion and at most 200 expanded nodes in
  one scene; show an explicit load-more or collapse prompt at the boundary.
  These are tunable product limits requiring capacity validation.
- Card state changes update in place. Re-sorting a live list must not move the
  focused item under the pointer or keyboard; offer an explicit refresh/reorder.
- No cross-origin media or canvas export behavior is assumed. Validate texture
  permissions and origins. Exports include only authorized data and require a
  user action; exports never include secret or hidden inspector content.
- Lazy-load scene rendering. No per-card GPU contexts. Cap resolution and effects
  quality on constrained devices; provide an effects-off preference.

Chrome documents HTML-in-Canvas in Chrome 157. This is not a cross-browser support
guarantee. Before implementation, verify the current API and supported browser
matrix using the [overview](https://developer.chrome.com/blog/html-in-canvas?hl=en),
[technical concepts](https://developer.chrome.com/docs/web-platform/html-in-canvas/concepts?hl=en)
and [WebGPU documentation](https://developer.mozilla.org/en-US/docs/Web/API/WebGPU_API).
Choose rendering libraries in a technical spike; this PRD does not mandate Three.js
or a custom shader engine.

## 9. Reliability, authorization, and truthful system behavior

- Every data fetch and command is server-authorized. Hidden navigation is not
  enforcement. Permission changes invalidate affected cached data and open views.
- Preserve expected resource versions on edits. On conflict, retain the user's
  draft, fetch current state and offer a comparison/reapply flow; never silently
  overwrite another editor or a controller update.
- Live views bootstrap current state and resume supported watches correctly.
  Reconnect backoff is bounded; expired cursors trigger a fresh bounded bootstrap.
  Display freshness and connection state. Do not treat watches as complete audit
  histories or subscribe separately for every card.
- GraphQL aggregates come from asynchronously materialized `status.resolved`.
  Missing or stale aggregates show unavailable/stale, never fabricated zeroes or
  live resolver scans. Counts used for deletion explain their scope/freshness.
- API/controller concurrency and rolling upgrades remain supported. Capability
  changes or mixed-version responses degrade individual features explicitly.
- Git service remains stateful and singleton-only: exactly one active process
  per deployment. No Git replica map, quorum animation, consensus badge, peer
  mesh, zero-downtime replacement claim, or distributed storage parity claim.
- Remove unsupported mockup details such as RocksDB/libgit2 storage claims,
  automatic GPG verification, regional Git nodes and provider-specific admission
  assertions. Display actual configured providers and returned evidence.
- Connection freshness is not synchronization proof; a short request duration
  does not establish catalog consistency. Reconciliation percentage is shown
  only when the backend supplies a meaningful numerator and denominator.
- Error messages preserve machine-readable diagnostics and offer human-readable
  next steps. Do not expose secret material, internal paths or unrelated tenant data.

## 10. Delivery sequence and dependencies

| Stage | Deliverable | Exit gate |
| --- | --- | --- |
| A — Product prototype | Full navigation, concept-v2 shell, key journeys, both themes, planned-domain labels | Navigation and brand review; demonstrable merchant and operator flows |
| B — Working foundation | Provider-aware login, namespace context, supported product/category edits, resource lists/inspectors, provenance, conflicts and freshness | Real API integration; no demo data in operational views; permission and watch recovery verification |
| C — Signature interactions | State halos, resource grid, card-to-inspector continuity, Focus Mode; HTML-in-Canvas/WebGPU enhancement spike | Keyboard/accessibility and renderer-fallback parity; measurable motion quality on agreed hardware |
| D — Catalog breadth | Variant/collection authoring, scalable relationship queries, media library, bulk work, spatial taxonomy | Supported write paths and bounded backend queries; real admission diagnostics |
| E — Review and publication | Durable drafts/review, revision history, releases, target publication and validated recovery | Implemented release/approval services; publication separate from source readiness |
| F — Commerce operations | Orders, checkout exceptions, customers, payments, fulfillment, returns, operational inventory, pricing and markets | Domain-specific backend/provider contracts, privacy, monetary correctness and operation recovery |
| G — Ecosystem and insight | Apps, automation, content breadth, reporting, B2B and advanced capabilities | Extension isolation, job/delivery evidence and real reporting sources |

Stages describe dependencies, not fixed dates or a strictly serial staffing plan.
Commerce services can progress alongside catalog work once their contracts are
defined. Avoid delaying the full navigation prototype until every backend exists.

## 11. Acceptance criteria and success measures

The following are proposed launch gates, to be validated with representative
merchants and operators before production rollout.

| Area | Acceptance |
| --- | --- |
| Navigation | Every planned domain has an index/detail destination in the prototype; every enabled production destination has a functioning authorized route, reload and Back behavior |
| Merchant usability | At least 80% of representative test participants complete find/edit product, diagnose blocked resource, and locate order/return flows without assistance; planned commerce tested in labeled prototype |
| Catalog correctness | Products and variants are distinct; unsupported writes are unavailable; admission, readiness and publication are never conflated |
| Concurrent edits | Two editors changing the same resource see a recoverable version conflict; both drafts survive until explicitly resolved or discarded |
| State lighting | Every halo has a matching label/icon; connection loss and stale observed generations cannot remain falsely green |
| Relationship work | Expanding a node loads only a bounded neighborhood; switching namespace removes prior scope's nodes and subscriptions |
| Accessibility | Core journeys meet WCAG 2.2 AA; keyboard, screen reader, 200% zoom, reduced motion, forced colors and touch checks pass in the agreed browser matrix |
| Rendering fallback | Unsupported APIs, denied GPU access, device loss and effects-off preserve navigation, drafts, selection and task completion |
| Motion quality | On an agreed reference laptop, representative 50-card grid and 200-node scene target 60fps during interaction; proposed p95 input-to-feedback under 100ms, measured separately from network completion |
| Loading | Proposed p75 LCP at most 2.5s and INP at most 200ms on the agreed field population; GPU bundles do not block shell initialization |
| Reliability | Watch expiry, reconnect, forbidden responses, partial bulk failure and mixed-version capability failures show actionable states without duplicate actions |
| Truthfulness | No fake GMV, Git replication, quorum, progress percentage, cryptographic proof or unsupported provider capability appears in production |
| Business outcomes | Establish baselines for edit completion time, review turnaround, diagnosis time and failed/retried operations before selecting improvement targets |

Instrument navigation success, task duration, admission errors, conflict recovery,
renderer fallback and scene quality without recording secrets, customer payloads,
or full catalog bodies. Compare effects-on/off task outcomes: visual delight must
not increase operator mistakes.

## 12. Design deliverables and decisions still needed

Create a route-linked prototype covering onboarding, Home, all domain indexes,
product/variant/category/collection details, resource grid states, spatial taxonomy,
review/compare, publication, an order/return flow, and platform identity/resources.
Include loading, empty, forbidden, unsupported, stale, failed, conflict and
terminating states. Deliver light/dark, desktop/tablet/mobile, reduced-motion
storyboards, semantic tokens and reusable shell/card/inspector specifications.

Resolve during technical planning: merchant/store-to-namespace mapping; the
supported browser/hardware matrix; identity/capability discovery; durable draft
and approval contracts; history/provenance read surfaces; channel/market/target
relationships; commercial providers; searchable media and global lookup; extension
trust model; and the persistence policy for personal/shared views.

Do not resolve these gaps by making the UI appear operational. The intended
experience is ambitious; each enabled action must still represent a real contract.


### Home empty states and compact screens

Home uses one shared empty-state anatomy: neutral Split / Return illustration,
heading, concise explanation, and optional relevant action. An empty catalog says
“Your catalog starts here”; an empty personal workspace says “No work yet”; empty
history says “No activity yet”. These differ from empty tabs and filtered results.
Hide search/sort/filters when the whole store has no corresponding data. Retain
work tabs for navigation. Overview omits zero-only summary cards, and zero counts
elsewhere receive no status halos. Existing setup/catalog links still lead to
placeholders; no creation flows are introduced.

At mobile widths, omit the repeated store/Home trail and subtitle, use a compact
heading, and keep demo identification visible. The Demo data disclosure contains
reset behavior, snapshot information where relevant, and Reset demo. Its reset
action closes the disclosure before opening the existing confirmation. Overview
uses compact two-column summaries; My work uses horizontally scrollable counted
tabs. Recent activity shows search plus one Filters button and compact timeline
entries. The filter sheet stages changes until Apply filters, restores focus on
close, and discards pending edits on Cancel/Escape. Applied filters have removable
chips; no-results states retain controls and Clear filters. At 375 × 667, the first
activity entry or the first-use guidance is visible above bottom navigation.

Storybook interactions use the navigation visible at the actual canvas width,
including rail menus and mobile drawers. Global command navigation can leave a
modal inspector without discarding its draft. Native disclosure state specimens
render explicitly expanded and assert visible content; real browser tests verify
Enter/Space toggling. Narrow manager-canvas checks complement static build checks.

## Implemented Product catalog demo increment

**Scope:** Catalog / Products is the fourth mock destination. The 79-destination registry now retains 74 placeholders; Home / Setup guide is the fifth mock destination. This is a local demo, not shipped backend authoring.

**Visual reference:** [Product Canvas (Focus Mode)](https://stitch.google.com/projects/13975495090411370806?node-id=42860fe6eac54d098d5041326457bbfb), in the original GitStore Admin project, supplements the latest GitStore Admin II designs. Reuse its narrow document composition, inline Title, restrained writing controls and lower media/variant sections with the current Split / Return identity. Do not copy its invented publication or telemetry controls.

**Merchant flows:** Product index with grid/table views, Title search, readiness/lifecycle/category filters, sorting in the URL and cursor-based Load more; Product detail; create/update with Title (1–200 characters), Markdown body, category, tags, existing ordered media, option definitions and Active/Retired lifecycle. Technical identity and `.md` path remain stable on Title edits and live in Technical details. Products have no SKU, price or inventory fields. Variants are read-only.

Create/save records a demo event and enters Processing without implying publication/readiness. Personal drafts/reviews remain independent. Retirement keeps the authored Product; it does not withdraw a published release. Deletion is blocked by dependent ProductVariant owners. An unblocked confirmed request enters Deleting, disables edits and generic ready actions, and requires explicit demo completion with a fresh dependency check. Final removal keeps history/work/drafts; recovery cannot resurrect a missing Product. Missing related items display unavailable.

**Shared session:** Products and Home share per-store in-memory catalog/history. Buffers survive page and store changes, Focus transitions and responsive layouts. Reload resets both stores; a confirmed reset restores only the active store. Dirty Cancel/discard and stale-editor reload confirm before throwing away Product text. Optimistic base comparison blocks stale saves after recovery or another catalog edit, preserving the buffer. Recovery snapshots cover all authored Product fields and Markdown body.

**ADR-0013:** The backend decision is Proposed, and the current GraphQL Product body is `String`. Markdown in top-level `body` remains authority. Milkdown Crepe offers BlockNote-style writing (slash blocks, formatting tools, headings/lists/quotes/code/links/images/tables/tasks) with Write / Markdown / Preview modes. Opening and switching modes or editing Title must preserve original body bytes. Compare source and rich serialization semantically before accepting rich edits; unsupported content stays source-editable. Preview never executes HTML or unsafe URLs. Product frontmatter fields and gallery references remain outside the body; body images remain ordinary Markdown.

Do not save editor JSON, an invented IR vocabulary, interpolation tokens or generated HTML. The editor adapter can later consume a versioned read-only `bodyDocument`, once the backend contract exists. Parsing/admission/publication resolution remains server-owned future work. No API, Apollo, authentication, local image uploads, collaboration, AI, persistence or backend write is included.

Product list controls follow Recent activity: visible inline desktop filters, an aligned tablet row, staged mobile filter sheet and removable applied chips. Demo data remains at the far right of the page heading/toolbar, independent of CRUD actions; context trails are hidden on mobile. Product tags are committed as removable chips on Enter/comma/paste or blur. Products uses **Load more**, driven by endCursor/hasNextPage, without numbered pages or total-page claims; filters reset loaded batches.

Recent activity displays only the number of loaded activities (or loaded matching activities), with Load more for additional entries. Neither the results label, load announcement, nor mobile Apply filters action promises a connection total. Local fixtures still determine whether another demo batch exists; a backend integration must use connection pageInfo/endCursor. ADR-0017 and issues #502/#503 do not supply an exact activity total; operational Events may aggregate occurrences and expire, and are distinct from the merchant change/review timeline.

### Product browsing continuity (implemented with local demo data)

Selecting a Product card or table row expands its surface into the detail page over 300ms. The Title and leading image have separate shared snapshots so row columns and text are not stretched into the detail layout. Destination content fades in as the surface settles; shell chrome remains anchored. Back reverses the transition. The Products return link restores the originating filters/view, loaded cursor batches, scroll and selected-item keyboard focus. This bounded, per-router in-memory navigation state is not demo-data persistence and is invalidated by store reset; reload starts normally.

Only same-store list/detail journeys participate. Create/edit, unrelated destinations, missing Products, direct entry, reduced motion, and unsupported snapshot/type APIs navigate normally. Focus motion interrupts Product snapshots; grid choreography and shared lighting pause during the transition. No HTML-in-Canvas capture or new WebGPU layer is introduced.

Product transition refinement: preserve uninterrupted coverage for the surface, Title and image in both directions. Use complementary crossfades rather than fading the outgoing content to zero before revealing its replacement. Separate supporting content from the moving background so the full detail page is never compressed into a card. Avoid hover-driven position changes at the snapshot-to-DOM handoff.

### Home / Setup guide: implemented local demo

Visual reference: [P04 Home — Setup guide, GitStore Admin II](https://stitch.withgoogle.com/projects/12282348560453912861?node-id=c059af7b8a8f499cb64fe55b826c1886). Retain the vertical checklist/progress composition, adapting it for merchants. Deployment, credentials, telemetry, policy activation, and storefront syncing in the mockup are not implemented claims.

Four ordered steps are independently available: Review store details, Review categories, Add your first product, and Review catalog checks. Three steps record confirmed guidance reviews only; reopening a review reverses that flag. The Product step derives completion from saved non-deleting Products, including retired or processing Products. Buffers do not count. Deleting the last eligible Product reopens the step. Atelier Kyoto starts at 3/4; Studio Berlin starts at 0/4. Overview uses the same definitions and selector, with task links into Setup guide.

Progress lives in the shared per-store memory session, surviving routes/store switches and Focus Mode until reload or confirmed store reset. Reviews do not enter catalog history or My work and do not change resources, settings, categories, readiness, or publication. Completed copy says Checklist complete, not ready for commerce. Repository and future storefront topics remain informational outside progress.

Optional `step` search values are `store-details`, `categories`, `first-product`, and `catalog-checks`; valid values open guidance in the shell inspector and invalid values are ignored. Closing removes the parameter with replacement navigation. Settings/Category links retain canonical placeholder destinations with Page not implemented disclosure; Product creation uses its existing route. Loading, unavailable, and read-only stories are presentation specimens, not network/authentication contracts. Five destinations are mock, 74 remain placeholders, and all 79 remain reachable.
