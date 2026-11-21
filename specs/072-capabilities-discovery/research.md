# Research: Deployment Discovery and Self Access

**Date**: 2026-11-20. Decisions are proposed implementation choices; source inspection is not proof of new behavior. No graph existed in this worktree, so research used owning source files. Gates are recorded in [plan.md](plan.md).

## 1. Bootstrap and Publication Authority

**Decision**: One public document at `/.well-known/gitstore` on the Admin listener, accepting a full proxy-prefixed bootstrap URL. Publish complete external addresses/templates, independently of AuthZ readiness but behind its own ingress budget.

**Rationale**: Clients cannot discover a GraphQL endpoint by querying it first. `internal/app/graphql.go` deliberately isolates Admin and Storefront; `config/endpoints.go` describes policies, not published addresses.

**Alternatives considered**: Public GraphQL duplicates bootstrap knowledge; Host/forwarded-header inference risks poisoning and cannot reconstruct rewrites; a new listener adds operational cost. Storefront remains a placeholder and is not advertised as usable.

## 2. Client Identity Alternatives

**Decision**: Discriminated pre-registration, RFC 7591 and client-hosted metadata descriptors. Pre-registration is baseline. Optional descriptors require explicit verified support; clients choose, no silent fallback/trial registration, no GitStore registration executor or public confidential credentials.

**Rationale**: RFC 7591 obtains an identifier through registration, which may require initial access credentials. Client metadata identities let a provider discover client information at a client-owned HTTPS identifier. Neither proves grant approval. The verifier's `OIDCConfig.ClientID` is an API-audience fallback, not an interactive registration (`config/config.go`, `provider/oidcjwt/provider.go`).

**Alternatives considered**: A universal shared CLI ID excludes extensible clients; mandatory DCR adds lifecycle/prerequisite work and excludes providers; mandatory CIMD forces draft adoption. Desired metadata is not approved metadata.

