# Admin foundation

## Boundaries

The shell, shared controls, layouts, navigation, Catalog / Products, and Home / Overview, My work, and Recent activity are implemented. They use deterministic fixtures and shared per-store in-memory sessions; the other 75 destinations remain placeholders. No network transport, authentication, live operational metrics, Apollo cache, GraphQL endpoint, or API mock server is present. Presentation specimens do not enforce authorization.

The PRD covers future commerce capabilities. Source references in the copied PRD describe the backend's historical planning baseline, which is intentionally absent on this branch. Stitch project GitStore Admin II is visual reference, not production source code.

## Structure

- `src/navigation`: canonical 13-group / 79-destination metadata, stable destination IDs, scope and URL helpers.
- `src/routes`: thin TanStack file routes. Generated route-tree output is ignored and recreated by the router CLI/Vite plugin.
- `src/components`: React Aria-backed interactions, navigation items, theme picker, workspace selector, profile menu and command palette.
- `src/shell`: header/sidebar and standard, inspector and Focus Mode composition.
- `src/features/home/overview`: seed fixtures, selectors, controls, resource inspector, and presentation states. No transport or API contract is implied.
- `src/features/demo`: shared session, snapshots, recovery validation, event history, and change previews.
- `src/features/home/recent-activity`: timeline, filters, local pagination, and event/recovery inspector.
- `src/features/home/my-work`: personal-work seed fixtures, selectors/reducer, product text editor, change preview, confirmations, and presentation states.
- `src/features/home/shared`: common Home page frame, context trail/header, demo disclosure, summary content, and resource cards. Overview and My work share their responsive spacing and card anatomy.
- `src/features/home/shared/motion`: shared card choreography and decorative GPU lighting.
- `src/pages`: shared placeholder and not-found UI. Future business screens belong in feature directories.
- `src/state`: browser preferences only; never future API data or credentials.
- `src/stories`: shared control and layout states, including themes, rail, reduced motion and responsive previews.

The root Makefile is canonical. `make dev` defaults to the app; select `TARGET=storybook` for component development. `make check` validates types, lint, unit tests and both production builds. `make pr-ready` additionally exercises Chromium plus Firefox/WebKit compatibility smoke tests and Storybook.

