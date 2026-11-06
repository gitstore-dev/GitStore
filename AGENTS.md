# GitStore Admin development

This branch intentionally contains only the Admin. Preserve LICENSE and shared Git history.
Never merge the backend deletions wholesale into the main GitStore branch; import Admin changes selectively.

- Use Node 24 and the pinned pnpm version. The root Makefile is the canonical command interface.
- Run `make check TARGET=all` and `make pr-ready` before handing off changes.
- Keep app features, shared controls, navigation, and layouts separate. Read `gitstore-admin/docs/admin-prd.md` for product direction.
- The current increment includes the shell, Home / Overview, and Home / My work with clearly labeled, typed page-local demo fixtures. Overview simulates resource resolution. My work permits editing product name/description and simulated draft submission, discard, and personal reviews. Keep the other 77 destinations as placeholders. These pages have independent state that resets on exit, namespace switch, reload, or reset. No API calls, authentication, Apollo, live operational telemetry, or backend writes.
- Use React Aria interactions, custom CSS Modules, semantic HTML, and reduced-motion support.
- Add stories and focused tests for meaningful interactive behavior. Keep all 79 destinations reachable.
- Use Conventional Commits. Do not push or create a PR unless requested.
- Do not change shared Git configuration or other worktrees.
