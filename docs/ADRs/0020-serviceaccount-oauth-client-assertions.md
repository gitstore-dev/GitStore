# ADR 0020: Reference-Issuer ServiceAccount Client Assertions

**Status**: Proposed

**Date**: 2026-11-01

**Audience**: workload/app authors, identity administrators and API/controller maintainers.

**Contracts**: [040 Reference-issuer workload authentication](../implementation/040-serviceaccount-oauth-token-exchange.md).

## Context

GitStore ships an optional reference identity stack: an OAuth/OIDC issuer,
a headless identity directory and a browser login/consent bridge. Its current
OAuth client setup uses authorization-code/refresh grants and client-secret
authentication. It does not provision the workload client-assertion path proposed
here. Native ServiceAccounts already use enrolled asymmetric keys and the
GitStore GraphQL token exchange.

[The UserDir proposal](https://github.com/gitstore-dev/GitStore/pull/454)
keeps AuthN, UserDir and AuthZ independent. AuthN establishes identity; UserDir
resolves profile type, active state and groups; AuthZ decides an operation.
Its three directory profiles are staff, customer and serviceAccount. A
ServiceAccount profile is credential-less and login-disabled: GitStore remains
authoritative for workload keys, immutable UID and enablement.

RFC 7523 client assertions can use the reference issuer's existing token
endpoint. Adding another OAuth server inside gitstore-api would duplicate
client registration, replay handling and protocol behavior unnecessarily.
The preferred design therefore extends reference-issuer workload integration,
while preserving native ServiceAccount issuance for deployments without it.

This ADR and its examples are proposed contracts, not implemented support.
The UserDir dependency remains a draft; its group/profile orchestration cannot
be assumed present in today's middleware. No GitStore-owned OAuth token endpoint
is introduced, and existing supported endpoints remain unchanged.

## Decision

### 1. Ownership and independent identity planes

| Responsibility | Authority |
| --- | --- |
| ServiceAccount UID, keys, enablement and lifecycle | GitStore ServiceAccount record |
| OAuth client registration and client-assertion verification | Configured issuer, with a controlled registration projection |
| OAuth access-token signing and assertion replay persistence | Configured issuer |
| Credential-less identity profile, active state and groups | Configured UserDir; reference directory implementation |
| Trusted external workload-to-ServiceAccount mapping | Explicit issuer/client/subject/UID registration managed by GitStore |
| Issuance permission and API operation decisions | Configured AuthZ provider, using enriched authoritative facts |
| Workload private key acquisition | Deployment bootstrap SecretResolver |
| Browser login and consent | Existing reference bridge and human identity/session flow |

The reference implementation uses Hydra as issuer and Kratos as UserDir.
Those are dependencies, not alternative identity authorities for the same facts.
Keep issuer adapters replaceable and the OIDC signature verifier generic.
A non-reference issuer must meet the same workload enrollment, issuance gate,
claims, replay and revocation contracts before support is advertised.

ServiceAccount profiles in the directory must not gain passwords, recovery,
verification, browser sessions or human consent flows. OAuth client-credentials
authentication at the issuer is distinct from logging into a directory account.
No resource CredentialsRef becomes authority to resolve a workload private key.
Native ServiceAccount JWT/assertion paths remain valid with reference UserDir,
and reference issuer operation must not become mandatory for native deployments.

### 2. Enrollment and synchronization

An authorized installer creates or selects a GitStore ServiceAccount, enrolls
public keys and assigns policy using supported provider administration. When
the directory supports profiles, the installation also creates/reconciles a
credential-less serviceAccount profile. Link by explicit AuthN-provider identity,
not by email, display name or coincidentally equal subject strings.

A separately authorized issuer provisioning step registers a workload OAuth
client with client_credentials and private_key_jwt only. It projects approved
public JWKs, audience, short TTL and a versioned GitStore registration identity
into issuer-controlled client state. The private key remains at the workload;
GitStore's API issuer-signing key is never copied to the OAuth issuer.

Each registration binds:
- trusted issuer identity and its exact configured public endpoint;
- OAuth client ID and expected signed access-token subject;
- GitStore ServiceAccount name and immutable UID;
- source enrolled-key revision, projected public-key IDs and registration generation;
- configured API audience, enabled state and permitted workload profile.

Use an immutable client ID such as gs-sa- followed by the ServiceAccount UID
and key-set registration revision. Key-set changes create a new client ID;
overlap is explicit and only retains still-approved key sets. Never mutate an
active client's keys in place and stamp new revision evidence over stale issuer
key caches. Never reuse an old client ID when an account name is deleted/recreated. The
OAuth client ID need not equal the canonical system:serviceaccount subject.
The registration and signed issuer evidence connect them explicitly.

Provisioning across the GitStore store, directory and issuer is a bounded,
idempotent reconciliation process, not an assumed distributed transaction.
Stage clients disabled or without usable grants until authoritative identity,
directory prerequisites and key projection are consistent. GitStore wins any
UID/key/enablement disagreement. Unavailable dependencies leave external
activation pending; they do not redirect issuance to an admin identity.

Changes use expected revisions and record provisioning progress. Reconcile
disable/delete and orphan profiles/issuer clients. Keep enough tombstone/UID
history to prevent stale clients and tokens from binding to a replacement.
Issuer administration is internal-only and available only to the authorized
provisioner. Human directory administration continues through user.* actions;
a token hook is not an enrollment or group administration endpoint.

### 3. OAuth transport and assertion profile

Clients send the client-credentials request to the configured issuer's advertised
token endpoint, /oauth2/token in the reference integration. Do not add
/oauth/token to gitstore-api in this proposal. Use only trusted configured
issuer/discovery metadata, preserve proxy prefixes and never follow a redirect
carrying an assertion.

Authenticate the workload OAuth client with private_key_jwt and the standard
client_assertion_type/client_assertion parameters. Signed iss/sub identify
that OAuth client. The assertion audience identifies the issuer's token
endpoint; the returned token audience identifies GitStore's API. These are
different values. Lifetime, clock leeway, jti and algorithm restrictions are
explicit per tested issuer version.

The immutable UID is bound through the non-reusable registered client and
issuer-controlled claims. A custom sa_uid assertion claim is not required for
this path and cannot override registration. This lets standard JWT-signing
clients participate without trusting caller-supplied account metadata.
Use inline enrolled public JWKs rather than token-selected URLs. Verify ES256
and EdDSA support against the pinned issuer before advertising either.

OAuth scopes bound issuer grants; they do not become GitStore actions, groups
or app capability grants. Restrict client grants to the configured API audience.
Keep browser openid/profile/email/offline_access grants on separate human clients.
This workload contract requests no human login, ID token or refresh token.

### 4. Issuance gate and token evidence

Before returning a workload token, the reference issuer must invoke a mandatory,
authenticated, fail-closed GitStore issuance check. Use the issuer's existing
token-hook capability with a version-specific adapter, not a second token
endpoint. A logical /internal/identity/issuer-token-check route is proposed for
that adapter; it is not an implemented API or a public authentication path.

The callback authenticates the configured issuer using internal deployment
trust, resolves its registered client independently of caller-controlled form
claims, and checks current ServiceAccount UID/enablement, projection revision,
configured audience and permitted grant. It resolves UserDir facts under the
same orchestration as other authenticated identities, then delegates
serviceAccount.token.issue for the mapped account to the active authorizer.

A hook invocation does not itself prove client key possession: issuer hook
ordering must be verified. The issuer must still validate its client assertion
and replay record before issuing. The callback only returns approval and
server-derived identity evidence for that registered candidate. It never
authenticates a caller as a workload merely because a JSON body names one.

The signed access token must carry the current SA UID, registration identity
and enrolled-key projection revision in a reviewed issuer-owned claim namespace.
No groups, profile type, active state or roles are taken from token claims.
Claims are evidence of the issuer's approved mapping, not live authority:
GitStore checks them against its current record/registration again on use.

The reference hook affects all grant types. Dispatch workload checks only for
the approved workload registrations/grants; preserve the existing human
authorization-code/refresh behavior without adding SA claims to human tokens.
An unknown machine client cannot opt into a permissive human branch.
Hook authentication, routing, timeout and retry behavior require explicit tests.

### 5. Authentication, directory enrichment and authorization

For an issuer-issued workload access token:
1. Verify signature, issuer, audience, expiry and token purpose with the configured verifier.
2. Run an explicitly enabled workload identity adapter for that issuer/client profile.
3. Validate signed registration/UID/key-revision evidence against current GitStore identity.
4. Construct the canonical ServiceAccount subject with verified provenance.
5. Resolve UserDir profile, active state and groups using the configured provider link.
6. Apply profile boundaries and call the configured authorizer for the requested operation.

Today's generic oidc-jwt identity normalization rejects reserved ServiceAccount
subjects. Do not bypass it by mapping username_claim or email to a system:
subject. The implementing feature must add a narrowly scoped, provenance-checked
workload adapter and central principal validation, keeping ordinary human OIDC
tokens unable to assert ServiceAccount identity. This is new implementation work.

Directory enrichment also applies to native serviceaccount-assertion and
serviceaccount-jwt before their AuthZ decisions. Enabling this optional issuer
path is not the only way to obtain directory groups. Static and human OIDC
identities use their own explicit provider links; equal raw subjects do not
merge accounts across providers or issuers.

A credential-less serviceAccount profile cannot be reclassified as staff or
customer by token claims. Groups come only from administrator-controlled UserDir
state, are rendered as system:group:name, and cannot inject reserved system
groups. Synthesized groups follow the core identity rules. AuthZ remains
provider-owned; issuer scopes or directory membership alone never grant an action.

The UserDir draft distinguishes:
- configured none/static-users without group capability: no directory groups,
  while legitimate explicit subject grants still work;
- a successful unlinked lookup: no directory profile/groups, with normal
  authorization on those facts rather than another provider's profile;
- a failed/timed-out lookup: retryable fail-closed behavior, never an empty-group
  fallback masquerading as a successful resolution.

External workload registration activation additionally requires its declared
directory prerequisites. A missing required linked profile blocks that
registration; it does not globally change the draft's unlinked-subject semantics.
GitStore disable/deletion always overrides an apparently active directory profile.

### 6. Freshness, revocation and failure boundaries

The directory proposal bounds group removal and deactivation propagation to
five minutes, including re-evaluation or closure of open subscriptions. Reuse
those bounds rather than baking directory groups into access tokens. Tightening
them requires implementation evidence. ServiceAccount record/registration
revocation uses its own separately tested, no-weaker existing identity checks;
directory freshness is not permission to keep a deleted UID valid.

The issuer owns durable single-use assertion replay across issuer replicas.
The native GraphQL verifier retains its own shared replay records. Do not claim
both transports share a jti table or erase either history during rollout.
Distinct audiences and purpose checks prevent cross-protocol assertion reuse;
unique client IDs and UID checks prevent account-name resurrection.

Current enrollment is authoritative even while issuer projections lag. Issuance
checks and resource access both reject stale projected-key revisions. This
proposal adds provenance for external tokens; it does not retroactively give
existing native access tokens enrollment-key provenance. Preserve and document
native revocation semantics independently.

Issuer/client disable, token signing-key rotation, ServiceAccount disable/key
changes and UserDir deactivation are different controls. Test their races
separately. An expired authorization cache or unavailable hook/profile lookup
must not produce an allow decision. Requests already committed retain their
audit evidence; revocation does not roll back business transactions.

Lost token responses can burn assertions. Retry with a fresh signed assertion,
bounded jitter/backoff and coalesced renewal. No automatic fallback to native
issuance after issuer rejection/outage; operators select a transport explicitly.
Reacquire on definitive authentication rejection, not ordinary authorization
denial, and never blindly replay an uncertain business mutation.

### 7. Rollout, evidence and non-goals

The pinned reference Compose images are Hydra v2.3.0 and Kratos v1.3.1.
Current client setup is human-only; login/consent handlers do not implement
the proposed machine token hook. The directory's initial checked-in schema
also predates the three-profile UserDir draft. These are implementation gaps,
not configuration already enabled by this documentation.

Enable external workloads only after compatible enrollment projection,
issuer callback, trusted subject adapter, directory orchestration, replay and
revocation behavior are implemented and tested across replicas. Stage human
and workload clients separately. Retain existing native endpoints, issuer keys,
replay state and deployment choice on rollback. No reference-issuer requirement
is added to native-only deployments.

Acceptance requires two API and two controller replicas, shared issuer/directory
state, pluggable AuthN/UserDir/AuthZ combinations, rolling replacements, directory
and hook outages, blocked self-enrollment, human/customer-to-SA impersonation
attempts, UID recreation, group changes on live watches, and sustained token,
Git push and reconciliation load under bounded queues/caches/timeouts.

Use existing Make command families and parameterized Compose files when
implementing. Configuration/Compose changes require a corresponding quickstart
repository follow-up. This PR changes documents only; no schema, endpoint,
image version, key store or provider implementation is modified.
Git remains singleton-only with retained storage and non-overlapping replacement.

## Alternatives and consequences

Reusing the issuer avoids duplicating the OAuth protocol server and works with
the UserDir model, but introduces issuer enrollment projection and a mandatory
trusted workload adapter. These are substantial boundaries that must be tested,
not inferred from existing human OIDC login support.

A GitStore-owned OAuth endpoint remains a deferred native-only alternative if
required later; it is not part of this preferred reference-stack design.
Making Kratos a second SA key/login authority, importing token groups, accepting
arbitrary system subjects from oidc-jwt, reusing the browser client, or silently
bridging issuer tokens into native bearer tokens is rejected.

## Sources and related contracts

- [UserDir draft, pinned revision](https://github.com/gitstore-dev/GitStore/blob/9306c732904d67d9810f241e0d29fb759177fe22/specs/065-kratos-user-directory/spec.md):
  independent identity planes, per-provider links, credential-less SA profiles and freshness.
- [Reference configuration](../../compose.oidc.yml), [issuer configuration](../../config/oidc/hydra/config.yaml)
  and [directory schema](../../config/oidc/kratos/identity.schema.json): current checked-in baseline.
- [ADR 0009](0009-credential-secret-boundary.md): private-key bootstrap boundary.
- [ADR 0010](0010-authorization-model.md): canonical actions and trusted principal facts.
- [RFC 7523](https://www.rfc-editor.org/rfc/rfc7523.html): JWT client authentication.
- [RFC 6749](https://www.rfc-editor.org/rfc/rfc6749.html): client-credentials exchange.
- [Hydra v2.3.0 token hook](https://github.com/ory/hydra/blob/v2.3.0/oauth2/token_hook.go)
  and [configuration schema](https://github.com/ory/hydra/blob/v2.3.0/.schema/config.schema.json):
  hook context, authenticated calls, response handling and all-grant dispatch.
