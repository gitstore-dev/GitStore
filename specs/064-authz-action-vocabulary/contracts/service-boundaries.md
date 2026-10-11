# Contract: service enforcement and identity rollout

## Catalog write concurrency

Product, CategoryTaxonomy, Collection and ProductVariant desired-state update methods require `expectedResourceVersion`. Memdb checks it in the write transaction; Scylla uses it in the authoritative LWT. Successful writes advance the version. A concurrent owner/status/spec change invalidates the caller's snapshot, returning a conflict rather than replacing newer fields. No implicit latest-version lookup, unconditional update overload or automatic blind retry is permitted. A retry must load and authorize the new row.

## Authorization context

Keep `AuthZProvider.Authorize(ctx, principal, action, ResourceContext)` and
ResourceContext's Kind/Name/OwnerSub/Attrs unchanged. Use generated catalog
constants and canonical camelCase Kind values everywhere. Attributes contain
namespace, tier, visibility only when known from authoritative request/resource
facts, and a structurally normalized targetOwnerRef for transfers. Use existing
lookups; authorization itself adds no datastore or network calls.

| Boundary                              | Required action / behavior                                                                                   |
|---------------------------------------|--------------------------------------------------------------------------------------------------------------|
| Namespace create                      | namespace.create, attrs.tier on both USER and ORGANIZATION paths                                             |
| Namespace reads/lists/updates/deletes | namespace.read/list/update/delete; include node/nodes routes                                                 |
| Ownership mutation                    | kind.transfer; authorizer decides owner/membership; admission enforces invariants                            |
| Repository object CRUD/list/watch     | repository.create/read/list/update/delete/watch; never content actions for metadata                          |
| Git clone/fetch/read blobs            | repository.contents.read                                                                                     |
| Git push/write blobs/refs             | repository.contents.write                                                                                    |
| CategoryTaxonomy                      | categoryTaxonomy verbs, including status.write and purge                                                     |
| Product / ProductVariant reads        | read/list or management.read/list for the actual requested/effective data scope                              |
| Completion mutations                  | namespace/repository/categoryTaxonomy/product.purge; finalizers and resource-version guards remain mandatory |
| ServiceAccount CRUD/key/token         | serviceAccount verbs, key.rotate, token.issue                                                                |
| Typed and generic watches/status      | canonical kind slug, list/watch/status.write, namespace facts where applicable                               |
| Namespace system provisioning         | namespace.system-repository.provision; backing Git calls use repository actions                              |
| Catalog validate/delete/admit         | appropriate kind create/update/delete/transfer using authenticated author facts                              |

Inventory every protected field/operation in the existing middleware before
implementation, including aliases, fragments, nested fields and node/nodes so a
second route cannot bypass the same operation. This is direct middleware work,
not introduction of the deferred authorization-directive framework.

### Implementation inventory (T001, 2026-10-11)

This inventory records source inspection, not passing runtime coverage. Paths
below are relative to the repository root. The field authorizer must operate
on schema field names (aliases and fragments do not change authorization).
Every row remains an acceptance obligation until its indicated test passes.
`security` denotes `gitstore-api/internal/middleware/security`; `resolver`
denotes `gitstore-api/internal/graph/resolver`. Facts must come from stored
objects/existing lookups, not caller-provided ownership booleans.

