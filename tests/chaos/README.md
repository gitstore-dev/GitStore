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

For an identity-service restart, select one existing Kratos container and
explicitly match its ownership label:

```bash
make chaos CHAOS_PROFILE=identity-restart \
  CHAOS_TARGET=gitstore-oidc-kratos \
  CHAOS_OWNER=your-owned-compose-project CHAOS_CONFIRM=1
```

The default ownership label is `com.docker.compose.project`. An owned integration
fixture may select `CHAOS_OWNERSHIP_LABEL=dev.gitstore.test_owner` and its private
owner value instead. Unowned, stopped, paused or non-identity targets are
rejected. The wrapper preserves the container/storage, rechecks its immutable
ID and ownership before restoration, and records whether the process is running
and unpaused after injection. A failed injector still fails the run even when
the process is restored.

`restored` is a process receipt, not an authentication pass. Run under the
bounded identity capacity workload; its verifier must establish existing-profile
authentication recovery on both APIs within 30 seconds of dependency readiness,
while keeping shared identity storage and the one Git process unchanged.
Missed workload mirror events may remain discarded indefinitely; no orphan
repair or replay guarantee is part of this fault.

## Owned RBAC fault windows

Select `CHAOS_RBAC=1` with `api-restart`, `controller-restart` or
`journal-interruption`. The target must match its role and ownership label,
already be running and unpaused, and have a process epoch. Declare Unix
milliseconds in `CHAOS_WINDOW_START_MILLIS` and `CHAOS_WINDOW_END_MILLIS`;
the current time must be inside that window, which cannot exceed 30 seconds.
The injector is bounded to 20 seconds and reserves restoration time.

Controller restart additionally requires `CHAOS_PEER_TARGET` naming a distinct
owned running controller. Both must have non-overlapping writable checkpoint
mounts at `/var/lib/gitstore/checkpoints`, or the explicit
`CHAOS_CHECKPOINT_DESTINATION`. The peer's identity, epoch and checkpoint mount
must remain unchanged. These profiles never target or overlap singleton Git
replacement.

`journal-interruption` pauses a journal-source container for six seconds by
default. `CHAOS_DURATION=2500ms` selects a below-five-second interruption;
durations cannot exceed 20 seconds or exhaust the declared restoration window.
This is a shared-source outage affecting every consumer, not selective
receiver packet loss or cursor-loss/resync proof.

The wrapper targets immutable container IDs, observes pause state or a changed
restart epoch, and attempts owned-only restoration on injector failure, timeout,
SIGINT or SIGTERM. Run directories cannot overwrite prior evidence. Receipts
separate partial observed faults, successful injection, process restoration and
confirmed windows; `domainRecoveryVerified` remains false. The active workload
oracle must independently establish correct decisions, bounded freshness and
recovery on every affected API. These lifecycle receipts are not sustained
capacity acceptance.

Lifecycle faults are enabled first. Network and resource faults require a new
reviewed profile, a proven rollback on Docker Desktop and Linux, and a verifier
that distinguishes intended unavailability from data loss or split brain.
