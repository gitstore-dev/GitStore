# GitStore Admin development

This branch intentionally contains only the Admin. Preserve LICENSE and shared Git history.
Never merge the backend deletions wholesale into the main GitStore branch; import Admin changes selectively.

- Use Node 24 and the pinned pnpm version. The root Makefile is the canonical command interface.
- Run `make check TARGET=all` and `make pr-ready` before handing off changes.
- Keep app features, shared controls, navigation, and layouts separate. Read `gitstore-admin/docs/admin-prd.md` for product direction.
- The current increment includes the shell, Catalog / Products CRUD with a Markdown-first editor and lifecycle-aware demo deletion, and Home / Overview, My work, Recent activity, and Setup guide with typed local demo fixtures. Products and the four Home pages share per-store in-memory sessions across navigation and store switches; reload or a confirmed store reset restores fixtures. Setup guide records demo-only reviews and derives Product completion from saved, non-deleting Products; it shares progress with Overview and never configures settings, categories, or readiness. Overview simulates resolution; My work permits product Title/description editing and personal reviews; Recent activity permits confirmed full authored fixture recovery for Product, CategoryTaxonomy, Collection, and File metadata. Preserve drafts and buffers during recovery; never equate recovery or review approval with publication/readiness. File-backed catalog types (including ProductVariant) use spec.title and Title labels, not Name. Products have no SKU. Markdown descriptions live in top-level body, not spec.description. Preserve untouched Markdown bytes. Product editor AST is transient; ADR-0013 remains Proposed. Product deletion must check dependent variant owners twice, remain visibly Deleting until explicit completion, and preserve history/drafts. Existing demo media only; variants are read-only. Keep the other 74 destinations as placeholders. No API calls, authentication, Apollo, live operational telemetry, browser-storage persistence of demo data, or backend writes.
- Use React Aria interactions, custom CSS Modules, semantic HTML, and reduced-motion support.
- Add stories and focused tests for meaningful interactive behavior. Keep all 79 destinations reachable.
- Use Conventional Commits and include `Refs #288` in Admin commit messages. Do not push or create a PR unless requested.
- Do not change shared Git configuration or other worktrees.