| Protected operation | Canonical action / Kind | Facts and current gap | Source / verification destination |
| --- | --- | --- | --- |
| Query.namespace, namespaces; Namespace through node/nodes | namespace.read/list / namespace | Name/owner/tier; missing direct/list/node coverage must be added | security/graphql.go; graphql_test.go; tests/integration/namespace_contract_test.go |
| createNamespace | namespace.create / namespace | Proposed tier/name/owner; USER currently returns before authorizer | security/graphql.go; graphql_namespace_admission_test.go |
| updateNamespace | namespace.update / namespace | Current effective owner plus proposed immutable fields; explicit check required | security/graphql.go; resolver/namespace.resolvers.go; namespace_contract_test.go |
| deleteNamespace | namespace.delete / namespace | Current owner; remove ownership-selected .own/.any | security/graphql.go, secure.go; graphql_test.go |
| transferNamespaceOwner | namespace.transfer / namespace | Current owner + normalized targetOwnerRef; remove IsAdmin override, retain CAS | security/ownership.go, graphql.go; ownership_test.go |
| provisionNamespaceSystemRepository | namespace.system-repository.provision / namespace | Namespace/name/owner; internal backing writer separately authorized | security/graphql.go; gitstore-api/internal/app/bootstrap_test.go |
| completeNamespaceDeletion | namespace.purge / namespace | Already canonical; finalizers/RV unchanged; delete/status must not grant purge | security/graphql.go; graphql_namespace_admission_test.go |
| Query.repository, repositories; Repository via node/nodes | repository.read/list / repository | Namespace/owner/name; list currently uses read and tenant helper selects .own/.any | security/graphql.go, secure.go; resolver/repository_authorization_test.go |
| createRepository, updateRepository, deleteRepository | repository.create/update/delete / repository | Namespace owner for create; stored resource owner for changes; retain source/target namespace guards | security/graphql.go, secure.go; tests/integration/authz_repository_contract_test.go |
| completeRepositoryDeletion | repository.purge / repository | Already canonical; namespace/name; independent of delete/status | security/graphql.go; graphql_repository_watch_test.go |
| provisionRepositoryStorage | repository.status.write / repository | Namespace/name and existing provisioning CAS | security/graphql.go; resolver/repository_authorization_test.go |
| Query.category, categories; Category children and node/nodes | categoryTaxonomy.read/list / categoryTaxonomy | Name/namespace/effective owner; preserve bounded hierarchy filtering | security/graphql.go; graphql_category_lifecycle_test.go |
| createCategory, updateCategory, deleteCategory | categoryTaxonomy.create/update/delete / categoryTaxonomy | Namespace and stored owner; no category slug | security/graphql.go; graphql_category_lifecycle_test.go |
| completeCategoryDeletion | categoryTaxonomy.purge / categoryTaxonomy | Already canonical; finalizers/RV retained | security/graphql.go; graphql_category_lifecycle_test.go |
| Query.product, products; Product via node/nodes | product.read/list or product.management.read/list / product | Canonicalize current PascalCase Kind; independently sufficient management grants, preserve public filtering | security/graphql.go; resolver/product.resolvers.go; tests/integration/product_authorization_test.go |
| createProduct, updateProduct, deleteProduct | product.create/update/delete / product | Namespace/name/stored owner; no new visibility facts | security/graphql.go; graphql_product_lifecycle_test.go |
| completeProductDeletion | product.purge / product | Already canonical; no product.delete.complete regression | security/graphql.go; graphql_product_lifecycle_test.go |
| ProductVariant query/connection and nested product variants, node/nodes | productVariant.read/list or productVariant.management.read/list / productVariant | Authorize actual PUBLIC/MANAGEMENT selection; no role shortcut | resolver/product_variant.resolvers.go; tests/integration/product_authorization_test.go |
| Collection query/connection and node/nodes | collection.read/list / collection | Existing namespace/name/owner facts; add missing middleware check | shared/schemas/collection.graphqls; security/graphql.go; graphql_test.go |
| Category.products, Collection.products and nested resource relations | product.list (or management.list where supported) / product | Preserve parent namespace guard and publication filtering; do not infer child grants from parent read | security/graphql.go; resolver/product.resolvers.go; tests/integration/product_authorization_test.go |
| Query.file; File via node/nodes | file.read / file | Stored namespace/name/owner; canonicalize File Kind | security/graphql.go; graphql_file_status_test.go |
| watchNamespaces, watchRepositories, watchCategories, watchProducts, watchFiles | kind.watch / canonical kind | Namespace filter for namespaced kinds; empty Name; canonicalize PascalCase watch context | security/graphql.go; graphql_subscription_transport_test.go and existing per-kind watch tests |
| watchResources | catalog kind.watch / canonical kind | Reject unsupported kind/source; do not add new watch sources | security/graphql.go; resolver/watch.go; graphql_subscription_transport_test.go |
| updateNamespaceStatus, updateRepositoryStatus, updateCategoryStatus, updateProductStatus | kind.status.write / canonical kind | Name/namespace; category.status.write remains legacy | security/graphql.go; existing per-kind lifecycle/status tests |
| updateResourceStatus | catalog kind.status.write / canonical kind | Reject unknown kind rather than merely lowercasing; existing RV guards | security/graphql.go; graphql_file_status_test.go |
| createServiceAccount, rotateServiceAccountKey, deleteServiceAccount | serviceAccount.create/key.rotate/delete / serviceAccount | Global name; reject namespace; current action slug is lowercase serviceaccount | security/graphql.go; resolver/serviceaccount_service_test.go |
| issueServiceAccountToken | serviceAccount.token.issue / serviceAccount | Global name + independently verified assertion/UID/key/replay; no admin proof bypass | security/graphql.go; resolver/auth_service.go; tests/integration/serviceaccount_auth_test.go |
| SA read/list/update service paths (where exposed) | serviceAccount.read/list/update / serviceAccount | Reject namespace filters/arguments; do not add endpoints just because catalog reserves actions | shared/schemas/serviceaccount.graphqls; resolver/serviceaccount_service.go; serviceaccount_service_test.go |
| Git HTTP info/refs upload-pack, git-upload-pack | repository.contents.read / repository | Stored name/namespace/owner; authentication challenge preserved | security/secure.go; secure_test.go; tests/integration/git_http_test.go |
| Git HTTP info/refs receive-pack, git-receive-pack | repository.contents.write / repository | Same resource facts, no metadata-read substitution | security/secure.go; secure_test.go; tests/integration/git_http_test.go |
| GitService read RPCs: refs/tree/blob/files | repository.contents.read / repository | Signed verified actor and repository ID; currently repository.read.any | gitstore-api/internal/gitclient/read.go; read_test.go; gitstore-git-service/src/grpc/server.rs |
| GitService write RPCs: commit/write/delete file/ref | repository.contents.write / repository | Original domain actor distinct from internal writer; currently repository.write.any | gitstore-api/internal/gitclient/write.go; write_test.go; gitstore-git-service/src/grpc/server.rs |
| GitService create/delete repository | repository.create/delete / repository | Explicit system:api policy decision; currently create.any/delete.any | gitstore-api/internal/gitclient/write.go, authorization.go; authorization_test.go |
| Catalog ValidateResources / ValidateResourceDeletions | semantic kind.create/update/delete/transfer / catalog kind | Verified actor from signed receive context; currently lacks principal-aware policy enforcement | gitstore-api/internal/cataloggrpc/server.go; server_test.go; namespace_validation_test.go |
| Catalog AdmitResources (including retries) | same semantic actions / catalog kind | Bound receive/ref/commit/repository actor; reevaluate live policy; preserve CAS | gitstore-api/internal/cataloggrpc/admission_operations.go; admission_operations_test.go |
| API bootstrap and domain-to-Git backing work | narrow repository.create/delete/contents.read/contents.write / repository | Initialize registry first; no automatic missing-principal promotion | gitstore-api/internal/app/server.go, bootstrap.go; bootstrap_test.go; gitclient/authorization_test.go |
| Controller list/watch/read/status/provision/final removal/token exchange | explicit controller + own-account token grants | Verify actual emitted operations, four purges, product management and auth-rejection recovery | gitstore-controller-manager/internal/graphqlclient; tests/integration/serviceaccount_auth_test.go; secret_bootstrap_test.go |

