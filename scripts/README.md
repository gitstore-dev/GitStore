# GitStore Scripts

Utility scripts for GitStore development and demonstration.

## run-infrastructure.sh

Runs the DigitalOcean OpenTofu configuration through the root command interface:

```bash
make check TARGET=infrastructure PROFILE=digitalocean
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=init
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=plan
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=show
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=apply
```

Set `INFRA_BACKEND_CONFIG` and `INFRA_VAR_FILE` to absolute private paths for
initialization and planning. `apply` executes the saved plan without a prompt.
See the [provisioning runbook](../docs/runbooks/digitalocean-provisioning.md)
for prerequisites, outputs and application configuration after provisioning.

## run-capacity.sh

Runs a named profile from `tests/capacity/profiles/` with pinned k6 and writes
structured evidence beneath `.gitstore/capacity/`. Invoke it through the root
Makefile:

```bash
make capacity TARGET=api PROFILE=readiness MODE=diagnostic CAPACITY_BASE_URL=http://localhost:4000
```

## run-chaos.sh

Runs an explicitly confirmed, allowlisted Pumba fault profile against one
GitStore container and records before/after inspection evidence:

```bash
make chaos CHAOS_PROFILE=api-pause-5s CHAOS_TARGET=gitstore-api CHAOS_CONFIRM=1
```

For normal repository checks, run the licence scripts through the root Make target:

```bash
make check TARGET=licenses
```

Use the scripts directly only when you need a narrower mode such as `--staged`.

## check-credential-log-leakage.sh

Checks the service-account assertion and access-token providers plus the
controller GraphQL client for Zap fields that could carry raw tokens,
assertions, or private keys. Run it through the root Makefile:

```bash
make check TARGET=credentials
```

The check excludes test files and is required by the main CI workflow.

## check-license-headers.sh

Validates tracked Go (`.go`), Rust (`.rs`) and JavaScript/TypeScript
(`.ts`, `.tsx`, `.js`, `.jsx`, `.mjs`, `.cjs`) files through one checker.
`make check TARGET=licenses DIFF_BASE=origin/main` runs both the full
header check and the changed-file current-year check.

### Usage

```bash
# Check all tracked sources using working-tree content
./scripts/check-license-headers.sh --all

# Check staged additions, modifications and rename destinations from the index
./scripts/check-license-headers.sh --staged

# Check working-tree sources changed between a base ref and HEAD
./scripts/check-license-headers.sh --diff-base origin/main
```

The required file header is:

```
// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
```

Copyright may use a single year or a `YYYY-YYYY` range. Changed files must
have the current year as either endpoint; unchanged files need not be updated.
Headers must appear within the first 12 lines (Go/Rust) or 14 lines (JS/TS).
Generated-file exceptions are language-specific:

- Go: `Code generated` or `DO NOT EDIT` in the first 8 lines, case-sensitive.
- Rust: the Go markers plus `@generated` in the first 8 lines, case-sensitive.
- JS/TS: `Code generated`, `AUTO-GENERATED` or `DO NOT EDIT` in the first
  12 lines, case-insensitive.

The pinned `shared/scylla-cdc-go` Go sources retain their original upstream
Apache attribution rather than receiving GitStore headers. Their
`LICENSE.txt` must contain the Apache license notice. Staged checks read
that notice from the index too, including when only the license is changed
or deleted.

`.github/workflows/license-headers.yml` runs checker regressions and full
header validation on pushes to `main` and pull requests touching checked
sources or checker inputs, plus changed-file validation on pull requests.
Run the focused regressions with `bash scripts/test-license-headers.sh`;
they are also included in the default `make test` suite.

## install-git-hooks.sh

Installs repository-local git hooks into the standard `.git/hooks` directory, enabling automatic staged-file licence checks and Conventional Commits validation.

### Usage

```bash
./scripts/install-git-hooks.sh
```

This installs the `pre-commit` and `commit-msg` hooks into `.git/hooks`.

## update-env-secret.sh

Updates a `KEY='value'` entry in one or more `.env` files. If the key exists, the script replaces it in place. If the key is missing, it appends it. If the file is missing, the script creates it.

### Usage

```bash
./scripts/update-env-secret.sh GITSTORE_API__AUTH__JWT__SECRET "new-secret" gitstore-api/.env
./scripts/update-env-secret.sh GITSTORE_GRPC_AUTH__HMAC_SECRET "shared-secret" gitstore-api/.env gitstore-git-service/.env
```
