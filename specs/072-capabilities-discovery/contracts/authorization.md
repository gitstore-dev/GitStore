# Authenticated Review and Diagnostics Contract (Proposed)

The additive SDL is [admin.graphqls](admin.graphqls), to merge into the existing Admin schema during implementation. Top-level objects carry the standard apiVersion/kind/metadata envelope. Status follows [#40](https://github.com/gitstore-dev/GitStore/issues/40), reusing `Condition` and typed resolved data. Existing `SubjectAccessReviewStatus` supplies decision evidence under the new self-review's `status.resolved`; no existing `accessReview` input/output or permission changes.

## Top-Level Envelopes and Field Placement

| Object | Envelope / body |
| --- | --- |
| CurrentUser | `apiVersion=authentication.gitstore.dev/v1beta1`, `kind=CurrentUser`, metadata; direct subject and selfAccessReviews fields, no spec/status |
| SelfSubjectAccessReview | `apiVersion=authorization.gitstore.dev/v1beta1`, `kind=SelfSubjectAccessReview`, metadata; only spec/status as body fields; principal belongs in spec |
| DeploymentDiagnostics | `apiVersion=discovery.gitstore.dev/v1beta1`, `kind=DeploymentDiagnostics`, metadata; only status as body; observation details in status.resolved |
| DeploymentConfiguration | `apiVersion=discovery.gitstore.dev/v1beta1`, `kind=DeploymentConfiguration`, metadata; only status as body; redacted observed configuration in status.resolved |

If an object defines spec/status, do not add parallel domain body fields alongside them. An object without spec/status may have direct fields after its standard envelope. Transport payloads, correlation wrappers and nested value types are not independent resource objects and do not acquire envelopes. These read-only projections create no persisted resource, Node, CRUD or desired configuration write API.

Reuse ObjectMeta/ObjectMetaInput without variants. Computed output metadata is nullable: use authoritative source metadata when available, otherwise null; never fabricate stored UID/owner/time/generation or weaken shared fields. Request metadata.name is an optional request name, not a correlation key. Its namespace must match derived scope; reject resourceVersion on this non-persisted request. Shared label/annotation validation applies and confers no authority. Correlate batch results by input/output index only.

New status types contain conditions, typed resolved data, nullable observedGeneration and nullable lastAppliedRevision. These projections have no desired-state generation or applied Git commit, so those last two fields are null, never invented counters/commits. Reuse shared Condition (type, tri-state status, observedGeneration, lastTransitionTime, reason, message). Condition timestamps represent actual observation/state transitions; do not refresh lastTransitionTime on every read or represent process-local observation continuity as persisted history.

Self-review `Evaluated=True` means a definitive evaluation completed, including a DENY; the decision is `status.resolved.allowed`, not the condition's truth value. Unavailable/unsupported evaluation remains an error, not an Evaluated=False policy denial. Diagnostics use Ready and component conditions; configuration uses Configured, with False for required missing setup, Unknown if its state cannot be established. Bounded redacted details live under resolved. These are observations, not new reconciliation machinery.

## Current User and Batch Semantics

`me` is nullable. Unauthenticated calls return `me=null` and the established authentication error; authenticated calls return trusted `subject`. Explicitly reject absent/malformed/expired identity and the anonymous `subject=anon` / `AuthMethod=none` identity. `auth.ValidTrustedPrincipal` intentionally accepts anonymous principals for public-policy evaluation, so that function alone is not this authenticated guard. No caller-supplied subject/groups. UserDir enrichment owns group/profile authority. Never enrich from arbitrary JWT group claims. Contract tests must cover anonymous, absent, malformed and expired credentials independently.

Example query and variables:

```graphql
query MyAccess($reviews: [SelfSubjectAccessReviewInput!]!) {
  me {
    apiVersion kind metadata { name }
    subject
    selfAccessReviews(reviews: $reviews) {
      apiVersion kind metadata { name namespace }
      spec { principal { subject } targetRef { apiVersion kind name namespace uid resourceVersion } }
      status {
        conditions { type status lastTransitionTime reason }
        resolved {
          allowed reason hypothetical catalogDigest builtinDigest
          revisions { tierID namespaceUID gitRevision tierRevision capturedAt }
        }
      }
    }
  }
}
```

```json
{
  "reviews": [{
        "spec": {
          "action": "product.read",
          "target": {"namespacePath": {"namespace": "catalogue", "name": "blue-shirt"}}
        }
    }]
}
```

The query accepts 1–32 versioned reviews directly, with no key/check wrapper or caller-supplied scope. Return the same number/order of items, including null failures: index i maps to input i. Action <=128 UTF-8 bytes; use owning name/path bounds and IDs <=1024 bytes. Resolve one authorization scope for all reviews before one capture.

## Target Selection Without a Per-Kind Union

The canonical action already identifies its resource kind and authorization scope. Do not ask clients to repeat the kind through product/category/role/etc branches. SelfAccessReviewTargetInput is a fixed @oneOf over selection modes, independent of the number of resource kinds:

| Target mode | Applicability and scope derivation |
| --- | --- |
| id | Existing resource of the action's kind. Use the owning ID resolver, check kind parity, and derive the authoritative namespace incarnation for namespaced actions. Cluster actions resolve to cluster scope. |
| namespacePath | Existing namespaced resource, with the established namespace/name semantics. Resolve through the owning kind's path helper; the actual Namespace UID determines scope. Reject for cluster-scoped actions. |
| inNamespace | Reuse NamespaceBy for the namespace context of a namespaced operation, without selecting an existing child. Resolve its actual UID. A Namespace resource operation such as namespace.read instead uses id and is cluster-scoped. |

The selector contains id, namespacePath and inNamespace. Keep the established namespacePath name for locating a resource; inNamespace explicitly means only its operation context. Example: product.read uses namespacePath {namespace:"catalogue",name:"blue-shirt"}; product.list uses inNamespace {name:"catalogue"}. They are different targets, not aliases. No proposal-specific fields or per-kind union; use owning lookup semantics.

Example existing target: product.read with target: {namespacePath: {namespace: "catalogue", name: "blue-shirt"}}; its id alternative must resolve identically. Existing cluster-scoped resources use id; a separate owning lookup can obtain that ID. Do not infer namespace from decoding an ID alone or comparing unverified path strings.

When supplied, target uses @oneOf and rejects zero/multiple modes or explicit-null selections. Target may be omitted only for an action whose owning contract needs no individual resource (for example deployment diagnostic reads or a cluster-wide create permission check). Its registered action determines cluster scope. A resource-specific action without its required target is BAD_USER_INPUT. Namespaced actions require a target from which namespace can be resolved. Direct Go callers enforce the same rules.

Selectors identify where to evaluate; they do not describe a proposed mutation. Derive only authoritative existing facts and documented operation defaults. For a create/list/context review, return a definitive decision only if the provider can establish it without unknown request-specific facts. If proposed name/tier/visibility/owner or other missing data could change the result, return UNSUPPORTED_CAPABILITY with safe reason REVIEW_CONTEXT_REQUIRED; never fabricate defaults for a user's unspecified proposal, report a definitive denial, or silently bypass conditions. Operation-specific previews are outside this selector-only API. Empty-target cluster checks do not grant permission for arbitrary future payloads.

### One Resolved Authorization Scope Per Batch

1. Validate all request envelopes, modes and action definitions before target resolution/provider capture.
2. Resolve targets using owning helpers without disclosing intermediate facts. Each check produces an internal scope key: CLUSTER or the authoritative Namespace UID/incarnation. A Namespace resource is cluster-scoped; namespacePath applies to a resource inside it, not the Namespace object itself.
3. Require every key to match. Reject a cluster/namespace mixture or two distinct Namespace incarnations as a whole; never partition the batch, capture several policies or silently omit a check. For otherwise disclosable targets use BAD_USER_INPUT with a safe MIXED_AUTHORIZATION_SCOPE reason and no scope inventory. Inaccessible targets retain generic forbidden/not-found behavior rather than disclose their scope through this error.
4. If a check's scope cannot be determined safely (for example an unresolved namespaced opaque ID), reject the batch generically; do not guess scope from another check. Failures with already-known scope may remain per-item after the common capture.
5. Capture one authoritative policy model for the derived scope and revalidate namespace incarnation/resource-version facts using existing validation-start machinery. Namespace deletion/recreation or movement during resolution/capture fails unavailable or scope mismatch; never rebind to a new incarnation by name. Any post-capture lookup must also match the captured scope before evaluation.

Request metadata.namespace, when present, must agree with derived scope; it cannot select/override it. No metadata namespace for cluster actions. Denied decisions still belong to the same captured scope and do not change the batching rule.

Each review is directly a versioned request with defaulted non-null apiVersion/kind, optional shared metadata and required spec. Reject invalid discriminators/metadata before capture, lookups or review-provider effects. Direct Go callers validate explicitly. No batch/key/check wrapper. Results retain the standard envelope and spec/status without a resource lifecycle.

For namespace.create with omitted target, derive cluster scope but do not assume a proposed tier/name; return context-required if the policy/provider cannot decide without them. A product.create namespace selector supplies its actual namespace owner, not a future product name or visibility. Existing-resource reviews retain exact owning context parity. Deployment read checks omit target and derive cluster scope from the canonical action; no fabricated resource name or ID.

## Evaluation and Disclosure

Introduce an optional SelfAccessReviewProvider in internal/auth accepting the trusted principal, checks validated against one target-derived scope, and a deadline, returning authoritative decisions/evidence or ErrNotSupported. Indicate supported action/selection-mode cases explicitly. Public-login descriptions are a separate optional capability; existing bit flags keep their meaning.

RBAC adapter uses existing `ValidationCapture.Begin(ctx, trustedCaller, namespaceUID)` and `Validation.Authorize`, sharing one immutable validation-start capture for all checks. Never call arbitrary-subject `Review`, require `role.access-review.create`, or fabricate groups. Reuse status rendering/digest integrity checks. Namespace scope includes the ordinary cluster and namespace tiers; cluster scope includes only cluster evidence. Capture time is not a global fence and offers no future guarantee.

Use action-owned context builders shared with mutations; derive authoritative target namespace, owner, lifecycle and incarnation. Review does not write policy, resources, sessions or membership. No permission reservation: normal execution checks authorization anew.

Protect lookup disclosure before confirming a resource's existence. An inaccessible ID and an absent ID must be indistinguishable to that caller. Reuse owning read/disclosure rules; if unavailable, fail the review generically. A policy DENY may return `allowed=false` with a sanitized reason but must not reveal matched policy contents. Do not include inaccessible scope evidence or resource names.

Generic `AuthZProvider.Authorize` is not proof of authoritative review support. A non-pinning/non-evidence provider returns unsupported unless it explicitly implements the adapter contract; do not call rbac-local as fallback. A failure to fetch authority is unavailable, never a policy denial. ServiceAccount/user identities use the same provider-owned rules as operations.

The query returns a bounded list of review objects directly, with no Payload or result wrapper. Global validation/capture failures reject the field; individual check failures set selfAccessReviews[i]=null and attach the existing GraphQL error at that index. The returned list has the same length and order as input checks, allowing correlation even when metadata is absent or an item fails. A completed review has the standard envelope, a trusted server-derived spec.principal, normalized requested scope/target, Evaluated condition and existing decision evidence at status.resolved, with hypothetical=false. No parallel top-level subject or new decision enum. Request input still accepts no principal/groups override.

## Error Compatibility

Preserve `admission.Error.ToGQLError`: `errors[].extensions.code`, optional `diagnostics[{reason,message,level,file?,field?}]`, and phase/commit only for relevant admission errors. Preserve response-level `extensions.admission`. Forbidden errors keep the existing diagnostic suppression.

Reuse `BAD_USER_INPUT`, `FORBIDDEN` and existing `AUTHORIZATION_UNAVAILABLE` (`retryable=true`). Preserve existing authentication errors. Add a single catalogued `UNSUPPORTED_CAPABILITY` code, with a safe diagnostic reason for unsupported self-review/action targets; it is not a grant denial and does not reveal internal provider policy. Do not add replacement top-level failure objects to GraphQL responses.

All authenticated GraphQL responses, including mixed operations, are `Cache-Control: private,no-store`; do not vary a public discovery document by principal. A denied protected configuration field returns null/FORBIDDEN and confirms no configuration facts.

## Protected Deployment Fields

Add finite canonical actions during implementation:

| Action | Kind / scope / resource / verb | Authorizes |
| --- | --- | --- |
| `deployment.diagnostics.read` | deployment / cluster / deployment/diagnostics / read | Redacted health/version/schema/publication/provider summary |
| `deployment.configuration.read` | deployment / cluster / deployment/configuration / read | Allowlisted configuration state and safe missing-field names |

Extend the finite action catalog and any static kind/resource validation with exact tests; regenerate with `make generate TARGET=authz`. Add no new grant to ordinary users or `iam.manage`. Existing wildcard cluster administration follows the existing catalog/wildcard semantics; document and test the expanded action set. Deployment is a singleton in-process authorization context, not Git-backed state. Operations use the selected AuthZ provider, never require self-review support just to authorize diagnostic reads.

Authorize before collecting protected probes/configuration. Reuse bounded underlying health/registry functions rather than HTTP-to-self. `apiVersion` is the contract group/version; deployed software version belongs at `status.resolved.softwareVersion`, with safe unknown build data when unavailable. Hash the committed Admin schema for `adminSchemaDigest`. Registry summary exposes only provider-plane identifiers, not raw Policy, tier inventories, group membership or credentials. Version/build/schema/publication/provider/component details are exclusively under diagnostics status.resolved; configuration entries are exclusively under configuration status.resolved.

Configuration entries are a finite explicit projection of implemented settings (publication, authn, authz, userdir and registered optional features). State is CONFIGURED, MISSING or DISABLED; missing fields are safe schema names. No reflective serialization, arbitrary config-key lookup, secret values, credential references or private upstream addresses. Authorized configuration presence indicators are deliberate disclosure; unauthorized requests confirm none of it. Operational health is current observation; publication/support remain distinct.

## Convention Ownership and Drift Prevention

Follow standard envelope/#40 rules and #589 descriptive query arguments/direct returns. selfAccessReviews takes a list of versioned reviews, not correlation wrappers. Preserve shared metadata, references and field placement; no input-named query argument, mutation Payload or new result wrapper. Coordinate with #589 before implementation. Public discovery/OAuth metadata retain their protocol shapes.

Extend existing schema contract suites with AST assertions for top-level envelopes using exactly ObjectMeta; shared ObjectMetaInput reuse; no parallel metadata types; no new query-reachable Payload types; exclusive spec/status versus direct body placement; #40 Condition/resolved status shape; request defaults/nullability; owner-type reuse; and no principal/group input overrides. Add HTTP GraphQL tests for omitted, valid explicit and invalid version/kind/metadata, plus direct Go-call validation and review-provider/store spies proving invalid input causes no effects. Every new root operation/input family must be accounted for; unclassified additions fail. Exceptions need reviewed rationale, not a naming or projection-label bypass.

Generated-source freshness proves code matches SDL; it cannot prove SDL follows conventions or that runtime code validates discriminators. Keep both checks in the existing default `make test` / `make pr-ready` path. Runtime SDL is authoritative after implementation: promote this proposed contract once, then remove its duplicate SDL from the spec or replace it with a source pointer in the same PR. No independently maintained second schema snapshot.

## Normalized Target Reference

spec.targetRef replaces spec.namespace/resourceId/resourceName. Reuse existing CatalogObjectReference (apiVersion, kind, name, namespace, uid, resourceVersion, fieldPath); do not create another object-reference contract. Generalize its catalog-specific docstring during implementation rather than rename existing client types or clone the shape.

For id/namespacePath, targetRef identifies the actual resolved resource, including safely disclosable captured UID/version. uid is the source resource UID, not a Relay-encoded input ID. For inNamespace, targetRef identifies the Namespace context itself: kind=Namespace, name=<slug>, uid=<namespace UID>, namespace=null. The action still determines whether this is a namespaced child-operation context or a cluster-scoped Namespace resource action. Omitted context-free cluster targets produce targetRef=null; do not synthesize a resource.

A reference is not a grant, scope override or promise about future versions. Normalize ID/path selectors to the same reference in parity tests. Policy capture retains authoritative internal scope; no parallel spec.namespace is needed. Correlate null failures by list index, never a missing key. Tests must assert shared CatalogObjectReference identity and the absence of loose reference fields/correlation wrappers.
