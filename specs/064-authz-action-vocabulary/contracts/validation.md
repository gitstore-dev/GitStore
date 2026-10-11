# Contract: validation and evidence

## Test-first matrix

Before each implementation slice, add the smallest relevant tests and record
their expected failure. Keep tests in the current package/integration suites.

| Slice               | Required evidence                                                                                                                                                                                                |
|---------------------|------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| US1 policy          | Neutral JSON decision/validation rows; strict YAML negative cases and every bound; defaultDeny=false rejection; deterministic reasons; concurrent valid/invalid reload under Go race detector                    |
| US2 vocabulary      | Catalog generation drift; real recording-authorizer exercise of every protected operation and node/nodes route; Go/Rust signed-action parity; canonical Kind; all four purges independent of delete/status.write |
| US3 conditions      | Four attribute operators including missing/wrong types; User/Group/SA ownership; transfer overrides and concurrent owner change; provider-independent concealment; no list filtering                             |
| Identity foundation | All AuthN providers' reserved-subject rejection, trusted system groups, SA proof/replay/UID/key checks; no roles in signed envelopes; explicit internal identity authorization                                   |
| Namespace admission | GraphQL and Git preflight/apply parity, USER invariants, tier immutability, fresh default bootstrap, two-API concurrent create and transfer/write races                                                         |
| SA persistence      | Backend-neutral and Scylla contracts; two-API same-name and same-UID/different-name creation; partial enrollment/projection writes/repair; disable/delete/recreate/key race; replacement baseline defines no legacy SA tables; legacy tokens rejected         |
| Controller recovery | HTTP 401, pre-execution GraphQL UNAUTHENTICATED, WS auth rejection; coalesced exchange; stale rejection after refresh; no retry on forbidden/ambiguous mutation                                                  |
| US4 tools           | Cluster/namespaced roles and bindings, exact projection without overgrant, atomic failure/idempotence, DENY rejection, removed namespace config rejection; every local active user's effective grant             |
| Operations          | Baseline-to-v1beta1 access matrix, valid/invalid reload, same-version replica replacement, legacy-input rejection, measured latency and sustained lifecycle load                                         |

Negative fixtures and historical migration documentation necessarily contain
legacy strings. SC-001's zero-legacy check applies to emitted/accepted active
actions, positive policy/config fixtures and current usage examples; keep
explicit negative-test and migration-diagnostic exceptions narrow and reviewed.
SC-002 removes caller authorization comparisons, not stale-owner CAS checks or
structural USER-namespace invariants.

### Shared fixture adapter

`tests/fixtures/authorization/decision-table.json` is the promoted, byte-identical
T003 fixture. A provider adapter loads the policy referenced by each case,
converts its already-trusted principal/resource facts without inventing roles or
groups, and compares the decision outcome plus a nonempty diagnostic reason.
RBAC allows must identify the binding, role and rule (masters is the explicit
non-rule exception). Ignore timestamps and provider-specific prose.
Validation mutations each apply to a fresh `policies.base` JSON tree; raw YAML
cases exercise the parser separately. Response expectations are boundary tests,
not evaluator outcomes: US1 exercises all 55 decisions and 28 validation rows;
US3 must exercise every response field against the real presentation mapper.
Future providers consume this same fixture unchanged. New cases append rather
than weakening expectations; a parity check protects the reviewed planning seed.

### Initial observed baseline (T002)

The existing integration harness uses a permissive `namespaceOwnerAuthZ` test
provider, not the shipped RBAC policy. Its successful controller reads are not
evidence of shipped least privilege or anonymous access. The provider-level
`TestAuthorizationBaselineShippedPolicy` uses the real `config/policy.yaml`,
without injected Principal.Roles: admin permits organization create/delete;
the bound controller permits list/watch, four purges and token issue but denies
organization creation; unbound users and anonymous contents reads deny even
with synthetic public/private visibility. The internal partial wildcard grants
nothing, although the existing Git envelope helper bypasses policy entirely.
USER creation currently bypasses the evaluator; its new username-constrained
namespace-creator grant is an explicit feature change, not baseline parity.

## Decision performance

Use 200 total roles, 1,000 total bindings, 2,000 total rules and 50 groups per
principal. Cover early allow, last-candidate allow, no binding, worst candidate
deny, owner/transfer and missing attributes. Build subject indexes before timing;
never count YAML loading as a decision. Include adversarial overlap where many
groups reach the same bindings and no grant permits early return.

Run 60-second warm-up, then 10 minutes at concurrency 1, 8 and 32 separately.
Record a bounded latency histogram/reservoir with sample count, p50/p95/p99/max,
allocations and machine/Go build metadata; p99 must be strictly below 1 ms in
each run. Mean ns/op alone does not pass. Track throughput without imposing an
invented service-wide throughput promise; report achieved decisions/s at each
concurrency. Assert zero evaluator datastore/network calls with failing spies.
During concurrent reload, reader latency still meets the bound and every result
belongs wholly to the old or new policy. Reject oversized policy/principal
inputs deterministically and retain the old snapshot after invalid reload.

