# Contract: Migration, Replica Safety and Capacity Evidence

## Release and migration gate

The user requires migration before strict deployment, not indefinite legacy
support. Every strict-release entry point rejects bare File references.

1. **Identity prerequisite**: upgrade API/controllers to the normalized #429
   protocol first. This feature does not bridge pre-#429 exchange fields.
2. **Preparation release**: deliver a separately versioned compatible File
   decoder that preserves both legacy and explicit metadata while migration
   runs. Upgrade all affected readers before writing explicit wrappers.
   Old readers must not silently discard nested fields.
3. **GraphQL consumer preparation**: the change from `SecretRef` to
   `CredentialsRef` is breaking. Migrate clients before the schema transition,
   temporarily restricting their selection to common fields such as `kind`
   during a mixed-schema rollout; enable nested selections only once all
   endpoints expose the new schema. Declare/version the breaking change.
   Do not claim old `credentialsRef { name }` queries work afterward.
4. **Migration**: a proposed paginated/read-only `gitctl` audit enumerates
   supported deployment repositories/active refs and File projections and
   reports legacy/malformed paths, counts and cursors without resolving secrets.
   Operator-reviewed Git commits replace references with explicit types.
   Replay/admit through normal Git-owned paths; never directly patch datastore
   JSON to bypass validation. Type selection is operator-confirmed, not guessed
   from the old bare reference.
5. **Cutover check**: proposed `make check TARGET=secret-migration` requires
   zero legacy/malformed active manifests and projections across the declared
   deployment inventory, and records schema/client baseline versions. Limit
   each audit page to 500 records and concurrency to 4; interrupted audit resumes
   from cursors. Audit completeness is explicit; a sampled scan cannot pass.
6. **Strict release**: remove temporary legacy decoding; roll API replicas
   while the preparation baseline still understands explicit references.
   Freeze File-spec writes during the final audit/cutover window so an old
   preparatory replica cannot reintroduce a bare reference. Unrelated writes
   and reads continue. Resume File writes only after every API is strict.
7. **Rollback**: roll back only to the preparation baseline, which reads the
   migrated shape and the normalized identity contract. Never roll back to an
   unmodified bare-only reader. Keep the File-write restriction if a rollback
   re-enables legacy admission; migration audit must pass before strict re-entry.

Historical Git commits are retained. Any restoration or new push of a legacy
File document must migrate it before admission; no runtime history walk is
introduced. Re-audit projected data when rebuilding from old active refs.

The migration gate is a new planned root target. Wire its help, `gitctl`
integration, config, docs and CI checks together; it is not implemented by this
planning command. There is no strict-release legacy acceptance switch.

## Capacity interface

Extend the existing public command:

```bash
make capacity TARGET=repository PROFILE=lifecycle MODE=production \
  REPOSITORY_CAPACITY_SECRET_SCENARIO=1
```

`REPOSITORY_CAPACITY_SECRET_SCENARIO` is a proposed extension, not an existing
working option. Require it for spec-063 acceptance. Wire it through Makefile,
dispatcher, stack setup, sanitized manifests, evidence validation, domain
verifier and docs. Existing runs without it retain their existing meaning and
cannot count as spec-063 passes.

Use `tests/integration/repository_lifecycle_capacity_test.go` and its existing
helpers. Add actual Git pushes of File manifests; Repository metadata-only
mutations cannot prove reference validation. Also run focused Go contract/race
tests for runtime material consumers, which have no deployed File consumer.

## Workload and topology

| Dimension | Production requirement |
| --- | --- |
| Dataset | At least 5,000,000 Product rows in durable datastore; seeded and counted before offered load |
| Foreground | Bounded rotating pool of 100 Files; batches of <=10 Files and <=128 KiB/push, explicit typed references |
| Sustained load | 10 Git pushes/s for 60m; no hidden unbounded client queue |
| Burst | 100 pushes over 1s once per minute, drain within 30s |
| Client bounds | 32 push workers, queue <=256 batches; shed/drop is measured and fails the normal-load gate |
| APIs/controllers | >=2 real processes each, unique process IDs and shared durable identity/projection state |
| Git services | >=2 instances with disjoint assigned repository storage for production hook/routing evidence; no shared-volume multi-writer claim |
| Resource-runtime resolver | 32 contract-consumer callers contend on 16 in-flight slots; explicit saturation/deadline checks |
| Identity renewal | Test-issued access TTL 60s, subject to configured server bounds, forcing repeated exchanges on both controllers |
| Stabilization | 5m before and 10m after offered load |

