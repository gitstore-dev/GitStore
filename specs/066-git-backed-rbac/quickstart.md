# Implementation Validation Quickstart

RBAC contracts, component implementations and local tests are available,
but production OPA startup is deliberately refused while final acceptance
remains pending. Local checks and explicitly selected owned native capacity
diagnostics and the strict healthy-alpha runner are supported. A deployed
healthy-alpha pass remains pending. The production sequence below is not a currently
usable deployment recipe. Use only an explicitly owned environment.

## 1. Verify local contracts

Start with the model, finite actions, resource schemas, store/RPC boundaries
and failing tests described in [plan.md](plan.md) and
[contracts/capacity-recovery.md](contracts/capacity-recovery.md).
Preserve the current spec's Staff-only owner policy and validation-start
consistency semantics.

Run the existing local generation and datastore checks:

```sh
make generate TARGET=all
make test TARGET=datastore
```

Run native datastore contracts using the existing Scylla selector/address
configuration and an owned isolated keyspace:

```sh
GOFLAGS=-p=1 make test TARGET=datastore DATASTORE=scylla \
  SCYLLA_TEST_ADDR=<owned-address>
```

These contracts cover same-tier atomicity, CDC, source progress and
response-loss behavior. They do not prove deployed API/controller convergence.

## 2. Prepare a compatible owned stack (pending)

Reuse the existing Compose/configuration and identity helpers, not a new
feature-specific overlay. The standard command family is:

```sh
make compose DATASTORE=scylla
```

This command alone does not create the required two-API/two-controller
acceptance topology. Use the existing owned fixture/replica runner, with
separate controller checkpoint volumes and one Git process. Provision
private identity records through existing explicit enrollment helpers;
never replace operator keys or embed tokens in examples.

Apply the additive RBAC migration with `gitctl migrate`. With automatic
migration disabled, verify startup history validation remains read-only.
The implemented provider name is `opa`; its limits and write gate are
documented in [configuration](../../docs/configuration.md). Selecting it
currently refuses ordinary production startup. Do not remove that refusal
to follow this sequence; native acceptance must use a narrowly scoped,
test-only composition before production cutover is approved.

## 3. Exercise runtime authorization (pending)

Create an owned namespace and push the RoleBinding example from
[resource-api.md](contracts/resource-api.md) to its system repository.
Verify its admitted commit, projected resource and generic watch event.
Send an authorized product request directly to each API at
`/admin/graphql`; observe grant propagation, then remove the binding and
verify denial on both APIs and an existing subscription.

Repeat through GraphQL CRUD and confirm identical common admission:
wrong scope, unknown verb, unprovable grant and built-in/managed
tampering must fail on both paths. Use a hypothetical access review only
with review permission; it never establishes a caller's real authority.

## 4. Exercise personal namespaces and bootstrap (pending)

As a trusted Staff user, create a USER namespace whose name matches the
AuthN-selected subject, not a substituted directory username. Verify one
protected `namespace-owner-access` binding referencing
`namespace-full-control`, Namespace UID and correct owner subject.
Observe Pending/Ready/Failed provisioning status separately from API cache
readiness. Attempt duplicate reconciliation, collision, tampering and
old-UID work after deletion/recreation.

For the opt-in native identity case, set `RBAC_TEST_OWNER_IDENTITY_RUN=1`,
`RBAC_TEST_CONTROLLER_REPLACEMENT_RUN=1` and
`RBAC_TEST_OWNER_USERNAME` to the verified Staff directory username, distinct
from `RBAC_TEST_SUBJECT`. Run
`TestRBACNativeOwnerSubjectDiffersFromUsername` with the existing owned native
fixture inputs. It verifies the Staff UID on both APIs, rejects directory-name
substitution, and exercises GraphQL recreation and signed-Git creation with
independent controller checkpoints. Use an explicitly qualified UID mapping
and tokens for that authenticated subject; do not relabel an unrelated actor.

Remove the ordinary initial-admin binding after establishing another
administrator, restart the owned API, and confirm the removed binding is
not silently re-created. Built-in system bindings remain immutable.

## 5. Exercise cold start and cursor recovery (pending)

Restart one API while continuing requests to the other. Before a complete
cluster snapshot/watch boundary is installed, the restarted API must be
unready and direct authorization attempts must fail retryably. Namespace
tiers cannot allow before their own snapshots are valid.

Use the owned chaos runner for API restart and a declared journal
interruption. Verify source heartbeat, stale/cursorless denial, full
resync and recovery <=30 s. Reconnecting a transport is not sufficient.

## 6. Run owned diagnostics, then declared acceptance (pending)

The existing families support an explicitly selected diagnostic workload:

```sh
CAPACITY_RBAC_WORKLOAD=1 make capacity TARGET=namespace PROFILE=watch MODE=diagnostic
CAPACITY_RBAC_WORKLOAD=1 make capacity TARGET=api PROFILE=readiness MODE=diagnostic
```

Supply the private seed declaration, live owned topology, independently enrolled
ordinary principals and per-API source/decision captures documented in
[the capacity README](../../tests/capacity/README.md). The optional mixed
Git/GraphQL mutation companion requires its separate holdout principal and
original admission clocks. Replay checks complete per-API ordinary outcomes,
exact revision-pinned propagation and producer-nanosecond provider timings.
The RBAC driver's four-hour outer lifetime includes fixture preparation; it
does not extend individual operations, seed readback or propagation deadlines.

The same two families accept `MODE=alpha` only with the complete healthy-alpha
shape: maximum root batch under ordinary load, fixed 48-second root preamble,
1,800 seconds of mixed Namespace writes at 50/minute across four hot tiers and
1,860 seconds of ordinary traffic at 1,000 HTTP/s total. Build each owned
artifact with the existing `make build` capacity receipt selectors documented
in the capacity README; the API uses `CAPACITY_BUILD_KIND=owned-rbac`, not a
production activation bypass. Declare private build receipts in the topology,
hardware/configuration manifests and the exact selected run receipt directory.
Both APIs must supply actual SDK resource bounds, complete source joins and
independently replayed provider/propagation percentile gates. Independent
sealed postflight is mandatory for both families.

Production RBAC selection remains refused. Implemented alpha wiring is not a
deployed pass. Diagnostic passes, including short full-rate windows, do not
satisfy the remaining acceptance gates; neither does an unselected default
health-only run. Use the
1,000-namespace/10,000-role/50,000-binding
dataset, sustained 50 writes/min, decision/propagation oracle and
30-minute load/fault gates from the capacity contract.

Before declaring implementation ready:

```sh
make test
make lint
make check TARGET=all
make pr-ready
```

Also measure actual API binary and image size before/after OPA with
identical architecture/build settings. The planning probe is not a
substitute. Publish scoped acceptance and mark unavailable deployed
scenarios pending rather than substituting local fixture results.
