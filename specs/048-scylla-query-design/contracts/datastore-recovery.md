# Contract: Datastore Mutation Recovery

## Error outcomes

- `ErrAlreadyExists`: a unique reservation is owned by another stable identity.
- `ErrConflict`: the expected resource version or prior mapping no longer matches.
- `ErrNotFound`: the authoritative resource does not exist.
- `RepairRequired`: the primary operation failed and compensation could not restore a known-valid state.

`RepairRequired` MUST retain the original failure and the failed compensation step in its error context.

## Create contract

1. Unique keys are conditionally reserved.
2. Repeating the same create identity is idempotent and may finish missing projections.
3. A competing identity never overwrites an existing reservation.
4. Projection writes are individually observable.
5. Failed creates compensate only rows owned by the creating identity.
6. Namespace and Repository create operations persist the parsed Markdown body before reporting success.
7. Create writes the complete canonical resource envelope to the authoritative row before reporting success.

## Update contract

1. The authoritative resource-version conditional update happens before projection changes.
2. No projection changes occur after a resource-version conflict.
3. Projection writes set absolute values and are safe to retry.
4. Failed projection updates retry and converge on the committed authoritative version.
5. If convergence cannot complete, `RepairRequired` reports that the authoritative update committed and identifies every unresolved projection.
6. Namespace and Repository body updates participate in the same authoritative resource-version transition as spec updates.
7. Update preserves all canonical envelope fields not changed by the admitted manifest or system-owned status operation.

## Delete contract

1. Projection rows are removed before the authoritative row.
2. The authoritative row is deleted conditionally using expected version/identity.
3. A retry of a completed delete is idempotent.
4. Failure before authoritative deletion restores removed projections.

## Repository rename/transfer contract

1. Target path reservation occurs before authoritative ownership/name change.
2. Target conflict leaves the current path unchanged.
3. Authoritative update failure releases only the target reservation owned by the Repository.
4. Old mapping deletion is conditional on the stable Repository UID.
5. Retry converges to exactly one active path.

## Failure-injection contract

Tests MUST inject failure after every mutation step and assert one of:

- desired final state,
- exact prior state restored, or
- `RepairRequired` with an operator-visible consistency signal.