Capture a 30-minute same-policy reload/load soak after warm-up; no growing queue
or retained snapshot count and no sustained post-GC heap growth above 10% of the
stabilized baseline with identical workload. Record CPU/RSS and declared
container limits; memory-limit breach or OOM is a failure. These are new
authorization-specific gates, not evidence already collected by planning.

## Existing deployed gates

Use the root interface, preserving existing gates:

```sh
make capacity TARGET=api PROFILE=readiness MODE=alpha
make capacity TARGET=repository PROFILE=lifecycle MODE=alpha
make chaos CHAOS_PROFILE=api-restart CHAOS_TARGET=<owned-api-container> CHAOS_CONFIRM=1
```

Readiness needs the existing config/environment manifests, verified distinct
API endpoints/containers, release build metadata and declared runtime limits;
the commands above are not sufficient without those inputs. Alpha supports
exact-revision local release images where the existing preflight permits them;
production uses digest-pinned artifacts. Use a clean committed implementation
checkout so evidence identifies the tested revision.

Readiness currently measures only `/health` and `/ready`. Extend that profile's
existing helper/harness with bounded authorization probes and a decision-matrix
verifier. Retain p95 <1s, p99 <3s, HTTP failure rate <0.001, all checks passing and
zero dropped iterations on readiness operations. Run 10 requests/s for 10
minutes per API with existing 10 preallocated / 100 maximum VU bounds. Record
negative authorization probes separately from readiness HTTP failures; expected
denials pass only when the exact decision/error class matches.

Repository lifecycle reuses `scripts/repository-capacity-stack.sh`,
`compose.capacity.yml`, existing integration evidence gate and verifiers. The
managed alpha run has two APIs, two active controllers, exactly one Git process
and three Scylla nodes. Keep its existing effective defaults (currently 10-minute
load, 100 subscribers, 1,000 replay events, five replay samples, 20 resource pool,
256 overflow transitions, 31-second overflow wait, 20-event bursts each minute,
500ms transition interval and API replacement after five minutes). Record the
resolved settings; do not substitute root production-oriented defaults silently.
Pin payload manifests and measure Git delegating mutation/admission, watch,
replay and controller convergence outcomes. No unexpected FORBIDDEN/NOT_FOUND,
lost mutations, unauthorized writes or stalled controllers are allowed.

Retain lifecycle alpha watch visibility p95 <=2s (warn above 1s), production
p95 <=1s/p99 <=3s and all existing latency/correctness thresholds. Both controller
ready states and their list/watch recovery must pass before load. Add a
malformed policy reload during sustained requests; decisions on that replica
must remain unchanged. Same-policy restart of one API must restore identical
decisions within the existing api-restart profile's 30-second deadline. Fault
injection command success alone is not evidence of recovery.

## Production envelope and limitations

The evaluator operates only on bounded policy/principal/context facts and does
not enumerate the 5,000,000-product catalog. Preserve that property with spies
and query-count tests. Before any production-capacity claim for affected API/
Git/admission paths, run the existing readiness and repository lifecycle
production profiles against an acknowledged 5,000,000-Product dataset on the
shared Scylla deployment, at least two APIs/two controllers and one retained-
storage Git process. Use at least 60 minutes of sustained lifecycle load with
the existing 20 mutation-worker/1,000-subscriber production envelope, real
manifest pushes (record operation mix, payload bytes, offered/completed rate),
overflow and process replacement. Verify dataset count/digest with bounded
pages outside the request path; no resolver aggregates or full request scans.
Document any environment-specific limits in the evidence manifest before load.

The alpha profiles and local policy benchmarks do not prove this production
envelope. rbac-local remains development/evaluation only; production-engine
translation and distribution are spec 066. No Git replication, overlapping Git
replacement or zero-downtime cross-format migration is tested or claimed.

## Evidence bundle

Extend the existing capacity bundle with catalog version/digest, policy digest,
neutral decision-table revision, per-replica semantic decision matrix, sampled
decision latency, input-policy dimensions, actual workload settings, sanitized
enrollment UIDs/key fingerprints, process identities and reload/recovery
timeline. Keep expected legacy-input denials separate from healthy
traffic and baseline parity evidence. Do not retain tokens, assertions, private
keys or sensitive claim values. Include verifier results, not only tool status.

Existing structured authorization logs retain request/operation/action/kind,
outcome, provider and bounded reason. Add policy digest and matched binding/role/
rule diagnostics. Reload success/failure and controller credential invalidation/
renewal need structured events; metrics use bounded action/outcome/provider labels,
not subject, namespace, policy digest or arbitrary role names. Scrape both APIs
and controllers with the existing capacity Prometheus integration.
