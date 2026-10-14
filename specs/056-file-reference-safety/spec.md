# Feature Specification: File Reference Safety and Renewable Source Access

**Feature Branch**: `056-file-reference-safety`

**Created**: 2026-08-20

**Updated**: 2026-10-09

**Status**: Draft

**Input**: Preserve File reference resolution and non-blocking deletion safety from #378. Expand spec 056 into the first production resource-runtime consumer of spec 063, with continued authorized source access across credential expiry, rotation, provider outages, and process replacement. Explicitly consider ADR 0001's SecretBinding/SecretClaim concepts.

**Related**: [#378](https://github.com/gitstore-dev/GitStore/issues/378), [spec 051](../051-file-resource-contract/spec.md), [spec 052](../052-categorytaxonomy-deletion-semantics/spec.md), [spec 063](../063-implement-secret-adrs/spec.md), [ADR 0001](../../docs/ADRs/0001-secretref-reference-contract.md), [ADR 0009](../../docs/ADRs/0009-credential-secret-boundary.md), [File/media architecture](../../docs/implementation/034-file-media-lifecycle-architecture.md).

## Clarifications

### Session 2026-08-20 - Retained decisions

- File deletion decouples every referencing Product, ProductVariant, CategoryTaxonomy, and Collection. A reference never blocks File deletion, regardless of its `optional` flag. A required reference gates its dependent's readiness; it does not make the File undeletable.
- Resolved references use system-owned `ownerReferences` targeting the File UID with `blockOwnerDeletion: false`. Reverse lookup reuses the established owner-reference mechanism, not a namespace-wide scan of authored reference names.
- Required references to a deleted File report a distinct deletion reason; optional references are omitted from resolved media without a reference condition or readiness penalty. Authored references remain unchanged.
- A reference to a File that never resolved remains a normal asynchronous not-found case, not an admission rejection or evidence that this particular reference experienced a deletion.

### Session 2026-10-09 - Scope expansion and current baseline

- Update the existing 056 branch with its remote changes and current `origin/main` before revising this specification. The August assumptions that File schema and owner-reference infrastructure have not landed are obsolete; reuse the current baseline.
- Spec 056 now owns production File source access and verification using spec 063's runtime acquisition boundary, in addition to the original reference-safety work. The controller's existing bootstrap identity integration is a separate consumer, not a substitute for File runtime integration.
- The user selected **AWS S3 with renewable role/workload credentials, Backblaze B2 S3-compatible application-key rotation, and Backblaze B2 Native API token renewal** as required production acceptance paths.
- Stable authorization is distinct from ephemeral credential bytes. Explicitly evaluate an owned `SecretBinding` against deployment-managed bindings, and evaluate a `SecretClaim` only for genuine provisioning/management needs. Neither resource is pre-approved merely because it is named in ADR 0001; neither eliminates token renewal.
- Existing external credentials or a workload trust relationship may be provisioned outside GitStore. Continued access must not require a new File commit or process restart on every credential/token expiry.
- This revision supersedes the old exclusions of source access and checksum verification, and the assumption that some unspecified future File controller supplies reference resolution. It does not add general binary upload, transformation pipelines, or automatic deletion of external payloads.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Record resolved File references safely (Priority: P1)

A catalog author references a File from a Product, ProductVariant, CategoryTaxonomy, or Collection. Once the File is usable, the dependent records its resolved identity and availability without duplicating or authoring system-owned relationships.

**Why this priority**: Reference identity is the foundation for accurate media readiness and targeted change propagation.

**Independent Test**: Resolve multiple required and optional File references for each of the four dependent kinds, then retry reconciliation and remove one reference.

**Acceptance Scenarios**:

1. **Given** a reference to an existing ready File, **When** it resolves, **Then** the dependent records one File-targeting owner reference containing its UID and `blockOwnerDeletion: false`; a required reference reports `FileRefConfirmed=True`.
2. **Given** multiple entries naming the same File, **When** they resolve, **Then** each media entry is evaluated but only one owner reference exists for that File UID.
3. **Given** references to multiple Files, **When** one cannot resolve, **Then** unrelated entries can still resolve; the unresolved entry acquires no owner reference.
4. **Given** an optional reference, **When** it resolves, **Then** it records the same non-blocking ownership and resolved output without emitting a required-reference condition.
5. **Given** an edited or removed authored reference, **When** reconciliation completes, **Then** the old ownership is removed only if no remaining entry needs it; unrelated relationships remain intact.

---

### User Story 2 - Find and refresh only affected dependents (Priority: P1)

An authorized operator or File controller can identify the resources related to one File and propagate availability changes without scanning unrelated catalog resources.

**Why this priority**: A catalog containing millions of resources cannot rely on whole-catalog scans for one File change.

**Independent Test**: Resolve a known set of dependents, query it in pages, change File readiness, and verify that affected resources converge while unrelated resources are unchanged.

**Acceptance Scenarios**:

1. **Given** a File with dependents across all four kinds, **When** reverse lookup is paged by File UID, **Then** it returns exactly the authorized dependent set.
2. **Given** a File with no dependents, **When** queried, **Then** the result is an empty page, not an error.
3. **Given** a previously ready File whose source access becomes unavailable, **When** it becomes not ready, **Then** affected required references cease claiming successful current resolution; optional entries are omitted without gating dependent readiness.
4. **Given** source access is restored without a File edit, **When** File readiness recovers, **Then** affected dependents recover their resolved output and conditions automatically.
5. **Given** a reference that has never resolved and therefore has no owner reference, **When** its File becomes available later, **Then** it eventually resolves through bounded retry or targeted unresolved-reference work, without a whole-catalog scan.

---

### User Story 3 - Delete a File without blocking on media consumers (Priority: P1)

An operator deletes a File. The deletion is not blocked by catalog references, and dependents visibly degrade instead of retaining stale media claims. Authored catalog content and external payloads are not silently rewritten or deleted.

**Why this priority**: This preserves the original #378 deletion-safety decision.

**Independent Test**: Delete Files with required and optional references, replace a controller during decoupling, and verify the final relationship and condition state.

**Acceptance Scenarios**:

1. **Given** any number of required or optional dependents, **When** File deletion is requested, **Then** none of those non-blocking references vetoes deletion.
2. **Given** a previously resolved required reference, **When** its File is deleted, **Then** it reports `FileRefConfirmed=False` with a distinct File-deleted reason, updates readiness, removes the matching owner reference, and preserves the authored `fileRef`.
3. **Given** an optional reference to the deleted File, **When** decoupled, **Then** its resolved output is omitted without a new reference condition or readiness penalty.
4. **Given** a deleted-File reason, **When** reconciliation or process replacement repeats, **Then** the reason persists until an author edits that reference; a same-name replacement File does not silently rebind it.
5. **Given** a reference that never resolved, **When** its named File is absent, **Then** the result is not-found rather than File-deleted, and admission does not reject that reference solely for absence.
6. **Given** interrupted deletion fan-out, **When** work resumes after the File row is gone, **Then** the affected UID and remaining work are still recoverable and all dependents converge.
7. **Given** source work is active when deletion starts, **When** stale source results finish, **Then** they cannot restore File readiness, resurrect the File, or reattach decoupled dependents.

---

### User Story 4 - Access private File sources without placing secrets in Git (Priority: P1)

An author supplies a File source and portable credential reference. An operator authorizes the File's use of a scoped credential or workload identity. The File controller obtains access and verifies the source without exposing credential material.

**Why this priority**: This turns spec 063's contract-only runtime boundary into useful production File behavior.

**Independent Test**: Admit Files for private AWS S3 and both selected B2 access modes, provision the required identity/material separately, and verify actual source reads and checksums under the correct tenant scope.

**Acceptance Scenarios**:

1. **Given** a well-formed File and authorized binding, **When** source reconciliation runs, **Then** the controller acquires credentials just in time, reads the permitted object, verifies its declared checksum when present, and reports truthful source/readiness state.
2. **Given** an accepted File whose material is not yet provisioned, **When** reconciliation runs, **Then** source access is blocked with a sanitized missing-material reason; provisioning it later permits automatic recovery without a File commit.
3. **Given** a File in another namespace/repository or naming an unapproved role, endpoint, bucket, or object scope, **When** it attempts to use a binding, **Then** no protected secret or storage operation is authorized by merely naming that binding.
4. **Given** a missing or invalid runtime binding, **When** acquisition fails, **Then** it does not fall back to the controller's bootstrap signing key, another tenant's credentials, or a broader ambient identity.
5. **Given** available credentials but a missing object or checksum mismatch, **When** source verification fails, **Then** status distinguishes source failure from credential acquisition success; obtaining credentials alone never makes the File ready.

---

### User Story 5 - Continue source access through expiration and rotation (Priority: P1)

An operator runs GitStore continuously. Files retain access through normal credential expiration and externally managed key rotation without edits to their desired state or recurring manual restarts.

**Why this priority**: A process-lifetime credential snapshot is not a production implementation for short-lived credentials.

**Independent Test**: Keep source reads active across real AWS session expiration, B2 application-key replacement, and B2 Native authorization-token expiration, using the same admitted File revisions.

**Acceptance Scenarios**:

1. **Given** an AWS workload identity authorized for a role, **When** its temporary credentials approach expiry, **Then** subsequent operations use refreshed access key, secret, and session token with the correct expiry and scope, without changing the File or restarting the controller.
2. **Given** B2 S3-compatible access through an application key, **When** an external provisioner installs a replacement key and retires the old one, **Then** access adopts the replacement under the same binding. The consumer does not assume B2 supports AWS STS.
3. **Given** B2 Native access, **When** an authorization token expires, **Then** the consumer obtains a new token and provider-returned routing information using the currently valid application key and continues authorized operations.
4. **Given** the B2 application key itself has expired or been revoked, **When** token acquisition fails, **Then** the consumer reports a blocked credential state until a replacement key is provisioned; it does not claim that reauthorization can renew an invalid underlying key.
5. **Given** several Files share one permitted binding, **When** renewal becomes due, **Then** renewal work is bounded and coordinated rather than multiplied by the number of Files, without sharing credentials across authorization scopes.
6. **Given** a long transfer or a sequence of source requests crossing credential expiry, **When** another external request or safe retry is needed, **Then** it acquires valid credentials and preserves source revision, checksum, and operation safety.
7. **Given** only secret/token material changes, **When** renewal succeeds, **Then** the File UID, desired state, and generation remain unchanged. Persisted status changes still advance resource version and are observable.

---

### User Story 6 - Recover safely and control ongoing authorization (Priority: P1)

An operator can revoke a binding, replace a controller, or recover from a credential/provider outage without losing accepted work, leaking secrets, or granting stale authority indefinitely.

**Why this priority**: Refreshable credentials require equally explicit failure, revocation, and recovery semantics.

**Independent Test**: Interrupt refresh beyond expiry, revoke a live binding, rotate keys during recovery, and replace one of two controllers while source and reference work continues.

**Acceptance Scenarios**:

1. **Given** refresh temporarily fails while the current credential remains valid and locally authorized, **When** retries occur, **Then** use is limited to its remaining validity and permitted operation budget; after expiry, new credentialed requests are blocked.
2. **Given** a binding is revoked or its scope narrows, **When** the change is enforced, **Then** cached credentials cannot authorize new out-of-scope requests even if the external token has not expired.
3. **Given** a binding is absent at startup, **When** an operator authorizes it while the system is running, **Then** pending Files recover without an application restart, File edit, or exposing the material through the catalog API.
4. **Given** a controller loses all process-local credential state, **When** a replacement starts, **Then** it reacquires its bootstrap identity and permitted runtime credentials separately and resumes pending work.
5. **Given** a dependency is restored after credentials expire, **When** reconciliation resumes, **Then** source and dependent readiness recover within the declared deadline without false success during the outage.
6. **Given** an operator investigates an incident, **When** reviewing status, audit records, logs, and metrics, **Then** they can distinguish binding denial, missing material, expiry, refresh failure, storage denial, source failure, and checksum mismatch without seeing secret bytes or bearer URLs.

### Edge Cases

- Initial unresolved references have no owner link; recovery must not depend exclusively on the resolved-owner reverse lookup.
- Temporary source failure does not mean File deletion. Existing identity links must remain useful for recovery fan-out and must not acquire a permanent File-deleted reason.
- Multiple media entries can share one File UID; removing one entry must not detach another entry's valid relationship.
- Same-name File or binding replacement must not silently transfer identity-bound authority or erase a persisted deletion reason.
- An unexpired provider token is not sufficient authorization after a local binding revocation.
- Expired B2 application keys require replacement; expired B2 Native tokens can be reacquired only with a valid underlying key.
- Invalid AWS session-token combinations, unknown expiry, clock skew, provider throttling, and credentials too near expiry for the next request must not become successful static-credential fallbacks.
- Source verification can race with File edits, binding changes, and deletion; stale results cannot restore readiness or old permissions.
- Already-dispatched external requests may complete after revocation; new requests and retries must enforce current local policy. Do not claim that GitStore can retract requests already accepted by an external service.
- A missing optional `fileRef` degrades presentation gracefully. A present but unresolved storage `credentialsRef` never authorizes anonymous fallback.
- Redirects and provider-returned endpoints must not leak credentials to unapproved hosts or private-network targets.
- An external rotation may change no catalog row; recovery and reload cannot depend solely on File generation or watch events.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Successfully resolved File relationships across Product, ProductVariant, CategoryTaxonomy, and Collection MUST record system-owned `{kind: File, name, uid, blockOwnerDeletion: false}` ownership. Required references MUST report `FileRefConfirmed=True`; optional references MUST NOT emit a required-reference condition.
- **FR-002**: Each dependent MUST have at most one owner reference per File UID, even when several media entries use it. Independent entries MUST resolve independently, and changing/removing one entry MUST preserve links still needed by another.
- **FR-003**: Reverse lookup MUST reuse the established owner-reference indexed query, be paginated and authorized, and scale with actual dependents rather than total catalog size. It MUST remain usable during interrupted deletion recovery.
- **FR-004**: File deletion MUST NOT be rejected or held pending because a catalog resource references the File, regardless of whether that reference is required. File-owned cleanup and durable handoff of decoupling work are separate from waiting for dependents to disappear.
- **FR-005**: Deletion MUST trigger durable, bounded decoupling work for the affected File UID. It MUST remove only that ownership and resolved media, preserve unrelated relationships, and remain recoverable after the authoritative File row is removed.
- **FR-006**: A previously resolved required reference to a deleted File MUST report `FileRefConfirmed=False` with a distinct File-deleted reason and update dependent readiness without editing the authored `fileRef`.
- **FR-007**: An optional reference to a deleted or currently unusable File MUST be omitted from resolved media without emitting a required-reference condition or independently reducing dependent readiness.
- **FR-008**: Required-reference readiness MUST retain the existing composition: a failed required check prevents ready, an unevaluated required check cannot count as success, and optional omission alone does not gate readiness.
- **FR-009**: The File-deleted reason MUST survive retries, controller replacement, and same-name File recreation until an author edits that reference. An old resolved UID MUST NOT be silently rebound to a new File.
- **FR-010**: A reference that has never resolved MUST NOT acquire an owner reference. If its File is absent, it MUST report the normal not-found result rather than the File-deleted result. Bounded recovery for such references MUST not require a previously existing owner link.
- **FR-011**: Admission MUST NOT reject a new or updated `fileRef` solely because its File does not exist or was deleted. Existing structural and namespace-isolation validation remains mandatory.
- **FR-012**: The File controller delivered by this feature MUST use targeted dependent work for readiness loss, recovery, and deletion. Temporary source failure MUST preserve resolved identity tracking sufficient for later recovery without masquerading as deletion.
- **FR-013**: This feature MUST deliver production File reconciliation for the three selected access paths: AWS S3 using renewable role/workload credentials, B2 S3-compatible access using replaceable application keys, and B2 Native access using renewable authorization tokens. A local contract resolver or admission-only check is insufficient acceptance evidence.
- **FR-014**: File source reconciliation MUST read the declared source and verify the declared checksum when present before claiming source readiness. It MUST preserve source identity/revision through retries, bound transfer work, and reject stale results after a File or authorization change. Acquisition success alone MUST NOT imply object access or content verification success.
- **FR-015**: The explicit `CredentialsRef`/`SecretRef` contract from spec 063 MUST remain the portable authored reference. Desired-state admission MUST validate reference structure without network secret acquisition; provisioning may occur before or after admission. Provider locations, role-selection authority, and secret bytes MUST NOT be supplied as unrestricted File-controlled overrides.
- **FR-016**: Each runtime credential use MUST have a stable authorization binding selected in a trusted environment and namespace, with permitted principals, consuming resources/repositories, operation/purpose, and external destination scope. Naming a reference or possessing a broad controller identity MUST NOT itself grant permission.
- **FR-017**: Bootstrap identity and resource-runtime acquisition MUST remain separate. Runtime consumers MUST NOT resolve bootstrap signing records or fall back to bootstrap keys, unrelated providers, broader ambient credentials, or unauthenticated access when their binding fails. Explicitly authorized workload identity is supported; accidental ambient authority is not.
- **FR-018**: Operators MUST be able to add, revoke, or update runtime authorization bindings and provision/replace material while GitStore runs, without editing each affected File or restarting application processes. Authorization updates MUST be attributable and applied to every relevant replica; active bindings must not depend on one replica's process-local state.
- **FR-019**: Credential acquisition MUST carry or otherwise reliably enforce expiration, token/session requirements, scope, and reacquisition behavior. Renewable temporary credentials MUST NOT be reduced to a process-lifetime static key snapshot. Unknown or invalid expiry for a declared temporary credential MUST fail closed. Non-expiring application keys must be classified explicitly rather than assigned invented lease behavior.
- **FR-020**: AWS access MUST support automatic reacquisition from an authorized workload/role identity, preserve access key/secret/session-token consistency, and refresh with enough validity for the next request accounting for clock skew and retry budget. A refreshable identity provider MUST remain refreshable throughout the source client's lifetime.
- **FR-021**: B2 S3-compatible access MUST adopt externally rotated application keys under the same authorized binding without assuming AWS STS semantics. B2 Native access MUST reacquire authorization tokens and valid provider routing information using the current application key. Expiration or revocation of that underlying key MUST require valid replacement material, not an endless claim of successful token renewal.
- **FR-022**: Provider-specific renewal and any future dynamic-secret lease renewal MUST obey the provider's actual contract, distinguishing renewal of a lease/token from issuing replacements and rotating its underlying authority. Other secret-store adapters are extension points, not implicitly supported production integrations.
- **FR-023**: Credential acquisition/refresh MUST use bounded concurrency, finite retries with backoff, and bounded ephemeral caches keyed by the full authorization context and policy/binding revision. Shared Files MUST reuse permitted acquisition work; distinct tenants or differently scoped principals MUST NOT share authority merely because a secret name matches. Each replica may independently acquire its own valid session where the provider permits it.
- **FR-024**: Refresh failure MAY permit continued use only while credentials remain unexpired, sufficiently valid for the next operation, and locally authorized. Once those conditions fail, new requests MUST stop with an explicit failure. Recovery MUST retry or reschedule without requiring a File edit, including when external rotation emits no GitStore event.
- **FR-025**: Long reads and multi-request operations MUST use valid credentials at every new external request. Credential refresh, authentication retries, source replacement, and cancellation MUST preserve content consistency and prevent duplicate destructive effects. A completed earlier read is not proof of continued future access.
- **FR-026**: Revocation or scope narrowing MUST invalidate local use of cached authority for new requests within the declared enforcement bound, regardless of external token expiry. File or binding deletion MUST cancel or fence pending source work and prevent stale readiness writes; already-dispatched external requests are not claimed retractable.
- **FR-027**: Secret bytes, private keys, access/session tokens, and bearer-bearing URLs MUST NOT enter Git, catalog desired state/status, checkpoints, durable caches, public responses, audit diffs, logs, or metrics labels. Credentials MAY live only in bounded ephemeral memory for permitted use; necessary private material remains in its authorized provider. Safe status MUST distinguish credential resolution, authorization, and source verification outcomes.
- **FR-028**: Credential/token refresh alone MUST NOT modify the File's desired state, UID, or generation. Status transitions MUST follow existing resource-version concurrency and durable observation contracts; equivalent no-op status writes MUST not manufacture new transitions.
- **FR-029**: Planning MUST explicitly compare ADR 0001's `SecretBinding` with a deployment-managed binding that provides equivalent live authorization, identity, audit, and revocation guarantees. It MUST separately evaluate whether `SecretClaim` is needed for requesting provisioning, rather than treating it as another name for a binding. The selected design, rejected alternatives, authorization model, ownership/deletion rules, and rollout contract MUST be recorded before implementation.
- **FR-030**: A binding or claim MUST never contain secret bytes. If a resource representation is selected, it MUST follow the appropriate established lifecycle, authorization, versioning, and replica-safe contracts. Files MUST NOT be able to self-authorize bindings. Deleting a consuming File MUST NOT delete shared external credentials; any future managed-material cleanup requires explicit ownership and management policy.
- **FR-031**: This feature MUST support externally provisioned material and pre-established workload trust without requiring a general secret-creation API. Automatic creation of cloud accounts, roles, application keys, managed secret slots, or generated secrets is not required. A claim is required only if an approved workflow actually requests such management; a claim alone MUST NOT fabricate provider authority.
- **FR-032**: All authoring, lookup, reverse-reference, watch, status, binding administration, and deletion-completion surfaces MUST retain configured pluggable authentication and least-privilege authorization. Provider/destination selection and redirects MUST prevent secret exfiltration or unauthorized network access.
- **FR-033**: Runtime deployment configuration and operator documentation MUST cover provider enrollment, tenant scoping, separate bootstrap/runtime delivery, live binding changes, rotation, revocation, missing-material recovery, and per-provider support limits. Every consuming replica MUST receive the required authorized configuration/material; no shared API/Git-service secret mount is implied.
- **FR-034**: File source and dependent reconciliation MUST use durable observation and bounded restartable work. Generation-only filtering MUST NOT suppress metadata/lifecycle changes or prevent recovery from external credential changes. Access/readiness MUST be rechecked on attempted operations and bounded periodic reconciliation, not remain permanently true after one successful read.
- **FR-035**: File source results, binding authorization, reference tracking, and deletion completion MUST remain duplicate-safe under concurrent replicas and reject stale updates. Accepted termination MUST not be reversed by a late source operation or refreshed credential.

### Production Requirements *(mandatory for core-service or load-bearing changes)*

- **PR-001 Replica Safety**: Verify at least two API and two controller replicas through concurrent source work, binding changes, status conflicts, reference fan-out, process replacement, and overlapping compatible versions. Runtime credential state must be reacquirable after complete process-local loss. Define mixed-version rollout/rollback constraints before implementation. Git remains exactly one active process using retained storage and non-overlapping replacement; no Git HA claim is included.
- **PR-002 Multi-User Security**: Exercise at least two namespaces and repositories with reader, author, controller, binding administrator, deletion-completion, and unauthorized identities across configured authentication/authorization providers. Verify cross-tenant denials, unauthorized role/destination changes, revoked cached credentials, and disjoint bootstrap/runtime material with zero unauthorized reads or writes.
- **PR-003 Capacity**: The production baseline is 5,000,000 Products, at least 1,000,000 Files, all four dependent kinds, a File with 10,000 dependents, and a binding shared by 10,000 Files. For 60 minutes, sustain 32 author clients offering 5 Git pushes/second with up to 10 changed manifests of at most 8 KiB each, plus 32 concurrent source-verification operations. Use owned fixtures of 1 MiB for ordinary reads and a separate 1 GiB multi-request/resumable-read case; include reference edits, File deletion, binding changes, and renewal. These are evidence workload sizes, not new public resource-size limits.
- **PR-004 Bounds and Latency**: Under PR-003, watch visibility MUST achieve p95 <=1 second and p99 <=3 seconds; eligible ordinary source/reference work MUST converge at p95 <=5 seconds and p99 <=15 seconds. A 10,000-dependent change MUST converge within 60 seconds. Pages/batches MUST contain at most 250 resources. Planning MUST fix finite worker, pending-work, refresh, credential-cache, byte, timeout, and memory/disk budgets before implementation. A five-minute double-load period MUST yield explicit backpressure without losing accepted work, and drain within 60 seconds after normal load returns.
- **PR-005 Renewable Access Evidence**: Extend `make capacity TARGET=repository PROFILE=lifecycle MODE=<diagnostic|alpha|production>` rather than introducing a new public family or feature-specific Compose overlay. Evidence MUST identify current-run owned datasets, provider accounts/buckets, authorization scopes, actual request outcomes, topology, and immutable sanitized artifacts. AWS acceptance MUST cross at least two genuine session-expiration boundaries with ongoing source reads and reacquisition. B2 S3-compatible acceptance MUST rotate and retire an actual application key while reads continue. B2 Native acceptance MUST include at least a 26-hour exercise crossing an actual 24-hour token expiration and proving reauthorization and successful subsequent reads. Accelerated clocks and local provider fixtures supplement but cannot replace these production proofs.
- **PR-006 Recovery and Revocation**: Reuse `make chaos CHAOS_PROFILE=tests/chaos/profiles/controller-restart.json` and `make chaos CHAOS_PROFILE=tests/chaos/profiles/api-restart.json` only against owned targets. Interrupt refresh beyond expiry, interrupt source reads and deletion fan-out, and restore dependencies while traffic continues. Readiness and pending ordinary work MUST recover within 60 seconds of restoration. Local binding revocation/scope narrowing MUST block new prohibited requests across replicas within 5 seconds of acknowledgement; policy unavailability beyond that bound MUST fail closed, not extend stale cached authority.
- **PR-007 Integrity and Observability**: Require zero secret-bearing persisted artifacts, zero unauthorized operations, zero stale resurrection, and zero missing acknowledged catalog transitions. Unexpected operation failures under healthy steady load MUST remain below 0.1%, with expected denials/fault injection reported separately. Report acquisition, refresh, actual source-operation, backlog, resource-budget, and per-process recovery evidence independently; a successful credential read is not a successful File operation.
- **PR-008 Evidence Limits**: Real-provider tests MUST use explicitly owned disposable data and least-privilege accounts, never operator production credentials. Bindings and tokens shared by many Files MUST show bounded refresh under replica overlap, not one acquisition per File. Document actual provider quotas and setup; insufficient duration, missing fixtures, unsupported providers, or smaller datasets MUST result in incomplete/diagnostic evidence rather than a production pass.

### Key Entities

- **File**: A Git-backed source manifest whose controller verifies source access/content and reports readiness; the source payload and its credentials are not catalog state.
- **File-targeting owner reference**: A system-owned, non-blocking relationship to one File UID, distinct from the per-media-entry required/optional presentation behavior.
- **Reverse-reference and pending work**: Authorized, paginated lookup and recoverable affected-resource work used for File state changes, plus bounded recovery for references that never resolved.
- **CredentialsRef / SecretRef**: Portable authored references and credential-type semantics from spec 063, not independently owned secret records or permission grants.
- **Runtime authorization binding**: Stable permission to use a credential source or renewable identity for specified resources, scopes, and destinations; it survives individual credential sessions and can be changed or revoked.
- **SecretBinding / SecretClaim candidates**: ADR 0001 design candidates respectively for representing binding/management metadata and requesting provisioning. Their exact resource contracts remain a planning decision, not an assertion that either currently exists.
- **Credential session or lease**: Ephemeral provider-issued access with scope, validity, and provider-specific reacquisition/renewal behavior; not a File generation or durable secret cache.
- **External provisioner**: The authorized operator or system establishing cloud trust, provisioning application keys, and replacing underlying material when needed.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: Every successfully resolved File identity has exactly one corresponding non-blocking owner reference per dependent, and paginated reverse lookup returns exactly the authorized dependent set at the production dataset size.
- **SC-002**: Zero File deletion requests are rejected solely because of catalog references. File-owned cleanup and unrelated authorization failures remain independently enforced.
- **SC-003**: Every affected required reference reaches the distinct File-deleted condition within the PR-004 convergence bounds without an author edit.
- **SC-004**: Every affected optional reference is omitted from resolved output with zero new required-reference conditions and zero independent readiness penalty.
- **SC-005**: No dependent retains a removed File's ownership or successful resolved output beyond the PR-004 bounds, including interrupted fan-out and replacement.
- **SC-006**: No persisted File-deleted reason regresses to never-resolved or silently binds a same-name replacement without a reference edit.
- **SC-007**: Work for one File scales with its actual dependents and uses bounded pages at the declared million-File/five-million-Product scale, not whole-catalog scans.
- **SC-008**: Each selected storage/authentication mode performs actual authorized source reads and content verification; credential acquisition alone never counts as source success.
- **SC-009**: Ongoing access crosses the expiry/rotation boundaries in PR-005 with no File desired-state changes or controller restarts required for normal renewal.
- **SC-010**: Zero new operations use expired or locally revoked authority outside the declared enforcement bounds; all denied cases remain isolated and expose no protected material.
- **SC-011**: Provisioning missing material, restoring identity/provider access, or replacing a controller restores pending eligible work within 60 seconds without manual File edits.
- **SC-012**: The production workload meets PR-004 latency/resource bounds and PR-007 error/integrity thresholds; overload leaves zero accepted work lost.
- **SC-013**: A recorded pre-implementation design decision explains whether SecretBinding, deployment-managed binding, and/or SecretClaim is used, with explicit lifecycle, revocation, provisioning, and ownership boundaries.

## Assumptions and Scope Boundaries

- This revision is a specification, not shipped File source access or proof of a passed capacity gate. The updated branch incorporates the current main baseline; older File documentation may describe superseded state.
- Spec 051's File representation, spec 052's owner-reference infrastructure, durable resource watch, and spec 063's reference validation/bootstrap/runtime library contracts are existing foundations to reuse. Close integration gaps rather than reintroduce process-local watch or duplicate versioning/ownership mechanisms.
- Spec 063 currently supports file/environment material readers and a contract-only runtime consumer. Its material result has no complete renewable-session/lease contract, and runtime production configuration is not wired. This feature must extend the runtime boundary and integrate the selected providers; it must not treat rereading expired bytes as renewal or disrupt working bootstrap identity.
- Existing static-material workflows remain supported. AWS workload identity is preferable to mandatory long-lived access keys, but the spec does not assert that AWS or B2 forbids long-lived keys. B2 S3 compatibility is not AWS identity compatibility; B2 Native authorization tokens and application keys have separate lifetimes.
- Provider-specific SDK acquisition/renewal may implement the approved runtime boundary, but must not bypass its authorization, isolation, redaction, or expiry guarantees. The design must distinguish provider-secret reads from access-session acquisition.
- Provider support is bounded to the three user-selected production paths plus existing local readers. General Vault/AWS Secrets Manager/other secret-store adapters and unrestricted provider plugins are not automatically included; leased-secret behavior is an explicit extension contract.
- SecretBinding/SecretClaim must be seriously evaluated, not both built by default. A stable binding need not be a new catalog resource if another mechanism meets live administration, per-resource authorization, revocation, audit, and replica correctness. A claim is not needed merely to read an externally managed secret.
- Source verification and the File controller's minimum lifecycle integration are now in scope. Existing File mutation, status, watch, and deletion contracts must be reused and any missing wiring required by the stories completed. This does not authorize unrelated CRUD redesign.
- Binary upload APIs, transformation/rendition pipelines, publishing, MediaAsset, automatic cloud-account/role/key creation, and deleting shared external payloads or externally owned secrets are excluded. Multi-request read/resume safety is required; multipart upload is not.
- File references remain non-blocking. The older blocking deletion sketch in ADR 0008 and File/media architecture is superseded for catalog dependents only; File-owned cancellation/cleanup and durable decoupling handoff remain necessary.
- `FileRefConfirmed` is the reference condition vocabulary retained from the earlier feature. Required and optional semantics must be implemented consistently across all four kinds, not assumed complete merely because CategoryTaxonomy has prior support.
- Mutable source/readiness state is private management state. Credential renewal and File changes do not rewrite immutable release/publication snapshots or grant public access to source credentials.
- No Git replication, sharding, overlapping Git replacement, or zero-downtime Git failover is in scope.
