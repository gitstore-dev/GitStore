# Data Model: Deployment Discovery and Self Access

Proposed structures; no new persisted records/migrations. Validated snapshots are immutable and atomically replaced. Existing resources/RBAC stores retain authority.

## PublicationProfile

Operator configuration: `enabled=false` by default, `profileId`, finite versioned `requiredFeatures`, `allowedOrigins`, <=16 `endpoints`, <=16 `providers`, <=32 total `clientProfiles`, <=128 extension descriptors and <=64 search kinds with <=64 terms each. Public projection <=256 KiB. Reject excess rather than truncate.

Endpoint: unique `id`, `protocol`, exactly one `url`/`urlTemplate`, defined template variables/encoding. Absolute HTTPS/WSS public addresses; explicit known-local development exceptions only. Reject userinfo/fragments, ambiguous templates and inferred upstream addresses. Operator config owns proxy publication; listeners cannot discover external rewrites.

Endpoint authentication binds provider/profile references to accepted BASIC/BEARER schemes and actual protocol delivery channels. Login profiles define credential acquisition once; bindings define credential use and audience/resource compatibility for each endpoint. Validate against handler/provider descriptors and proxy forwarding behavior, never names such as BasicAuthenticator. Unsupported Basic-token conversion, cookie or redirect behavior is not advertised. GraphQL WebSocket init and HTTP header delivery are separate capabilities. Anonymous use remains operation-dependent.

The required feature floor bounds all published functionality. Missing local support rejects readiness; component registration alone cannot activate a feature. Profile pins the public provider metadata subset; upstream refresh can disable incompatible flows but cannot expand it. State: `DISABLED -> VALIDATING -> ACTIVE`. Invalid startup fails ready; invalid reload retains last coherent bytes internally for diagnosis, sets discovery-valid=false and ready=false, and returns 503/no-store rather than serving those obsolete bytes. Discovery-valid is independent of authorization readiness: AuthZ failure alone leaves a valid public document readable. Provider metadata failure changes affected flow availability, not discovery-valid. Activation follows the fleet procedure in [operations](contracts/operations.md).

## ProviderDescriptor / ValidatedProviderObservation

Descriptor: `id`, `displayName`, `kind`, approved `discoveryUrl`, expected public metadata subset, linked client profiles. Password mechanism identifies the published Admin endpoint and existing `login` mutation, not an invented token endpoint.

Allowlisted OIDC metadata: canonical issuer; authorization/token/device/registration endpoints; supported grants, response types, scopes, PKCE and token authentication methods; CIMD support. Validate issuer under the verifier's existing explicit identity rules. Never relay raw upstream JSON or silently rewrite issuer identity for a proxy.

Private observation: validated digest, upstream validator, last validation/expiry, next attempt and safe reason. `UNKNOWN -> AVAILABLE -> UNAVAILABLE`; expiry/mismatch disables dependent flows; successful validation restores them. Transient fetch failure retains the last observation only within its original validity. Safe reasons: `METADATA_UNKNOWN`, `METADATA_EXPIRED`, `METADATA_MISMATCH`, `DEPENDENCY_UNAVAILABLE`. No private host or network error detail.

Two fetch workers total; one queued/in-flight item/provider; three-second timeout; 256 KiB body; zero redirects. Approved HTTPS DNS/dial destinations only, protecting against rebinding/private-network access except explicit local-dev endpoints. Caller-supplied issuer/client-ID URLs never trigger API-side retrieval. CIMD retrieval belongs to the provider.

## ClientProfile

Unique `id`, `providerId`, `applicationKind` (`BROWSER`, `NATIVE`, `SERVICE`), `bootstrap`, configured flows, scopes, token authentication method, callback restrictions and optional resource target. Interactive IDs never default from the verifier audience. Public native profiles use `none`; authorization-code native profiles require S256. Confidential descriptors contain no credential and are not directly usable public CLI profiles.

Bootstrap union:

- `PRE_REGISTERED`: explicit interactive `clientId` and configured accepted constraints.
- `DYNAMIC_REGISTRATION`: verified endpoint, `registrationAccess=OPEN|PROTECTED|UNKNOWN`, public prerequisites. Protected registrations need out-of-band credentials. The registration response determines actual identity/accepted metadata.
- `CLIENT_ID_METADATA_DOCUMENT`: exact profile revision -03, verified provider support; client supplies its owned HTTPS identifier. No shared fabricated ID/automatic hosting.

Each flow has `name`, `availability=AVAILABLE|UNAVAILABLE|REQUIRES_REGISTRATION|REQUIRES_CLIENT_METADATA`, optional safe `reasonCode`. Only a verified pre-registered flow is immediately available. Optional methods report prerequisites; client negotiation establishes actual usability. `refresh_token` is metadata, not a login UI flow.

