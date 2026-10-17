# GitStore chaos profiles

Pumba provides repository-standard fault injection for Docker/Podman-based
capacity environments. The wrapper is deliberately opt-in and narrowly scoped:

```bash
make chaos CHAOS_PROFILE=api-restart \
  CHAOS_TARGET=gitstore-capacity-api-a \
  CHAOS_CONFIRM=1
```

Only one explicit existing container may be targeted. Without a project
selection, its name must begin with `gitstore-`. Set `CHAOS_COMPOSE_PROJECT` to
an explicitly owned Compose project to allow its project-scoped names; the
target's `com.docker.compose.project` label must match before injection.
The integrated capacity runner forwards `COMPOSE_PROJECT_NAME` for this check.
Every run records metadata, logs, and before/after container inspection under
`.gitstore/chaos/`. Profiles must describe one bounded fault and its expected
recovery; the capacity verifier—not Pumba—decides whether service correctness
and the recovery objective passed.
An existing profile/run directory is never reused; choose a fresh `CHAOS_RUN_ID`
for retries so failed evidence remains intact.

API restart/pause profiles verify `/ready` inside the exact target container
within their unchanged 30-second budget. The controller restart profile verifies
`/health` within its declared 60-second budget, including credential readiness,
registered-kind recovery and absence of stalled or poisoned work. The clock is
monotonic and starts before invoking Pumba, not after it exits. The target must
be ready before injection. Both probes require the same executable hash,
container/image, allocations and mounts afterward; restart must also change the
container's start timestamp. API authentication providers, authorization provider,
catalog digest and policy snapshot must remain identical.

The standard internal ports are 4000 and 5001. For a deployment configured with
another port, set `CHAOS_READINESS_URL` (or `CAPACITY_CHAOS_READINESS_URL` for an
integrated run) to the corresponding credential-free container-loopback URL,
with the same `/ready` or `/health` path. No external endpoint or credentials are
accepted. Sanitized readiness snapshots and bounded observation timestamps are
retained alongside injection metadata. `injectorExitCode` distinguishes Pumba
success from a failed `recovery`; either failure makes the wrapper fail.
These checks do not substitute for peer semantic probes, durable-watch recovery,
deletion safety or the workload's domain and capacity verifier. Profiles without
a declared readiness probe explicitly record recovery as unmeasured.

Lifecycle faults are enabled first. Network and resource faults require a new
reviewed profile, a proven rollback on Docker Desktop and Linux, and a verifier
that distinguishes intended unavailability from data loss or split brain.

Integrated readiness, Namespace admission and Category hierarchy faults wait
for a run-matched sustained load-window event, not a timer started during setup.
Injection must begin and finish inside that declared window. Missing or expired
windows fail without injection; successful injection alone is still not a
recovery or capacity pass.

## Invalid-policy reload

`api-invalid-policy-reload` uses Docker `SIGHUP`, not Pumba. It exercises only
malformed-policy rejection on one API using `rbac-local`; it is not the
maximum-policy reload soak or a multi-replica authorization correctness proof.

Provision a disposable owned deployment with one private policy directory per
replica, **outside the repository**, containing only `policy.yaml` and
`owned-policy.json`. The invoking user must own the directory and marker;
neither the directory nor policy may be group/world writable. Symlinked paths
are rejected. Mount the directory read-only in the API and explicitly set
`GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE` to its `policy.yaml`. Do not use an
operator deployment, a shared policy directory, or a single-file bind mount.
After creating the container, provision its marker explicitly:

```json
{
  "schemaVersion": 1,
  "composeProject": "owned-project",
  "containerId": "<full exact container ID>",
  "purpose": "policy-reload"
}
```

```bash
make chaos CHAOS_PROFILE=api-invalid-policy-reload \
  CHAOS_TARGET=gitstore-capacity-api-a CHAOS_COMPOSE_PROJECT=owned-project \
  CHAOS_POLICY_DIR=/absolute/private/api-a-policy CHAOS_CONFIRM=1
```

The runner checks the marker, explicit policy-file environment value and
read-only directory bind before modifying anything. It atomically installs a
bounded malformed candidate, verifies the mounted hash, signals the exact
container and requires its native JSON registry rejection. Candidate compilation
logs alone do not count. Active provider/catalog/policy metadata must stay
unchanged. It restores the original bytes, signals again, requires native atomic
publication and rechecks the original snapshot and retained process/settings.
Matching native outcomes and their hashes are retained; arbitrary request logs
and environment secrets are not copied into evidence.

Concurrent runner use of the same directory is rejected. Do not independently
edit the fixture or reload its API during the probe. Ordinary failures and
handled interruption attempt restoration with a separate bounded cleanup;
cleanup never extends the original 30-second acceptance budget. A failed cleanup
is reported as `policyReload.restored:false` and the run fails; inspect the
fixture and active snapshot before reusing that deployment. Forced termination
or host failure cannot guarantee rollback. Evidence reuse is always rejected.

For integrated load, use `CAPACITY_CHAOS_PROFILE=api-invalid-policy-reload` and
`CAPACITY_CHAOS_POLICY_DIR=/absolute/private/api-a-policy` with the existing
target, confirmation and delay flags. The k6 profiles declaring sustained
windows and the Go Repository lifecycle runner use the same load-window
scheduler. Other Go profiles reject this option, as does the complete secret
scenario with its independent managed fault schedule. A configured fault that
never starts or finishes outside the window fails the overall run; it does not
replace workload thresholds, the lifecycle API replacement or domain checks.
