# Admin foundation

## Boundaries

The shell, shared controls, layouts, navigation, Home / Overview, and Home / My work are implemented. Overview simulates resource resolution; My work provides limited product text editing and personal draft/review demo actions. The other 77 destinations remain placeholders. Local typed fixtures describe two demo workspaces and one demo identity. No network transport, authentication, live operational metrics, Apollo cache, GraphQL endpoint or API mock server is present. Demo menus do not enforce authorization.

The PRD covers future commerce capabilities. Source references in the copied PRD describe the backend's historical planning baseline, which is intentionally absent on this branch. Stitch project GitStore Admin II is visual reference, not production source code.

## Structure

- `src/navigation`: canonical 13-group / 79-destination metadata, stable destination IDs, scope and URL helpers.
- `src/routes`: thin TanStack file routes. Generated route-tree output is ignored and recreated by the router CLI/Vite plugin.
- `src/components`: React Aria-backed interactions, navigation items, theme picker, workspace selector, profile menu and command palette.
- `src/shell`: header/sidebar and standard, inspector and Focus Mode composition.
- `src/features/home/overview`: page-local fixtures, selectors, reducer, controls, resource inspector, and presentation states. No transport or API contract is implied.
- `src/features/home/my-work`: independent page-local work fixtures, selectors, reducer, product text editor, change preview, confirmations, and presentation states.
- `src/features/home/shared`: common Home page frame, context trail/header, demo controls/notice, summary content, and resource cards. Overview and My work share their responsive spacing and card anatomy.
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

## Overview demo lifecycle

Atelier Kyoto starts with 14 resources across seven canonical kinds; Studio Berlin starts with an empty catalog and setup guidance. Counts and the health matrix derive from resource records. Confirmed demo actions update local readiness and activity; cancellation does not. Ready items remain accessible through the inspector selector and activity. State resets on route unmount, namespace change, reload, or Reset demo. Storybook scenarios are injected through router context, never URL parameters. Restricted and unavailable specimens are presentations, not permission enforcement or network behavior.

Feature content registers with the shell-owned page inspector by route owner. Stable registration/open/close functions preserve the shell layout; owner checks and cleanup prevent resource details leaking across routes. The navigation registry marks only `home.overview` as `mock` and all other destinations as `placeholder`.

### Merchant vocabulary

Overview's `presentation.ts` maps canonical kinds and readiness values to merchant
labels. These are display labels only: selectors, sorting, filters, shader colors,
routes, and fixtures retain canonical keys. Primary sections read Updates and
issues, Store status, and Store setup. Counts include store content and configuration,
not only products. Canonical types and `.md` source paths remain available in the
inspector's native Technical details disclosure; they do not appear on work cards.
Keep future business-page copy aligned with the PRD's merchant language policy.

## My work demo lifecycle

`/w/$namespace/home/my-work` has independent page-local state. Atelier Kyoto starts
with 2 drafts, 2 submissions, 2 assigned reviews, and 1 completed review; Studio
Berlin is empty. Tabs/search/sort/editor state are not stored in URLs or browser
storage. Leaving the route, switching namespaces, reloading, or Reset demo restores
the fixtures. Timestamps and local revisions are deterministic; there is no timer.

The reducer validates draft-only saves/submissions/discards and assigned-only
reviews. Product names must be nonblank; descriptions may be empty. Base values
remain fixed for Before/After comparisons. Unsaved editor text is separate from
saved draft state; submission stays disabled until it is saved or cancelled.
Review notes are mandatory for requesting changes. Completed outcomes never
publish products and do not synchronize into Overview.

The shell's owner-scoped `registerCloseGuard` lets My work confirm closing an
inspector with unsaved edits. Other pages retain unguarded closing. The same guard
covers the close button, view-control toggle, and tablet/mobile dismissal. Focus
Mode can hide and restore the editor without abandoning its state. Responsive
layouts mount only one inspector, avoiding duplicate field IDs and focus targets.
Selection/tab/reset changes use the page's matching confirmation boundary. Route
exit follows the explicit reset notice rather than blocking navigation.

My work stories include all tabs, empty/no-results, read-only/loading/unavailable,
change preview, editing/unsaved, confirmations, themes, tablet/mobile, and reduced
motion. Read-only and unavailable are presentation specimens, not authorization
or network behavior. Shell navigation metadata marks exactly two mock pages and
77 placeholders. No API contracts or external transport are introduced.

### Shared Home presentation and overlay layers

Both implemented Home pages use `HomePage`, `HomePageHeader`/`PageContext`,
`DemoControls`, and `DemoNotice`. The frame preserves Overview's width and
responsive padding; page-specific styles must not override these. The notice has
no redundant “Demo workspace” prefix. `SummaryCardContent` is used in Overview's
articles and My work's keyboard-operated tabs; counts, descriptions, state edges,
hover/focus lighting and ratio bars use the same geometry. `ResourceCard` owns the
kind label, title, body spacing, divider, and View details control. Work timestamps
are optional footer metadata. Work status and readiness labels remain distinct. Both inspectors use the shared
`TechnicalDetails` native disclosure with Resource type, Source file, the file
icon, and a wrapping `.md` path. Labels, typography, spacing, and keyboard focus
styles belong to that component rather than individual page styles.

`ConfirmationDialog` is shared across both pages. Root CSS layer tokens order
inspectors/drawers (1000), modal confirmations (1200), and popovers (1300). Dialog
content must be above an active inspector, not merely present in the accessibility
tree. Browser regressions check actual pointer hit testing, keyboard containment,
focus restoration and confirmation at tablet and mobile sizes. The shared select
trigger has a stable 160px width and fixed height; selected labels truncate rather
than shifting the control or toolbar.

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
