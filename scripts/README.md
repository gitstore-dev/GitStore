# GitStore Scripts

Utility scripts for GitStore development and demonstration.

The default `make test` and `make pr-ready` suites run the local Translation
authoring, read/watch and evidence-oracle regressions alongside existing checks.
They do not enable native deployment, capacity or fault workloads.

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

`CAPACITY_TRANSLATION_READINESS=1` selects protected, authenticated exact-source
read diagnostics on the same `api/readiness` selector, never health-only
fallback. It requires an explicitly owned deployment, two API endpoints,
a fresh run and a private ten-namespace source fixture.
`CAPACITY_TRANSLATION_PUSHES=1` additionally starts the bounded native companion
and synchronizes real Git pushes with those public reads. Connected receipts
verify exact source/watch/current-status joins and unchanged target/Product
material and retained history. A bounded thirty-second read warmup includes
twenty verified pushes; fifteen seconds of read cooldown keep the final resource
scrape within steady offered traffic. Preparation has separate counters and
source joins, never extends the measured window or dilutes measured latency,
and uses the existing bounded client pool. Requests follow absolute offer
timestamps after executor initialization; completion waits for the entire
cooldown boundary. Bounded failure-reason counters distinguish schedule errors
from source/status mismatches without exposing response content.
Both modes currently reject alpha/production
selection and never certify sustained or fault acceptance.
An explicit `services.translationTopology` manifest block adds live immutable
image/PID/endpoint/ownership/storage/ceiling checks on an exclusively owned
container cohort. With Prometheus, original-scrape timestamps and completed-GC
live-heap signals support fault-free full-window resource checks. Native
`process_start_time_seconds` must match live preflight throughout those scrapes;
missing, changed or stale evidence fails closed unless an independently declared
physical replacement joins original-scrape per-process resource baselines and
one bounded `services.translationResourceGaps` entry. The API/controller
replacement verifier requires immutable target and unchanged same-role peer
captures; controller captures additionally retain independent checkpoint
storage across replacement. These checks still retain diagnostic-only results.
`CAPACITY_TRANSLATION_CALLBACK_STAGE=callback-loss|reply-loss` adds one declared
simulated boundary to a 20-to-30-second connected diagnostic using the existing
owned callback proxy and Catalog route. Exact interception/replay, unchanged
references and five seconds of native both-API source reads must join the
acknowledged measured commit; it does not enable physical or sustained claims.
See the [capacity guide](../tests/capacity/README.md)
for fixture and receipt requirements. Identity mode remains independently
selectable and cannot be combined with Translation mode.

## run-chaos.sh

Runs an explicitly confirmed, allowlisted Pumba fault profile against one
GitStore container and records before/after inspection evidence:

```bash
make chaos CHAOS_PROFILE=api-pause-5s CHAOS_TARGET=gitstore-api CHAOS_CONFIRM=1
```

For owned API/controller restart or journal interruption, select exactly one of
`CHAOS_RBAC=1` and `CHAOS_TRANSLATION=1`, with an explicit owner and predeclared
window of at most 30 seconds. Controller peers need independent retained
checkpoints; Translation API restart also requires an unchanged owned API peer.
Both controller checkpoint mounts must survive replacement unchanged; their
sanitized before/after identities are retained in protected captures.
Physical injector/restoration receipts never certify domain recovery. See
[the chaos guide](../tests/chaos/README.md) for the complete ownership contract.

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

Installs the repository's `pre-commit` and `commit-msg` hooks, enabling
staged-file licence checks and Conventional Commits validation. Git resolves
the hooks directory, so installation works from a main checkout or linked
worktree, including paths with spaces.

### Usage

```bash
./scripts/install-git-hooks.sh
```

You may invoke the script from a subdirectory using its relative or absolute
path. Hooks are clone-shared: installing from any worktree updates the hooks
used by **all linked worktrees**. The installer prints this warning and does
not change Git configuration or create per-worktree hook settings.

Identical installed hooks may be reinstalled. A configured `core.hooksPath`,
differing installed hook, or symlinked hook causes installation to stop without
replacing hooks. Review ownership and preserve any existing integration before
an explicitly approved replacement; do not remove another worktree's hooks
merely to make installation succeed.

`bash scripts/test-license-headers.sh` covers installation in fresh test-owned
repositories and linked worktrees as well as the licence checks.

## update-env-secret.sh

Updates a `KEY='value'` entry in one or more `.env` files. If the key exists, the script replaces it in place. If the key is missing, it appends it. If the file is missing, the script creates it.

### Usage

```bash
./scripts/update-env-secret.sh GITSTORE_API__AUTH__STATIC_USERS__JWT__SECRET "new-secret" gitstore-api/.env
./scripts/update-env-secret.sh GITSTORE_GRPC_AUTH__HMAC_SECRET "shared-secret" gitstore-api/.env gitstore-git-service/.env
```