Authorization of nested objects must use the child's operation and facts even
when its parent is authorized. The final audit compares this matrix against
`shared/schemas/*.graphqls`, the gqlgen field ledger and all Go/Rust authorization
call sites; reserved catalog actions with no implemented boundary remain
unexposed, not silently implemented by this feature.

## Signed API → Git and Git → API boundaries

Remove `Principal.Roles` and `Principal.IsAdmin`. Remove protobuf
`AuthContext.roles` and reserve field number 4 and name `roles`; regenerate Go
and Rust stubs through existing buf configuration. The inspected baseline used
a reusable bearer secret, not signed envelopes. The 2026-10-11 user decision
explicitly replaces it with signed, expiring, audience-bound, replay-protected
requests; there is no bearer fallback. Rust requires both catalog membership and the
method's exact allowed canonical action. Legacy/unknown actions fail explicitly
even if signed; metadata read permission does not permit content reads/writes.

The `x-gitstore-rpc` metadata value is
`issuedMillis:expiryMillis:nonceHex:bodySHA256Hex:macHex`. HMAC-SHA256 covers
`gitstore-rpc-v1\n<audience>\n<full-RPC-method>\n` followed by the first four
colon-separated fields. Nonces are 16 random bytes, signatures/digests 32 bytes,
hex is lowercase, headers are bounded to 256 bytes, lifetime is at most 60s,
and future issuance is limited to 5s clock skew. Audiences are `gitstore-git`
and `gitstore-api`. The body is deterministic protobuf encoding of the complete
decoded request, including its role-free actor and authorization context. These
messages contain no maps or oneofs; Go/Rust golden vectors pin tag-order
encoding. Unknown/new wire fields are not a compatibility contract.

