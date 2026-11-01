# Admin foundation

## Boundaries

Only the shell, shared controls, layouts and navigation are implemented. Local typed fixtures describe two demo workspaces and one demo identity. No network transport, authentication, operational metrics, Apollo cache, GraphQL endpoint or API mock server is present. Demo menus do not enforce authorization.

The PRD covers future commerce capabilities. Source references in the copied PRD describe the backend's historical planning baseline, which is intentionally absent on this branch. Stitch project GitStore Admin II is visual reference, not production source code.

## Structure

- `src/navigation`: canonical 13-group / 79-destination metadata, stable destination IDs, scope and URL helpers.
- `src/routes`: thin TanStack file routes. Generated route-tree output is ignored and recreated by the router CLI/Vite plugin.
- `src/components`: React Aria-backed interactions, navigation items, theme picker, workspace selector, profile menu and command palette.
- `src/shell`: header/sidebar and standard, inspector and Focus Mode composition.
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

## Styling and motion

Sidebar Up/Down and Home/End keys move focus among visible navigation links and section controls. They do not change the route; Enter activates the focused link. Tab retains its native behavior, and React Aria popover menus retain their own keyboard handling. The mobile profile menu opens above its footer trigger, aligned to its width and constrained to the viewport; Escape closes the menu and restores focus without dismissing the navigation drawer.

Mobile uses a fixed bottom navigation bar with Catalog, Orders, Changes and Menu. Platform routes replace the shortcuts with Namespaces, Resources and Policies. Menu opens a bottom drawer; focus returns to its trigger on dismissal. Safe-area padding keeps content clear of the navigation. The mobile header contains the compact workspace context selector; desktop breadcrumbs start at the domain, without a redundant Workspace/Platform prefix. Expanded context menus open below their trigger within sidebar width. Selected indicators derive from React Aria selection state. The profile menu includes a disabled Log out item with an authentication-not-connected explanation; there is no simulated sign-out or session API.

Concept-v2 assets are copied from the branding repository. Production SVG symbols remain flat; no generated Stitch logos are reused. Archivo Narrow, IBM Plex Sans and IBM Plex Mono are bundled through Fontsource; license copies are included under public/licenses. Palette, layout and motion use custom CSS Modules and shared CSS variables, not a preset theme. Light/dark/system and sidebar choices persist locally. Motion can follow the system or be reduced; system reduction is never overridden. No WebGPU or HTML-in-Canvas runtime is implemented.

## Static deployment

Deploy the built app's `dist` directory with a fallback to `index.html` for application paths (excluding static assets). Deep-link tests use Vite's preview fallback. No hosting or deployment resources are created in this increment.

## Selective reintegration

Do not merge the Admin-only deletion diff wholesale. Import `gitstore-admin/` and adapt selected frontend commands/CI to the target branch. Retain that branch's backend root instructions, Makefile commands, workflows and services. Review the imported directory in a clean target worktree and run both frontend and target-branch acceptance suites before integration.