Resource target: optional `parameter=resource`, absolute fragment-free `value`, explicit allowed grants. Independent of connection URL/client ID. A proprietary audience parameter is not a resource indicator. API audience verification remains separate.

## DiscoverySnapshot

`schemaVersion=1.0.0`, `revision`, `profileId`, endpoints, `authentication.providers` with linked profiles, extensions, search. Typed registrations own IDs/versions. Duplicate IDs/unknown publication references reject profile validation; registrations create no App or policy grant.

Canonical payload excludes `revision` for hashing, sorts object keys and identity sets but preserves semantic AuthN order. `revision=sha256:<hex digest>`; ETag is its quoted value. No generated-at timestamp. Render off the request path on profile/observation-state changes. Public observations may conservatively differ across replicas during an upstream failure; identical public content has identical validators and cannot advertise beyond the common floor. HTTP TTL never exceeds 60 seconds or remaining relevant metadata validity.

## CurrentUser / SelfSubjectAccessReview

Trusted authenticated identity only. Query accepts reviews directly and returns an ordered list, following #589. There is no generic batch input wrapper. A supplied target chooses exactly one id, namespacePath or inNamespace selector. Canonical action determines kind and scope class; omitted target is allowed only for supported cluster operations needing no individual resource. No name/create branch, resource-specific proposal fields or growing per-kind union. Existing owning selector/context helpers retain authority.

Resolve each target to internal scope key CLUSTER or authoritative Namespace UID/incarnation. Every check must have the same key before capturing policy once. Reject mixed cluster/namespace, multiple namespaces or unresolved scope without partial evaluation; do not disclose inaccessible scopes in the error. Revalidate captured namespace/resource-version facts before evaluation to catch deletion/recreation. Metadata.namespace cannot override derived scope. Namespace resource actions are cluster-scoped even though their name is a namespace slug.

Selectors locate targets/context; they do not describe proposed mutations. Creation permission may use a namespace selector or no target for eligible cluster actions, but if a decision depends on an unknown proposed name/tier/visibility/owner, return UNSUPPORTED_CAPABILITY with REVIEW_CONTEXT_REQUIRED unless the provider can prove the result independent of missing facts. Do not invent defaults for an unspecified proposal, silently ignore conditions or call an incomplete evaluation DENY. Resource-specific previews are out of scope.

A batch uses one authoritative validation-start capture and sequential bounded lookups. Lookups use normal disclosure policy: inaccessible and absent targets are indistinguishable. Global input defects reject the batch; per-check failures produce a null list item with the existing GraphQL error at that index. Non-create reviews cannot spoof a prospective target.

Response is a direct ordered list correlated by input index. Review envelope/spec/status and shared evidence under status.resolved remain unchanged. No query Payload; align existing accessReview integration with #589 direct returns/descriptive arguments while retaining its authorization/evaluation semantics.

## Protected Diagnostics / Configuration

All top-level GraphQL objects carry apiVersion/kind/metadata. CurrentUser omits spec/status and exposes its direct subject/query fields after that envelope. Diagnostic/configuration projections instead define only status as their body, with #40 shared conditions and typed resolved details. Diagnostics status.resolved contains softwareVersion/build identity, Admin schema digest over committed Admin SDL, publication revision, bounded component conditions and redacted provider-plane summary. Contract apiVersion is not software version. Configuration status.resolved contains finite entries with CONFIGURED/MISSING/DISABLED and safe missing-field names.

Reuse ObjectMeta for outputs and ObjectMetaInput for optional review metadata. No runtime, reduced or review-specific metadata schema. Computed output metadata is nullable: return authoritative source metadata when available, otherwise null; do not fabricate UID/owner/timestamps or alter shared ObjectMeta nullability. Review metadata follows shared validation and cannot override scope/authority. These observations have no desired generation or applied Git commit: corresponding status fields remain null. Conditions use actual transitions and the shared Condition type. No raw config/policy/secret references; secret-presence facts only for authorized callers. All protected responses are private/no-store; existing error/admission extensions remain unchanged.

## Review Correlation and References

The query directly accepts versioned SelfSubjectAccessReviewInput values; no SelfAccessReviewCheckInput or key. Output indices correspond to inputs, including null items/errors. spec.targetRef reuses CatalogObjectReference and replaces loose namespace/resourceId/resourceName fields. For a concrete resource it contains actual kind/name/namespace/UID/version; for inNamespace it references the Namespace itself, with namespace=null. Context-free cluster operations return no targetRef. Never confuse source UID and Relay input ID. Shared reference type/doc semantics cover applicable resource kinds without another contract.