ReceivePack signs its first chunk, including every ref command, push policy and
actor. Later pack chunks remain on that accepted stream; Git's content-addressed
object/ref validation remains required. The client opens the stream lazily on
the first send and never buffers the complete pack. No transparent configured
RPC retry is enabled. Each explicit new call obtains a fresh nonce and performs
the existing live authorization; replay rejection is not evidence a mutation
failed before execution.

Git persists single-use reservations in `<data_dir>/.rpc-replay-v1` before Git
effects, retains them across non-overlapping process replacement, caps live
records at 100,000, reclaims at most 256 expired records per call and never
evicts live reservations. Clock high-water is persisted before expired records
are deleted; clock rollback fails closed. Verification uses at most 32 blocking
workers, with excess requests rejected rather than queued without bound.
Callbacks consume domain-separated nonce digests through the existing atomic
datastore replay table, shared by API replicas (Scylla LWT; memdb transaction in
development). Store errors fail closed. The previous shared signing key may be
accepted during explicit rotation on both receivers; primary keys sign new
requests, and replay state is independent of the signing-key version.

Signatures authenticate requests, not response confidentiality. Keep gRPC on a
trusted isolated network; this feature does not introduce TLS or multiple Git
processes. Old bearer-only peers, v1 policies and namespaced ServiceAccount
configuration are rejected; shipped local/Scylla/cluster/optional-OIDC Compose
configurations must use the new baseline together.

Build the provider registry before bootstrap Git operations. Inject a
registry-backed authorizer into the existing Git client so provider reload is
observed; missing authorizer/principal fails closed. Trusted internal call sites
explicitly establish `system:api`. Its narrow binding is evaluated before every
internal signed authorization. Do not turn an absent caller into an implicit
privileged identity.

Separate the domain actor from the backing Git writer: an authorized product
mutation may use the internal Git writer without granting the end user raw Git
write. Preserve the original authenticated author facts for audit and admission.
Internal writer credentials alone must not relabel user-requested domain work
as an internal privileged operation.

Extend existing catalog ValidateResources, ValidateResourceDeletions and
AdmitResources requests with a bounded authenticated principal snapshot from
the verified Git authorization, including subject, trusted groups, auth method
and verified SA UID where relevant. Propagate it through HookContext and sign/
authenticate the reverse service call with the existing service trust mechanism.
Bind the snapshot to repository and receive/ref/commit context; the callback
must not substitute an actor from a manifest, Git author line or arbitrary
header. Reject missing/untrusted facts for actor-dependent operations.

Validate before refs move and enforce the same invariant in bounded CAS
admission retries. A verified accepted-push snapshot can carry the author facts
through asynchronous admission; no raw bearer token/private key is persisted in
it. Any durable retry representation must retain authenticated provenance and
the accepted receive identity, not indefinitely replay an unbound user token.
Keep post-receive policy-change failures explicit and observable; do not silently
allow a queued push because preflight used a different policy snapshot.

## Namespace invariants

USER owner must be User and name must equal that user's normalized username
(the existing authenticated Subject, including the configured OIDC username
mapping). A self-service create must name the caller. Group/SA owners are invalid
for USER, including for cluster-admin. Namespace uniqueness provides one personal
namespace per username; concurrent creates use existing datastore conflicts.
Reject unsupported usernames with the namespace-name constraint. ORGANIZATION
creation remains controlled by grants; the shipped policy gives it only to
cluster-admin.

Reject USER transfer, USER rename, and any tier change, including promotion.
Compare old/proposed trees and stored state so Git delete/create combinations
and concurrent admission cannot evade this. Preserve owner/resource-version
concurrency guards separately from policy ownership evaluation.

Bootstrap `default` as ORGANIZATION in fresh development/test schemas. No
deployed namespace cutover or legacy-row repair is required for this alpha
baseline; do not introduce a general tier-change bypass. Existing developer
data is not silently renamed, transferred or deleted by implementation.

## Name-only ServiceAccount API

The shared ObjectMeta schema keeps namespace for other kinds. All SA create,
read, list filtering, update, deletion, key and token paths reject a supplied
nonempty metadata.namespace (and reject removed namespace-specific arguments).
Return empty/unset namespace in SA metadata. Binding subjects are
`{kind: ServiceAccount, name: gitstore-controller-manager}`; subject and OwnerSub
encoding is `system:serviceaccount:gitstore-controller-manager`.