CI runs those same checks as separate validation/build and browser-test steps so their progress is visible. The pinned pnpm version must include the 12.7.0 fix for orphaned non-interactive scripts during Playwright teardown (pnpm/pnpm#15555); 12.6.0 can hang after all tests have passed. Both preview servers receive SIGTERM with a five-second shutdown window, browser runs have a three-minute CI deadline, and workflow steps and the job have outer timeouts.

## Routing

Namespace pages use `/w/$namespace/<group>/<destination>`; platform pages use `/platform/<destination>`. Lowercase labels are normalized to hyphenated slugs. Group indexes redirect to their first destination. Root defaults to Atelier Kyoto / Home / Overview. Namespace changes preserve the active scoped destination. Platform routes remain platform scoped and do not imply namespace-scoped ServiceAccounts. Unknown demo namespaces explain the local-fixture limitation instead of manufacturing another tenant.

## Navigation contexts

The workspace landing sidebar exposes five areas: Home, Commerce, Content, Operations and Insights. `src/navigation/areas.ts` maps these presentation contexts onto the existing route registry without changing URLs. Commerce groups Orders, Catalog, Inventory, Customers and Pricing; Operations groups Channels, Changes and Apps. The sidebar shows only the selected area's domains, with an area switcher and a Back to workspace link. Single-domain contexts show their page links directly. Context follows the URL, including direct links, reload and browser history.

Settings is a separate context entered through the anchored footer link; its sidebar contains only settings destinations and no duplicate Settings entry. Control plane is a platform workspace chosen from the namespace/platform selector. Choosing a namespace from platform scope opens that workspace's Home; switching namespaces within a workspace preserves the current page. The command palette continues to search all 79 destinations. These are navigation contexts, not permission checks or personalized filtering.

## Shared Home demo lifecycle

`HomeDemoProvider` sits under the root preferences provider. It owns a session per
namespace: authored snapshots, readiness metadata, saved work, unsaved draft buffers,
immutable events, deterministic counters, and a reset epoch. All application routes
and store switches preserve these sessions. Reload restores both stores. Reset demo
requires confirmation, restores only the active store, clears its buffers and all
Products and the three Home views, and remounts its shell to dismiss stale inspectors and selections.
Preferences remain separate and may persist normally; demo data never uses storage.

File-backed Product, ProductVariant, CategoryTaxonomy, and Collection display titles
come from `spec.title`. Use Title in editing, search, sort, and field comparisons;
Products have no SKU. File names and namespace/user display names remain distinct.
Canonical kinds and readiness values stay separate from merchant presentation labels.

Overview derives current labels/references from authored snapshots and summary counts
from readiness metadata. Confirmed resolution changes readiness and records processing
activity without changing authored data. Its recent activity uses the shared event feed.
My work draft saves, submissions, discards, and reviews record explicit work events;
typing and cancellation do not. Submission/approval do not apply draft text to the catalog.
Draft buffers survive navigation and store switching and are restored when reopening a
draft. Explicitly abandoning edits uses the existing confirmation guard. Recovery never
rewrites a draft base/proposal; a stale-base notice explains differences from the current
item. Completed recovery is a discriminated read-only work type for all four kinds.

Recent activity is a date-grouped local timeline: 24 baseline events, 10 initially
visible, 10 more per Load more. Search titles/file names and filter actor, canonical
kind, category, and UTC date. Counts describe the full filtered history; filters reset
pagination. Entries are immutable and preserve historical titles/outcomes. New actions
use timestamps after the fixed 2 Nov 2026 12:00 UTC anchor and stable per-store IDs.
There are no background timers, polling, downloads, or complete-audit-log claims.

Recovery targets an authored event's After snapshot, compares it against the current
snapshot, and requires confirmation. The reducer rechecks no-op/missing versions,
reference kinds/existence, and category cycles, then atomically replaces authored data,
appends a demo recovery event, and records Completed work. Identity, paths, readiness,
publication state, and media bytes are outside recovery. Product/Category/Collection
snapshots use `spec.title`; File snapshots contain display-name/alt-text/caption metadata.
These are UI-only fixture schemas, not supported API write contracts. Other event kinds
are inspectable but cannot initiate recovery. View recovery event explicitly clears
filters; confirming recovery otherwise preserves the user's query and selection.

The shell's owner-scoped inspector close guard confirms abandoning unsaved edits.
Focus can hide/reopen an editor without discarding its buffer. Responsive layouts
mount one inspector. Page-owned filters and selection reset on route exit; session
data does not. Each Storybook router gets an isolated provider with optional fixture
seeds through router context, preserving existing presentation scenarios.

## Shared Home presentation and overlay layers

Home uses shared frame/header/context, demo disclosure, summary-card content,
resource-card anatomy, status pills, change previews, and Technical details disclosures.
The technical disclosure consistently shows Resource type and Source file with the
file icon and wrapping `.md` path. Work status, event outcome, and readiness remain
separate concepts even when they share decorative colors. Header titles also update
browser tabs to `<current page> · GitStore` from navigation metadata.

Root layer tokens order drawers (1000), confirmations (1200), and popovers (1300).
Tests check pointer hit testing as well as keyboard focus on mobile/tablet. Select
triggers use a fixed 160px width and fixed height; labels truncate without toolbar shifts.

## Styling and motion

Sidebar Up/Down and Home/End keys move focus among visible navigation links and section controls. They do not change the route; Enter activates the focused link. Tab retains its native behavior, and React Aria popover menus retain their own keyboard handling. The mobile profile menu opens above its footer trigger, aligned to its width and constrained to the viewport; Escape closes the menu and restores focus without dismissing the navigation drawer. Within that drawer, the profile popover is nonmodal: the drawer owns modality and focus containment, avoiding competing nested modal restoration in Chromium.

Mobile uses a fixed bottom navigation bar with Catalog, Orders, Changes and Menu. Platform routes replace the shortcuts with Namespaces, Resources and Policies. Menu opens a bottom drawer; focus returns to its trigger on dismissal. Safe-area padding keeps content clear of the navigation. The mobile header contains the compact workspace context selector; desktop breadcrumbs start at the domain, without a redundant Workspace/Platform prefix. Expanded context menus open below their trigger within sidebar width. Selected indicators derive from React Aria selection state. The profile menu includes a disabled Log out item with an authentication-not-connected explanation; there is no simulated sign-out or session API.

Concept-v2 assets are copied from the branding repository. Production SVG symbols remain flat; no generated Stitch logos are reused. Archivo Narrow, IBM Plex Sans and IBM Plex Mono are bundled through Fontsource; license copies are included under public/licenses. Palette, layout and motion use custom CSS Modules and shared CSS variables, not a preset theme. Light/dark/system and sidebar choices persist locally. Motion can follow the system or be reduced; system reduction is never overridden. Focus Mode uses a reversible 420 ms View Transition, with a Web Animations fallback. Its optional, lazy-loaded WebGPU enhancement captures decorative copies of shell regions through native HTML-in-Canvas; working controls and page content stay in the DOM. Reduced motion bypasses animation and GPU preparation. See the root README for capability detection and resource budgets. The motion boundary lives in `src/shell/motion`; its document-level pseudo-element stylesheet is scoped to `html[data-focus-transition]`, while layout and control styles remain CSS Modules.

### Home card choreography and lighting

The Home-level `shared/motion/` directory owns measured grid movement and a demand-driven
WebGPU light field. Filter/sort/reset/confirmed resolution capture card positions
before changing state. Stable HTML cards move for 300 ms; inert, aria-hidden exits
last 140 ms; arrivals settle for 220 ms with at most 96 ms stagger. The stage height
animates with the grid. Interruptions start from the current visible positions;
copies and animations are removed on completion, reduction, Focus, or unmount.

One lazy-loaded canvas lights summary and queue card boundaries without capturing
HTML. Its renderer caps dimensions to 2048 pixels and DPR to 1.5, draws only visible
cards, and stops requesting frames after a bounded interaction or layout change.
Hidden/offscreen scenes clear; route exit and reduced motion destroy the device.
Device loss/initialization failure returns to CSS without changing page state.
Readiness labels/icons remain authoritative. Theme/resize/scroll trigger redraws.
The shell's `gitstore:focus-motion` boundary suspends this light field and finishes
grid motion before Focus capture, resuming geometry updates after transition.
HTML-in-Canvas remains exclusive to the existing optional shell capture path.

Storybook's State Lighting, CSS State Lighting, Filter Choreography, and Reduced
Motion Filtering specimens expose these behaviors; effects can be compared without
changing browser flags. The decorative brand/workflow footer is removed from
Overview. No backend, polling, persistence, or live state changes are introduced.

## Static deployment

Deploy the built app's `dist` directory with a fallback to `index.html` for application paths (excluding static assets). Deep-link tests use Vite's preview fallback. No hosting or deployment resources are created in this increment.

## Selective reintegration

Do not merge the Admin-only deletion diff wholesale. Import `gitstore-admin/` and adapt selected frontend commands/CI to the target branch. Retain that branch's backend root instructions, Makefile commands, workflows and services. Review the imported directory in a clean target worktree and run both frontend and target-branch acceptance suites before integration.

If a Storybook development server is already running on port 6006, use
`STORYBOOK_PORT=6007 make pr-ready` to validate a separate static build without
interrupting it. The default port remains 6006.


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

## Product catalog and Markdown boundary

- `src/features/catalog/products`: list selectors/URL state, CRUD reducer, document editor, media ordering, lifecycle/dependency actions, and route pages. Nested routes belong to the existing Products destination; four mock destinations and 75 placeholders remain.
- `src/features/demo`: shared session provider, authored snapshots, events, Product buffers, draft buffers, revision counters and owner fixtures. Local category/media IDs are demo lookup keys, not GraphQL input envelopes; introduce an explicit contract adapter with real API integration.
- `src/components/markdown`: lazy Milkdown Crepe adapter, CommonMark/GFM semantic comparison, explicit source fallback and safe React Markdown preview. Rich AST is disposable. No image uploads, math, AI, collaboration or interpolation. Frontmatter fields are edited outside body.

Product buffers retain a base snapshot; saves check it against the current saved Product, including after recovery. Reload saved Product confirms discarding only that Product buffer. Source bytes remain unchanged until a body edit. Before opening rich mode, parse/serialize content is compared semantically; HTML and non-preserving content stays source-editable. Preview omits HTML and strips unsafe URLs. Markdown formatting after a rich body edit may normalize the edited body.

Product metadata identity remains stable when Title changes. IDs are unique even against deleted history. Save records authored state and Processing status; retirement is authored lifecycle, not deletion or publication. Foreground deletion uses explicit variant-owner fixtures, blocks before start, checks again at explicit completion, and disables generic Overview resolution while terminating. Missing reference labels degrade to unavailable. Recovery cannot resurrect a deleted Product or change a deleting Product. Existing personal work and buffers remain intact.

The Product gallery bundles the two ceramic basin photographs from the supplied Stitch screen under `public/media/demo`, with source attribution there. Other Products show an honest missing-image state. Variants stay read-only. Do not add browser-storage persistence, backend mutations or Apollo in this increment.

Run `make check TARGET=all` and `make pr-ready`. Product browser tests cover CRUD, source preservation, rich keyboard formatting, confirmations, URL filter state, Load more, Home integration and narrow Storybook play functions. Use `STORYBOOK_PORT=6007` for static checks while the developer's live Storybook remains on 6006; repeat Product story checks against 6006 to verify live interaction behavior.

### Product presentation consistency

Products uses the shared Home page heading, with Demo data anchored at the right. Product view/create/edit keep the same right margin for Demo data and place CRUD actions in a separate row. Context trails are hidden below 760px. `FilterBar` and `FilterSheet` are shared with Recent activity: inline desktop filters, an aligned tablet row, and staged mobile selection with apply/cancel and applied chips. Product tags use a reusable React Aria tag input: Enter/comma/paste commit unique chips, blur commits pending text before Save, and remove buttons or keyboard deletion remove chips.

Product pagination follows the connection boundary: opaque demo edge cursors and pageInfo drive **Load more products**, appending batches without displaying page numbers or total pages. Filters/sort reset loaded batches; reload starts with the first batch. Product counts say how many items are shown, not a server-guaranteed catalog total. The local fixture can preview match counts in the filter sheet; that is not a promise of a future GraphQL totalCount field.

Recent activity displays only the number of loaded activities (or loaded matching activities), with Load more for additional entries. Neither the results label, load announcement, nor mobile Apply filters action promises a connection total. Local fixtures still determine whether another demo batch exists; a backend integration must use connection pageInfo/endCursor. ADR-0017 and issues #502/#503 do not supply an exact activity total; operational Events may aggregate occurrences and expire, and are distinct from the merchant change/review timeline.

Browser CI runs two Playwright shards, each with the existing three-minute suite limit. Run a shard with `make test TARGET=e2e E2E_ARGS="--shard=1/2"`. Keyboard tests use `ControlOrMeta` for platform-dependent editing shortcuts so macOS development and Linux CI exercise the editor's native modifier. Artifact names include the shard number.
