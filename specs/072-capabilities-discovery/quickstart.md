# Planning Quickstart

**Status**: Proposed behavior. Discovery and `me` below do not exist yet. This document defines validation after implementation; do not treat these as currently supported deployment procedures.

## Current Planning Context

```sh
git branch --show-current
SPECIFY_FEATURE=072-capabilities-discovery .specify/scripts/bash/check-prerequisites.sh --json --paths-only
```

Expected branch: `codex/072-capabilities-discovery`. The environment override compensates for older scripts requiring a bare numeric prefix; it does not rename the branch. `.specify/feature.json` points to this feature directory.

## After Implementation

1. Configure discovery explicitly with full public URLs and an operator-owned capability floor. Validate the same publication profile on each eligible API before activation; follow [operations](contracts/operations.md). Never derive interactive client IDs from the verifier audience.
2. Configure a pre-registered native public client with accepted grants/scopes/callbacks and API-compatible tokens. Enable optional DCR/CIMD descriptors only after provider/fixture validation; preserve explicit method selection. Resource indicators require their own provider/token support.
3. Start a verified test-owned deployment through the existing Make/Compose workflow. Keep new advertisements disabled during code rollout; enable only after every recipient supports them. Storefront remains absent while its handler is a placeholder.
4. Fetch the configured full discovery URL without credentials. For a local explicitly permitted development profile, the future read would look like:

```sh
curl --include http://localhost:4000/.well-known/gitstore
```

Expect canonical public JSON and ETag. Repeat using the returned validator with `If-None-Match`; unchanged valid content returns 304. Inject metadata expiry and verify the validator/flow state changes. Proxy-prefixed publication must return external addresses verbatim.

5. Use the [authenticated query example](contracts/authorization.md) with an existing ordinary user's private credential through the established client/token-file workflow. Verify a namespace create check, an existing-resource check and cross-namespace denial. Ordinary self-review must not need arbitrary-subject review grants; a non-supporting provider returns explicit unsupported.
6. Grant diagnostic/configuration read actions only to the intended operator role. Verify protected views distinguish missing configuration, forbidden access and provider degradation without raw secrets/policy. Existing error/admission extensions remain intact.

## Contributor Verification During Implementation

```sh
make generate TARGET=authz
make test
make lint
make pr-ready
```

Use the relevant existing test selectors and `make generate TARGET=all` where needed for schema changes; generated Admin schema sources must match SDL. Run `graphify update .` after code changes. These checks are implementation gates, not commands that establish the feature exists today.

Owned deployed acceptance extends the existing `make capacity TARGET=api PROFILE=endpoint-split MODE=...` family; it requires the ownership, complete URL/token-file settings, retained dataset and connected workload in the existing capacity README plus the future documented probe mode. Do not run a partially configured live test. See [operations](contracts/operations.md) for rates, payloads, topology, fault profiles and correctness/recovery gates. Planning creates no containers and changes no retained dataset.
