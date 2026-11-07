# GitStore Admin development

This branch intentionally contains only the Admin. Preserve LICENSE and shared Git history.
Never merge the backend deletions wholesale into the main GitStore branch; import Admin changes selectively.

- Use Node 24 and the pinned pnpm version. The root Makefile is the canonical command interface.
- Run `make check TARGET=all` and `make pr-ready` before handing off changes.
- Keep app features, shared controls, navigation, and layouts separate. Read `gitstore-admin/docs/admin-prd.md` for product direction.
- The current increment includes the shell and Home / Overview, My work, and Recent activity with typed local demo fixtures. The three pages share per-store in-memory sessions across navigation and store switches; reload or a confirmed store reset restores fixtures. Overview simulates resolution; My work permits product Title/description editing and personal reviews; Recent activity permits confirmed full authored fixture recovery for Product, CategoryTaxonomy, Collection, and File metadata. Preserve drafts and buffers during recovery; never equate recovery or review approval with publication/readiness. File-backed catalog types (including ProductVariant) use spec.title and Title labels, not Name. Products have no SKU. Keep the other 76 destinations as placeholders. No API calls, authentication, Apollo, live operational telemetry, browser-storage persistence of demo data, or backend writes.
- Use React Aria interactions, custom CSS Modules, semantic HTML, and reduced-motion support.
- Add stories and focused tests for meaningful interactive behavior. Keep all 79 destinations reachable.
- Use Conventional Commits. Do not push or create a PR unless requested.
- Do not change shared Git configuration or other worktrees.
