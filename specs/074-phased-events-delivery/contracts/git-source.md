# #139 Git Source and API Acceptance (Proposed)

Add the following methods to the **existing** gitstore.git.v1.GitService, using [message additions](git-notifications.proto). No second Git service or WebSocket control-plane channel:

```proto
rpc WatchGitEvents(WatchGitEventsRequest) returns (stream GitEvent);
rpc ListRepositoryRefs(ListRepositoryRefsRequest) returns (ListRepositoryRefsResponse);
```

Both require existing receiver/method/body-bound signed requests, canonical repository authorization and repository UID field 15/authorization field 14. New generated action `repository.events.watch` authorizes notification reads; existing repository.contents.read authorizes ref listing. These are service-consumption operations, not public raw Git source history in Storefront. Never forward a user's bearer token or trust author/committer text as actor evidence.

## Source facts

Initial stream emits only **actual committed ref transitions**, one per changed ref. Absence of old/new OID represents creation/deletion; normalize Git wire zero IDs to absence. Both absent and unchanged old==new are invalid transition records. OIDs are complete IDs in the repository's supported object format; annotated tags retain actual ref target IDs, not silently peeled commit IDs. v1 does not claim new hash-format support just by carrying a format field. Update does not by itself mean non-fast-forward, published release or successful admission.

occurred_at_unix_millis is the persisted source observation/intent time, not a fabricated exact commit instant when publication is recovered; keep that time across replay. Mutation ID groups one supported write operation and its stable per-ref index. It does not assert atomic multi-ref publication. Source position binds repository UID, persistent source epoch and sequence; opaque cursor is a versioned encoding of the same fields and must match them. Bookmarks do not create integration facts. Rejected pushes are audit/report outcomes; symbolic/default-branch changes need an explicit later variant, not fictitious old/new commit IDs. Both WatchGitEvents and ListRepositoryRefs explicitly exclude HEAD/symbolic refs and reserved refs/gitstore plus all descendants, matching current advertisement/write protection. Internal receipt-marker creation/deletion never becomes a user transition or bootstrap item. Current #139 issue's broader candidate list is narrowed accordingly for v1.

## Private retained log

Use existing prost/SHA-256/filesystem primitives for length-framed versioned segments with checksums, mutation intents, completion batches and an atomically replaced/fsynced manifest. Store under retained `<data_root>/.gitstore/git-events/<repositoryUID>/`, outside removable bare repository directories. No secrets/pack contents; only bounded ref/source/correlation facts. Default completed retention 24 h; max segment 16 MiB, max intent 1 MiB/1,024 refs, max fact 8 KiB, global source budget 64 GiB plus configured per-repository budget. Reserve capacity before effects; fail RESOURCE_EXHAUSTED before modifying refs if it cannot fit. Runtime policy must account for all intent/completion records, not only wire facts.

Torn/incomplete unpublished tail may be repaired only to the last verified boundary; never truncate acknowledged completed history silently. Checksum failure in published history sets SourceCorrupt/unready and requires explicit recovery/epoch handling. Compaction excludes unresolved intents and keeps a durable retained floor. Repository deletion retains closed-source/tombstone/history until source policy expires; same-name recreation gets another UID/epoch. Restore requires coherent repository/log state; mismatched backups, out-of-band writers and media loss are explicit unsupported/uncertain states, not inferred continuity.

## Mutation and crash protocol

Every supported writer—ReceivePack, legacy CommitFile, CommitFileAtRevision, DeleteFile and CreateTag—uses the same bounded per-repository mutation gate and OS marker lock. DeleteRepository enters this gate too: resolve unfinished intents, durably close source/tombstone and accepted-consumer obligations before removing refs needed for recovery. It is not a synthetic ref-change batch. Retained closed-source replay by repository UID uses a separate cluster-scoped service-only gitSource.replay authority, current authenticated recovery identity and retained original scope/grant, not a live repository/name lookup. Retained context constrains recovery data; it is not a continuing authorization grant. Current cluster recovery authority permits required work recovery, not revoked subscriber export. Ordinary users cannot use it to bypass deletion/revocation. Source obligations are finalized or explicitly cancelled under policy before retention cleanup. Existing guards/anti-ABA receipt checks remain in force; particular receipts are not generic source commits. Pre-receive validation may run outside the short publication gate, but all expected ref states are revalidated before intent/publication.

