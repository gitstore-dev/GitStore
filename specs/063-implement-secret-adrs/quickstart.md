# Quickstart: Implement Secret Material ADRs

This is an implementation/acceptance guide, not a claim that the proposed
resolver, migration or capacity extensions already exist.

## 1. Establish the baseline

- Use branch `063-implement-secret-adrs`.
- Require the spec-061 identity plane including merged #429 on all participating
  APIs/controllers. Preserve existing public-key enrollment and RBAC.
- Read [research.md](research.md) and the four [contracts](contracts/).
- Do not change the unrelated Rust lockfile or other pre-existing worktree edits.

## 2. Implement test-first in delivery order

Follow the six slices in [plan.md](plan.md). Write failing contract tests before
each implementation slice. The API and controller share contract rules through
the proposed local module; Rust continues to call the Go validation service.

Use the root command interface:

```bash
make help
make check TARGET=config
make check TARGET=compose
make test
make build
make lint
```

During implementation, use the smallest package/test selectors for changed
Go/Rust surfaces; aggregate commands remain the repository-level gate. Add the
shared module to those aggregate commands, CI and both independent Docker
builds. Do not rely exclusively on `go.work` for a successful build.

## 3. Prepare and migrate File references

Deploy the documented preparation baseline before changing manifests. Arrange
the GraphQL consumer transition and freeze File-spec writes during final
cutover; no permanent legacy mode is allowed.

Replace each old bare reference with an operator-confirmed type:

```yaml
source:
  type: s3
  uri: s3://catalog-assets/products/product-hero.jpg
  credentialsRef:
    kind: CredentialsRef
    type: aws-access-key/v1
    secretRef:
      kind: SecretRef
      name: catalog-assets-writer
```

The optional key is omitted because this type needs a multi-item record.
Provision material out of band; this manifest contains no provider or values.
Push through normal validation/admission and verify nested metadata from both
API replicas. Do not directly edit projection rows.

After implementing the proposed audit command:

```bash
make check TARGET=secret-migration
```

Require complete inventory coverage and zero legacy/malformed current manifests
and projections before deploying strict validation. History is not rewritten;
historical restores must migrate before admission. Rollback stops at the
preparation baseline.

## 4. Prove bootstrap and rotation

Keep private key material in a controller-only source, never the shared config
file. For hot rotation, select the planned `json-record` file binding and a
whole-record keyRef; provision the atomic signing bundle outside GitStore.
Existing raw/static-ID config remains supported, but is not sufficient evidence
for overlapping-key rotation.

Start the local stack using its existing deployment procedure:

```bash
make compose
```

Verify startup fails closed for an absent/malformed/oversized record and that a
valid record yields an authenticated ready controller. Do not print token
responses to the terminal or capture them in committed evidence.

Enroll a second public key under a different ID, atomically replace the same
logical signing record, and observe renewal without a manifest/keyRef change.
Retire the old enrolled key only after every replica has transitioned and the
assertion/skew overlap window is satisfied.

## 5. Verify failure, isolation and recovery

Exercise all seven error classes and context cancellation in shared contract
tests. Test whole-record and selected-item semantics, wrong-tier requests,
foreign namespaces, unsupported credential types and oversize/count limits.
Use a contract consumer to prove runtime resource rotation and blocked
operations; no production File runtime status is expected.

In deployed tests, prove wrong-subject issuance is denied, shared identity
mounts fail managed checks, no token or key leaks into any telemetry, and
replacement controllers re-bootstrap without manual token injection.

After implementing the new scenario/profile, run:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=diagnostic \
  REPOSITORY_CAPACITY_SECRET_SCENARIO=1
```

Then use alpha for local bounded evidence and production only with the complete
external topology/dataset and sanitized manifests defined in the
[capacity contract](contracts/capacity-and-rollout.md). Diagnostic cannot pass;
alpha is not a production claim. Use `make chaos` only against the explicit
test-owned controller container, with confirmation.

## 6. Complete documentation and readiness

Update File storage/lifecycle docs, controller identity enrollment/rotation
runbooks, provider-format/config docs, root command help and agent command
reference. Preserve the explicit deferrals: no production File consumer,
payload retrieval, object-store writes, remote provider SDK or JWT/HMAC config
migration.

Before opening a PR:

```bash
make pr-ready
```

Include failing-then-passing test evidence, migration/rollback procedure,
deployed outage/rotation/replacement proof and the declared capacity evidence.
