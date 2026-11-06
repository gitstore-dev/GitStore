# GitStore Admin

<picture><source media="(prefers-color-scheme: dark)" srcset="gitstore-admin/public/branding/readme-dark.png"><img src="gitstore-admin/public/branding/readme-light.png" alt="GitStore — Change boldly. Keep a way back."></picture>

An isolated, mock-first Admin foundation: React, strict TypeScript, Vite, TanStack Router, React Aria, and custom CSS Modules. All 79 PRD destinations are navigable: Home / Overview and Home / My work are interactive mock pages; the other 77 remain placeholders. My work includes a limited, demo-only product name/description editor and personal review workflow. There is no authentication, backend connection, or GraphQL runtime.

## Start

Use Node from `.nvmrc` and pnpm 12.7.0, then:

```sh
make install
make dev TARGET=admin
# In another terminal:
make dev TARGET=storybook
```

Vite prints the app URL; Storybook uses port 6006. Use the workspace picker for local demo contexts. Cmd/Ctrl+K searches pages. The header icons toggle the inspector and Focus Mode. Profile preferences include reduced motion; system reduced motion always wins.

Focus Mode clears the surrounding interface with a reversible, approximately 420 ms directional wipe and page settle. The working DOM remains mounted. View Transitions provide the primary effect, with a Web Animations fallback. Browsers exposing native HTML-in-Canvas can also use a short-lived WebGPU edge dissolve. Capability adapters cover the earlier `layoutsubtree` / `copyElementImageToTexture` APIs (including Chrome 150 dictionary arguments and earlier positional arguments) and the newer `content="drawable"` / `drawElementImageToTexture` API. GPU preparation starts on hover or keyboard focus, expires after eight idle seconds, and never blocks the DOM fallback; snapshot capture has a 60 ms budget. The core transition needs no flags or graphics polyfills. Earlier experimental Chromium implementations need the Canvas Draw Element flag enabled to use the GPU enhancement. Reduced motion bypasses capture and animation. GPU resources and decorative, inert snapshots are removed after completion, interruption, device loss, or navigation.

Home / Overview uses deterministic local resources, readiness counts, filtering, sorting, resource inspection, and confirmed demo-only resolution. Atelier Kyoto starts populated; Studio Berlin starts with setup guidance. Demo changes reset when leaving Overview, switching namespaces, reloading, or choosing Reset demo. They are never saved or sent to a backend. Storybook includes the populated, first-use, all-clear, filtered-empty, loading, unavailable, stale, restricted, inspector, and confirmation states.

Home / My work provides Drafts, Submitted, Assigned to me, and Completed views with search, sorting, a field-by-field change preview, product text editing, and confirmed demo submission/discard/review actions. Atelier Kyoto has seven personal-work examples; Studio Berlin starts empty. All changes are page-local and reset on exit, store switch, reload, or Reset demo; Overview remains independent. Approved reviews do not publish products. Storybook covers every view, editor/confirmation states, read-only presentation, failures, themes, mobile, and reduced motion.

## Validate

```sh
pnpm --dir gitstore-admin exec playwright install chromium firefox webkit
make check TARGET=all
make test TARGET=e2e
# Or the complete gate:
make pr-ready
```

Browser tests require both app and Storybook builds, produced by `make check` / `make build TARGET=all`. Screenshots and reports are written beneath `gitstore-admin/test-results` and `playwright-report`. CI performs only frontend checks.

## Build one page at a time

Each thin file in `gitstore-admin/src/routes` renders the shared placeholder by a registry ID. Replace that route component with a feature page, keep its navigation identity, then add its stories and browser coverage. Keep UI preferences separate from future server data. Add Apollo Client 4, typed operations and contract-backed mocks when the first data-driven page needs them. No fake profile/permission API has been introduced for the shell.

[Admin PRD](gitstore-admin/docs/admin-prd.md) · [Architecture and workflow](gitstore-admin/docs/development.md)

## Branch boundary

This branch deliberately removes backend and deployment content while retaining GitStore history and the license. **Do not merge this entire branch into the backend branch.** Import the Admin directory and deliberately selected frontend tooling/documentation; exclude backend deletions and root-file replacements. Current backend worktrees and shared Git configuration are untouched. The copied PRD is planning context, not a declaration of shipped commerce functionality.

Overview animates explicit filtering, sorting, reset, and confirmed demo resolution
with measured HTML card movement. A single optional WebGPU canvas adds readiness
halos behind the cards; it does not require HTML-in-Canvas. Rendering is bounded
and stops at rest, reduced motion uses static CSS, and unavailable/lost GPU devices
retain the same controls and state. Storybook includes motion and CSS-only lighting
specimens; see the Home motion treatment in the Admin PRD for timing and boundaries.