1. Resolve unfinished prior intents and current authoritative ref state; deny new publication if unready.
2. Validate authorization/preconditions/size, reserve source capacity and ensure referenced objects are available under the supported durability model.
3. Append/sync exact intent: stable mutation ID, expected/proposed state per ref and correlation, before the first ref effect.
4. Publish through existing gix expected-value writes. Multi-ref commit may partially publish on process loss; record actual successful/failed ref outcomes, not planned outcomes.
5. Append a complete verified completion batch and sync before exposing its contiguous sequences or acknowledging ordinary success. Assign public sequences only to committed facts in completed batches, so aborted intent reservations do not fabricate gaps. Batch visibility requires a complete verified commit frame/manifest boundary.
6. Invoke/enqueue post-publication processing through the shared accepted-work identity. Failure after refs commit returns recoverable uncertainty and cannot claim the refs were rejected.

Recovery blocks later writers for that repository. Under exclusive supported writers and a retained unresolved intent, actual==proposed can prove surviving publication; actual==expected can prove no surviving publication only under those assumptions. A third value, corrupt object/ref or ambiguous restore is UNCERTAIN/unready, not fabricated success. Recover partial outcomes per ref. A -> B -> A is retained because the second publication cannot pass the gate until the first completion is durable. Preserve existing guarded-receipt anti-ABA logic; never replay an old command after a later force reset merely because a receipt ref exists.

Initial qualification covers process crash/replacement on healthy retained storage. Full host/power-loss durability additionally requires verified object/ref/directory sync, including native quarantine promotion, not just log fsync. Until that proof exists, do not claim that larger fault model; storage loss/rollback produces explicit continuity failure.

## Stream, bootstrap and gap contract

`after` resumes strictly after its position. Omitted after starts at captured high water and immediately emits a BOOKMARK; it is not a historical snapshot. Initial consumer captures bookmark **before** bounded ref enumeration, then replays from it. ListRepositoryRefs is lexical ref-name keyset pagination, 1–100/default100, current state rather than an immutable snapshot; concurrent changes are covered by replay. Never use potentially unbounded InfoRefs bytes as the production initialization list. Namespace/repository resource watches schedule monitor creation/deletion through existing management inventory.

Read batches <=128 facts/1 MiB; subscriber buffer64, bookmark <=2 s, stream lifetime <=30 s followed by a fresh signed/authorized reconnect, <=64 concurrent Git streams. After overflow/unavailability close explicitly; resume from persisted acceptance, not last received message. Internal source permission revocation is bounded by stream lifetime; newly authenticated requests check current authority. Token expiry/transport failures are not silently retried forever without reauthentication.

Qualification uses <=16 active repositories across the two API consumers. A bounded source scheduler rotates idle repository monitors and persists per-source positions; deployment admission must ensure its worst-case revisit/processing lag fits configured source retention and advertised freshness. No claim of unbounded active repository coverage from 64 streams. Larger catalogues need measured pool/revisit configuration or explicit unsupported readiness, not process-local lost hints.

Errors use gRPC status plus bounded `gitstore-source-reason` trailer: INVALID_ARGUMENT for malformed cursor/request, OUT_OF_RANGE/SOURCE_EXPIRED for retained-floor gaps, FAILED_PRECONDITION/EPOCH_MISMATCH or SOURCE_UNCERTAIN, UNIMPLEMENTED for unsupported source major, RESOURCE_EXHAUSTED for finite budgets, existing authorization codes without leaked ref state. Authenticate/authorize before disclosing bounds. Never reset after to head on a gap. Public source-sequence ordering is per repository/epoch; no cross-repository total order.

## API durable acceptance and #501 handoff

Two APIs may consume/replay the same source. A shared API source-control row/version and deterministic `(repositoryUID,epoch,sequence)` acceptance key protect work insertion/checkpoint. Persist immutable input and enumerable work/routing recovery intent before source cursor advancement. Current admission callback and new source consumer must meet at one idempotent command identity; do not execute two independent admissions or bypass current validation.

#501 owns accepted admission command/report execution and client observation; this plan supplies the source-to-accepted-work handoff and tests it. If that command/recovery interface is not implemented, enabling source-driven admission is blocked rather than pretending a detached task is durable. Diagnostics, reports and audit can correlate to mutation/operation IDs but are not this queue. A controller may coalesce current-state reconciliation; an occurrence-sensitive Git integration must retain every eligible source fact within the supported window.

Gaps persist independent API degraded coverage records. Ref listing can repair current-state convergence only. It cannot recover a lost tag occurrence or claim historical notification completeness. Git-derived outbox remains unavailable until its source/acceptance contract passes crash/replay tests; resource-derived outbox and other #502 phases remain independent.