**Sources**: [RFC 7591](https://www.rfc-editor.org/rfc/rfc7591.html), requested [metadata draft -00](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-client-id-metadata-document-00), evaluated [draft -03](https://datatracker.ietf.org/doc/html/draft-ietf-oauth-client-id-metadata-document-03).

**Draft decision**: Enabled CIMD descriptors pin `draft-ietf-oauth-client-id-metadata-document-03`, dated 2026-11-02. Compared with -00, account for revised authentication-method metadata/defaults, tighter identity/retrieval rules and transient/terminal failures. No silent translation of older profiles; require interoperability fixtures.

## 3. Device Flow and Resource Targets

**Decision**: Device availability requires validated device/token endpoints, the device-code grant, an explicit compatible profile and API-compatible tokens. Public native profiles use `none`; native authorization-code profiles require S256/callback restrictions. RFC 8707 is a separate protected-resource selector.

**Rationale**: `compose.oidc.yml` creates a confidential authorization-code/refresh client through Hydra's private Admin API; it does not prove public DCR, device or CIMD support. Its proprietary `audience` parameter must not be relabeled RFC 8707. This is a deployment finding, not a claim about all Hydra functionality.

**Alternatives considered**: OIDC does not imply device support; provider-wide grants do not imply client acceptance; proxy URLs need not equal token audiences.

**Sources**: [RFC 8628 discovery](https://www.rfc-editor.org/rfc/rfc8628.html#section-4), [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707.html), [OIDC Discovery](https://openid.net/specs/openid-connect-discovery-1_0.html), [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414.html).

## 4. Metadata, Cache and Fleet Rollout

**Decision**: Publication config pins the intended public provider endpoint/metadata subset. Bounded background refresh validates it against approved provider discovery; upstream changes never activate new fields/grants. Mismatch, missing prerequisites or expiry make affected profiles unavailable. Five-minute refresh/15-minute validity; no per-request fetch. Canonical document SHA-256 ETag, <=60-second freshness, no volatile generated timestamps.

**Rationale**: Configuration owns exposure; process-local observations can conservatively reduce availability but cannot expand published support. Cache is replaceable observation, not authorization/publication authority.

**Alternatives considered**: Raw upstream JSON permits uncontrolled disclosure/contract drift; indefinite stale data invents availability; distributed metadata storage/controller is unnecessary.

**Fleet decision**: Configure a required local capability floor. Each supporting API checks it before readiness. Roll out code with new ads disabled, verify all eligible replicas, then activate common publication config. Breaking configuration changes require a common overlap or advertisement removal plus cache drain. An old binary cannot enforce a new floor, so activate nothing new until it is drained. Roll back ads before binaries. No automatic membership discovery is claimed.

## 5. Self-Review and Provider Independence

**Decision**: Add me.selfAccessReviews, separate from arbitrary-subject accessReview; trusted identity only, <=32 checks, one target-derived authorization scope and one validation-start capture. No separate scope input or Boolean cluster flag. Reuse existing decision evidence under status.resolved only for authoritative adapters; otherwise structured UNSUPPORTED_CAPABILITY.

**Rationale**: Existing arbitrary-subject reviews require separate grants and authoritative resource/context handling. Self-review must not inherit that grant or invent authority. Selector-only requests cannot establish proposal-specific facts; missing context is explicit, not a false decision.

**Alternatives considered**: Forwarding to arbitrary-subject Review inherits grants/hypothetical semantics. Dummy evidence or unconditional booleans lie about authority. Resource-specific tier/visibility/create input would turn a generic selector into a parallel operation schema; omit it and report context-required where necessary.

**Source-review refinements**: ValidTrustedPrincipal accepts anon/none, so me needs an authenticated guard. Actual createNamespace authorization uses proposed tier/name; product creation uses name plus authoritative namespace owner. This proves a namespace selector alone does not always determine a complete mutation decision. Preserve existing-resource context parity and test safe refusal when proposed facts matter.

## 6. Diagnostics and Authorization

**Decision**: `deploymentDiagnostics` and `deploymentConfiguration` require new cluster actions `deployment.diagnostics.read` / `deployment.configuration.read`. Reuse bounded health functions and allowlisted registry summary. Preserve existing extension shapes and `AUTHORIZATION_UNAVAILABLE`/retryable behavior.

**Rationale**: No such actions currently exist; `iam.manage` is too broad. Deployment is a non-persisted authorization context. `admission.Error.ToGQLError` already handles diagnostics/forbidden redaction; `admissionreport.Extension` handles successful admission reports.

**Alternatives considered**: Raw config reflection/registry Policy serialization leaks facts; a parallel diagnostic error envelope breaks clients; self-HTTP health probes add unnecessary failure modes. Authorize before probing protected details.

## 7. Capacity and ADR Scope

**Decision**: Extend existing endpoint-split probes/preflight/verifier with publication/flow/self-review oracles, preserving connected authoring/push/watch evidence. Keep the spec envelope as an acceptance target, record actual runtime resource ceilings, retain existing stricter fault gates. No live execution during planning.

**Rationale**: Existing family covers two APIs/two controllers/one Git and retained assets. Discovery traffic cannot replace domain correctness or turn Storefront 503 throughput into successful catalogue reads.

**Alternatives considered**: New standalone/shortened benchmarks fail coexistence proof; new Make targets duplicate selectors. ADR 0019 remains outside scope; no status flip is justified by discovery registration alone.

## 8. Prevent Initial Convention Drift

**Decision**: Apply the user's explicit envelope/placement rules and [#40](https://github.com/gitstore-dev/GitStore/issues/40) status vocabulary: every new top-level GraphQL object has apiVersion/kind/metadata; spec/status bodies do not mix parallel domain fields; new observation statuses use shared Condition and typed resolved data. Reuse existing review evidence under status.resolved without changing accessReview. CurrentUser has an envelope and direct fields, with no artificial spec/status. Computed metadata and nullable generation/revision fields must not fabricate persisted state. Batch/key wrappers remain helpers. Enforce shape, rejection-before-effects and parity in default tests; promote the planning SDL once.

**Rationale**: [PR #586](https://github.com/gitstore-dev/GitStore/pull/586) corrected identity input conventions and discriminator validation even though generated-source checks were available. Syntactically valid/generated SDL is insufficient. Earlier plan revisions still missed top-level envelopes, mixed review subject beside spec/status, and used an ad hoc diagnostic status vocabulary. Those shapes are corrected here, with explicit structural assertions rather than projection exemptions.

**Alternatives considered**: More prose alone does not fail a regression. Generated-source-only gates accept inconsistent schema semantics. Forcing full resource envelopes onto helpers/protocol metadata contradicts the established classification. A separate global cleanup or optional drift suite defers this feature's obligations.

**Metadata and query correction**: Reuse shared metadata types, nullable only at computed output field where no authoritative record exists. Follow #589 direct query returns/descriptive arguments; do not keep its removed wrapper or input argument as a compatibility precedent. No new Payload/result wrapper.

**Selector decision**: Only id, namespacePath and inNamespace context. Action definitions supply kind/scope. No name/create branches, resource-specific proposal fields or per-kind union. Context-free cluster checks may omit target only when registered as such. Generic selector simplicity is preserved by explicitly refusing incomplete-context decisions.

**Batch derivation**: All targets must resolve to CLUSTER or one identical authoritative Namespace UID/incarnation before a single capture. Reject mixtures/unknown scope wholesale, retain non-disclosure, and detect recreation races. Tests must establish same-scope cross-kind success and mixed-scope rejection. Alternatives rejected: cluster=true sentinel, redundant caller scope, per-kind SDL growth, automatic partitioning and untyped target JSON.

## 9. Endpoint Credential Use Versus Login Acquisition

**Decision**: Publish credential scheme/delivery/provider/profile bindings per endpoint and keep acquisition flows in shared profiles. Verify audience/resource compatibility rather than assume one login credential works everywhere. Derive support from actual handlers/providers, not middleware names.

**Rationale**: Git BasicAuthenticator forwards original headers to the shared chain; oidc-jwt handles Bearer while static-users handles Basic passwords and Bearer sessions. No OIDC-token-as-Basic-password conversion exists. GraphQL WebSocket requires connection_init authorization even if non-browser upgrade credentials were prevalidated. The transport's usable delivery mechanism matters independently of OAuth grant selection.

**Alternatives considered**: Duplicating device/code login flows per endpoint drifts; publishing global flows without endpoint bindings leaves clients unable to tell where tokens work. Guessing Basic-only from middleware naming or treating an upgrade header as sufficient WebSocket authentication misstates actual behavior. Add positive/negative handler, provider, audience and delivery fixtures.

## 10. Review Naming and Normalized References

**Decision**: Keep established namespacePath for selecting a resource and use inNamespace for namespace-only context. Remove the unreturned correlation key/check wrapper; ordered result positions suffice. Replace loose output namespace/ID/name with targetRef using the existing CatalogObjectReference, which already exposes ObjectReference fields.

**Rationale**: Separate namespace duplicates target identity, and an unreturned key has no correlation role. References carry kind/API version and source UID/version accurately; adding another type would duplicate the existing reference contract. For namespace context, reference the Namespace itself; for no concrete cluster target, return null.

**Alternatives considered**: Echoing keys adds unnecessary surface; renaming namespacePath loses an established lookup convention; a new review-specific reference or duplicated output fields creates drift. Scope is still resolved internally and checked before one policy capture.