Reject legacy `serviceaccount:<label>:<name>` tokens/assertions everywhere.
Assertion iss/sub and access-token sub use the canonical subject; verify UID,
enrolled key, audience, expiry, disabled/deleted account and replay protections
as before. Token issuance still requires proof for the exact target account in
addition to serviceAccount.token.issue. No cluster-admin role bypasses that proof.

AuthN success normalization rejects reserved external User subjects, including
system:api, even when they arrive through an OIDC username claim/userinfo or a
local configured username. Only verified SA providers may assert canonical SA
subjects. Authentication supplies exclusive system:authenticated or
system:unauthenticated, plus system:serviceaccounts for verified SAs. Do not
import privileged groups from untrusted claims; system:masters membership must
come from the configured trusted authentication boundary. No group-directory
source is added in this feature.

Controller configuration identifies name, UID and key/secret reference, without
namespace. Reject the removed TOML/environment/CLI namespace setting with an
actionable message. Existing config examples, Compose environment wiring,
enrollment scripts, secret integration tests and capacity fixtures must migrate.
Do not overwrite an enrollment identity file with a different UID silently.

## Storage and failure semantics

Replace the namespaced ServiceAccount table definitions directly in undeployed
baseline migration `009_service_account.cql`. Do not append migration 013 or
retain the superseded tables. Replacement tables: `service_accounts_by_cluster_name` (authoritative partition by
name), `service_accounts_by_cluster_uid` and
`service_accounts_by_cluster_bucket` (existing bounded listing strategy).
Retain existing fields apart from namespace, including UID, timestamps, keys,
disabled state and optimistic resource version. Name create uses LWT IF NOT
EXISTS; update/delete conditions include UID and expected resource version.
Also conditionally reserve the unique UID-to-name mapping before making an
enrollment usable. Concurrent same-UID/different-name creates cannot overwrite
it. Partial-create cleanup verifies its own name/UID/reservation identity;
repair never steals another enrollment's UID. Memdb enforces unique name and
UID in one transaction. Incomplete enrollments fail closed until finalized.

UID routing/reservation and bucket rows are never independent authentication authority.
Resolve through the authoritative name row and verify UID; stale projections or
partial writes must not resurrect a deleted account or authorize a replacement
with an old token. Reuse existing projection audit/repair infrastructure and
bounded enumeration. Fault tests cover interruption between authoritative and
projection writes; repair must not overwrite a newer UID. Keep replay-digest
and revocation tables/protections unchanged. New code never falls back to old
namespaced tables. Existing old actor strings in audit/Git history remain data,
not accepted new authentication identities.

## Fresh alpha setup and replacement

The user confirmed on 2026-10-11 that no released deployment depends on the
namespaced ServiceAccount schema. Replace it directly; no data transfer,
compatibility shim, old/new fleet cutover or legacy-schema rollback artifact
is required. This explicit exception applies to the undeployed baseline only.

1. Replace the relevant definitions in migration 009, preserving unrelated
   replay/revocation tables and their protections. Remove namespaced store
   lookups, indexes and legacy configuration paths.
2. Initialize a fresh isolated development/test schema from the revised
   baseline. Do not bypass checksum validation or silently reset an existing
   database. Reinitializing disposable local state is an explicit developer
   workflow, not an automatic migration action.
3. Start the singleton Git service before API Git-dependent bootstrap. Start
   APIs with the matching v1beta1 policy and configuration; bootstrap default
   as ORGANIZATION.
4. Enroll controller ServiceAccounts by unique name, verify their new UIDs and
   key fingerprints, then start controllers with name-only configuration.
   Do not overwrite a different enrollment identity silently.
5. Verify matching decisions across both APIs, both controller readiness states,
   malformed-reload retention, unique name/UID writes and bounded credential
   recovery. Run same-version rolling API/controller replacement and
   non-overlapping Git replacement against retained storage.
6. Keep legacy actions, subjects and v1 policy as fail-closed negative fixtures.
   They do not require constructing or supporting an old deployed fleet.

A definitive authentication rejection invalidates only its token generation
and triggers coalesced renewal through existing backoff. Stale errors cannot
evict newer credentials; FORBIDDEN or ambiguous mutation responses do not cause
token-exchange loops or unsafe mutation replay.

Document the alpha schema/API break and the fresh-schema development workflow.
No existing database is modified or deleted by this planning update.
