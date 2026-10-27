# Test instructions

- `make test` runs the existing Rust/Go/shared-module suites, including local
  capacity regressions. `make pr-ready` retains the full default suite;
  deployed capacity scenarios run only through `make capacity`.
- For capacity, profiling, chaos or owned-deployment work, also read
  [capacity/AGENTS.md](capacity/AGENTS.md). This includes capacity-related Go
  files under `integration/`, not only files inside `capacity/`.
- Verify concurrent API/controller replica correctness and rolling upgrades.
  Keep test evidence scoped to what the fixture actually exercises.

## Datastore contracts

- `make test TARGET=datastore` runs backend-neutral contracts without an
  external Scylla instance.
- `make test TARGET=datastore DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>`
  runs tagged Scylla contracts.

## Native Git integration

- `make test TARGET=git-integration GIT_TEST_OWNED_DEPLOYMENT=1 GIT_TEST_URI=<host:port> GIT_TEST_HMAC_SECRET_FILE=<private-file>`
  runs race-enabled signed deletion-response recovery against an explicitly
  owned singleton. Use existing RPC signing material, never an operator
  deployment. Only disposable UUID repositories are created/removed.
- The response-loss interceptor discards a successful native reply before
  the caller sees it. It is not a network outage, server replacement,
  API/datastore saga or sustained-load acceptance.
- Add `DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>` for Repository/Namespace
  finalizer sagas through two independent in-process APIs and Scylla sessions
  with actual signed Git removal. Use an isolated test-owned keyspace and
  random storage UUIDs.
- This covers lost Git replies, partial projection cleanup, pre-authoritative
  failure, lost authoritative replies and stale-UID same-name recreation.
  Its API-side Git registry is the existing allow-all test provider, not
  deployed HTTP/RBAC, a physical network fault or sustained-load acceptance.

## Secret integration

- `make test TARGET=secret-integration SECRET_TEST_OWNED_DEPLOYMENT=1 SECRET_TEST_API_A=<base-url> SECRET_TEST_API_B=<base-url> SECRET_TEST_TOKEN_FILE=<path>`
  runs real controller bootstrap, isolated provider outage, enrolled-key
  overlap/retirement and process replacement against two owned APIs sharing
  Scylla. Both APIs must clamp ServiceAccount tokens to 60 seconds.
- The existing cluster-scoped `gitstore-controller-manager` identity needs
  controller RBAC permissions. `SECRET_TEST_SERVICEACCOUNT` may select a
  different test identity; `SECRET_TEST_NAMESPACE` is rejected. Enroll/remove
  only test-generated keys; controllers, records and checkpoints are
  test-owned. Never target an operator deployment.
- Set `SECRET_TEST_REINCARNATION_API=<disposable-issuer-base-url>` and
  `SECRET_TEST_REINCARNATION_SERVICEACCOUNT=<initially-absent-test-name>`
  together to test unexpired-token rejection after same-name/same-key
  recreation on both unchanged targets. The disposable issuer shares their
  datastore/signing material and alone grants issuance for that name; leave
  it unbound on the targets. Create/delete only the new test identity,
  never the enrolled controller. Without these flags explicitly report
  that this access-token case was not exercised.
