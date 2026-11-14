# CI and release instructions

- Releases use Release Please in `workflows/release-please.yml`. Read
  `../docs/development/release-process.md` for versioning, alpha/beta/stable
  graduation and troubleshooting.
- PR titles must be Conventional Commits; `workflows/pr-title-lint.yml`
  enforces them because squash-merge titles drive Release Please.
- `workflows/cd.yml` publishes only the core API, controller manager and Git
  service. Put optional-service images in `workflows/cd-optional.yml`,
  following the OIDC bridge. An optional-service build must never block
  or gate the core release. Image publication is not application deployment,
  smoke validation or rollback; those remain operator procedures.
- Preserve shared-module standalone `GOWORK=off` checks and the full default
  `make pr-ready` suite. Capacity runs do not replace that suite.
- Core CI runs on PRs, merge groups and main pushes; use its manual dispatch
  for branches without a PR. Cancel superseded PR runs only, never main or
  merge-group validation. Keep required job names unchanged.
- Shared Go modules run sequentially in one cached job, retaining each module's
  pinned toolchain and `GOWORK=off`. Optional Rust coverage runs on main pushes;
  formatting, Clippy, builds, tests and security scans retain their event coverage.