Use replica-owned regular local provider files on controlled mounts. Do not
point failure injection at an operator's real credentials. Negative namespace/
type/auth cases are labeled separate correctness traffic, not blended into the
success error rate.

The managed alpha lifecycle stack currently has one Git service; alpha can
validate development functionality but cannot establish the two-Git-service
production topology. Extend manifests/preflight to record and verify all
production Git endpoints and their repository placement rather than reusing
the existing singleton field as if it proved replication.

## Thresholds

| Signal | Required result |
| --- | --- |
| Pure reference validation | p95 <=5ms, p99 <=20ms per File |
| Healthy local resolution | p95 <=10ms, p99 <=50ms |
| Git push acknowledgment | p95 <=2s, p99 <=30s including bursts |
| Existing lifecycle visibility | Preserve production p95 <=1s/p99 <=3s; alpha p95 <=2s/p99 <=3s |
| Unexpected healthy errors | <0.1%; zero dropped offered batches |
| Data integrity | Zero acknowledged projection loss, cross-namespace success or secret leakage |
| Provider concurrency | <=16 per instance; zero internal queued operations |
| Identity exchange | <=1 in flight per source; retry delay <=30s including jitter |
| Resource saturation | Normalized CPU <80%, warmed RSS growth <10%; post-load goroutines return within 10% of stabilized baseline |
| Recovery | Both controllers regain authenticated progress <=60s after valid record restoration |

Separately measure fault windows: intentional denied operations are expected
and must fail closed, while no unaffected healthy traffic may be silently
excluded. Preserve the existing lifecycle gate's stricter invariants where
applicable. These are targets, not measured results.

## Fault schedule and domain verifier

1. Prove fresh baseline token issuance and active reconciliation on both
   controllers. Verify a typed File projection reads identically from both APIs.
2. At minute 15, withdraw only the test-owned signing record for controller A
   for 90s, spanning the configured token lifetime; controller B remains usable.
   Atomic rename/restore is performed by the fixture owner, with cleanup on
   cancellation. No service gets provider write privileges.
3. Verify bounded attempts, classified failure, no usable-expiry extension,
   false credential readiness after exhaustion, and no token/PEM in telemetry.
   Restore the record and prove A obtains a fresh token and resumes work <=60s.
4. At minute 30, enroll a new public key, atomically replace its complete
   key/ID record, observe both relevant replicas renew, then remove the old key
   after overlap. Run both authorized and wrong-subject issuance tests.
5. At minute 45, invoke planned
   `make chaos CHAOS_PROFILE=controller-restart CHAOS_TARGET=<explicit-container> CHAOS_CONFIRM=1`.
   Require changed process identity, fresh bootstrap authentication, and resumed
   reconciliation <=60s. Keep existing API replacement/overflow checks too.
6. Run the documented preparation/strict rolling API overlap with migrated
   manifests and no in-flight File-spec writes; no projection field loss.

Add `tests/chaos/profiles/controller-restart.json` with existing `restart`
action and 60s recovery objective. Provider record outage is a domain fixture,
not a fabricated network fault or unsupported Pumba action. Fault injection
success alone is never a pass.

Store sanitized counts, timings, provider categories/reasons, replica/build
identities, migration gate digest, workload configuration, fault/recovery
markers and correctness results under the existing evidence directory.
Do not store private records, token responses, assertions, raw environment or
full config dumps. Reuse token-file handling and credential scanning.

Diagnostic evidence cannot pass; alpha cannot claim the five-million-row,
60-minute production gate. Non-diagnostic runs require the existing clean
checkout, release-image, topology and datastore health evidence plus the new
secret-scenario fields. Missing fields/fault proofs fail closed.
