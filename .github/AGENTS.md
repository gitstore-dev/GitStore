# CI and release instructions

- Releases use Release Please in `workflows/release-please.yml`. Read
  `../docs/runbooks/release-process.md` for versioning, alpha/beta/stable
  graduation and troubleshooting.
- PR titles must be Conventional Commits; `workflows/pr-title-lint.yml`
  enforces them because squash-merge titles drive Release Please.
- `workflows/cd.yml` publishes only the core API, controller manager and Git
  service. Put optional-service images in `workflows/cd-optional.yml`,
  following admin and OIDC bridge. An optional-service build must never block
  or gate the core release.
- Preserve shared-module standalone `GOWORK=off` checks and the full default
  `make pr-ready` suite. Capacity runs do not replace that suite.
