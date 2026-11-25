# Test instructions

- `make test` runs the existing Rust/Go/shared-module suites, including local
  capacity regressions. `make pr-ready` retains the full default suite;
  deployed capacity scenarios run through `make capacity`, not as substitutes.
- For capacity, profiling, chaos or owned-deployment work, also read
  [capacity/AGENTS.md](capacity/AGENTS.md). This includes capacity-related Go
  files under `integration/`, not only files inside `capacity/`.
- Verify changed API/controller concurrency, conflict, migration and replay risks
  with focused tests under declared provider contracts. Development-only
  profiles/providers have no replica-safety or distributed qualification gate.
  Keep useful multi-handler/reconciler in-process tests, including shared memdb
  fixtures, for races, isolation, CAS, idempotency and retry; they do not certify
  independent memdb processes or waive shared production invariants.
  Full deployed rolling-upgrade, capacity and chaos
  qualification is opt-in production work, not a routine alpha PR gate.
  Keep evidence scoped to what the fixture actually exercises; local adapters
  do not establish deployed behavior.
- `make test TARGET=datastore` runs backend-neutral contracts without external
  Scylla. Add `DATASTORE=scylla SCYLLA_TEST_ADDR=<host:port>` for tagged Scylla
  contracts, using an isolated test-owned keyspace. Tagged packages run
  sequentially to avoid cross-package DDL/snapshot contention on the shared
  fixture; concurrent-writer tests retain their internal concurrency.
- `make test TARGET=secret-integration` is an explicit opt-in deployed suite.
  Set `SECRET_TEST_OWNED_DEPLOYMENT=1`, `SECRET_TEST_API_A=<base-url>`,
  `SECRET_TEST_API_B=<base-url>` and `SECRET_TEST_TOKEN_FILE=<private-file>`.
  Use two owned APIs sharing Scylla, with 60-second ServiceAccount tokens and
  an existing cluster-scoped controller identity with controller RBAC.
  `SECRET_TEST_SERVICEACCOUNT` may select another test identity;
  `SECRET_TEST_NAMESPACE` is rejected. Enroll/remove only test-generated keys.
- Never target an operator deployment. Create and remove only test-owned
  resources, keys, records and checkpoints; preserve unrelated identities,
  repositories, containers and volumes.
- Report skipped deployed scenarios and fault types explicitly. A simulated
  lost response, local allow-all provider or in-process API does not prove a
  physical network outage, deployed RBAC, process replacement or sustained
  load acceptance.
- Storage tests verify access contracts, identity/claim/fence safety and bounded
  automatic drift recovery, not a blanket prohibition of secondary indexes or
  materialized views. Cover startup/resume, detected and missed drift, concurrent
  writers and interruption without operator repair. Preserve meaningful
  projection/failure-injection tests; record the actual ScyllaDB build rather
  than assuming a pre-GA version is immutable.
