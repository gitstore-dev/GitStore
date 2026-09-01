# GitStore chaos profiles

Pumba provides repository-standard fault injection for Docker/Podman-based
capacity environments. The wrapper is deliberately opt-in and narrowly scoped:

```bash
make chaos CHAOS_PROFILE=api-restart \
  CHAOS_TARGET=gitstore-capacity-api-a \
  CHAOS_CONFIRM=1
```

Only an explicit existing container beginning with `gitstore-` may be targeted.
Every run records metadata, logs, and before/after container inspection under
`.gitstore/chaos/`. Profiles must describe one bounded fault and its expected
recovery; the capacity verifier—not Pumba—decides whether service correctness
and the recovery objective passed.

Lifecycle faults are enabled first. Network and resource faults require a new
reviewed profile, a proven rollback on Docker Desktop and Linux, and a verifier
that distinguishes intended unavailability from data loss or split brain.
