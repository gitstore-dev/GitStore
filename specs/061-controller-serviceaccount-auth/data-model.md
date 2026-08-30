# Data Model: Controller-Manager Service-Account Authentication (Phase 1)

## 1. Persistent `ServiceAccount` entity (`gitstore-api`)

Mirrors `datastore.File`'s field shape (spec 051) where applicable; new fields specific to service-account identity are called out.

```go
// gitstore-api/internal/datastore/entities.go

// ServiceAccount is the persistent, namespaced, non-human identity record
// backing the serviceaccount-assertion/serviceaccount-jwt AuthN providers.
// Datastore-backed (memdb + Scylla) from the first implementation phase —
// unlike the assertion-replay cache and WebSocket connection registry,
// which are intentionally in-memory-only (single-instance scope).
type ServiceAccount struct {
	// Identity (primary key: Namespace + Name; subject string is derived as
	// "serviceaccount:<Namespace>:<Name>", never stored redundantly)
	UID       string // stable, survives Disabled toggles; changes only on delete+recreate
	Namespace string // convention string, e.g. "controllers" — not GitStore's Namespace resource
	Name      string // e.g. "category-taxonomy"

	Disabled bool // true blocks new assertion exchange and new access-token authentication immediately

	Generation      int64  // advances only on PublicKeys change (author-controlled state)
	ResourceVersion string // advances on every persisted change, including Disabled toggles

	CreationTimestamp time.Time
	CreationActor     string // subject of the admin principal that created this record
	UpdateTimestamp   time.Time
	UpdateActor       string

	PublicKeys []ServiceAccountPublicKey

	DeletionTimestamp *time.Time // set on deleteServiceAccount; hard-delete is immediate in Phase 1 (no finalizer/Terminating lifecycle — a ServiceAccount has no dependent catalog resources to drain, unlike Namespace/Repository)
}

// ServiceAccountPublicKey is one enrolled public key, supporting an overlap
// window during rotation (multiple entries may be simultaneously valid).
type ServiceAccountPublicKey struct {
	KeyID     string // "kid" — protected-header value an assertion's kid must match
	Algorithm string // "Ed25519" (preferred) or "ECDSA-P256"
	PublicKey []byte // raw public key bytes (PEM-decoded at load, stored decoded)
	EnrolledAt time.Time
}
```

**Why no `Terminating`/finalizer lifecycle (unlike `Namespace`/`Repository`)**: a `ServiceAccount` has no dependent catalog resources (Product, CategoryTaxonomy, etc.) that could be silently orphaned by an immediate hard-delete — the only thing that references it is the `role_bindings` map in `policy.yaml`, which is a separate, hand-edited config file `deleteServiceAccount` does not and cannot mutate (exactly the layering boundary spec 060's research.md Decision 4 already establishes for the analogous `static-users`/`policy.yaml` relationship). `deleteServiceAccount` therefore deletes the record immediately; FR-005/FR-019 govern the *authentication-consequence* ordering (deny future auth, cancel live connections), not a drain-then-remove state machine.

### `Datastore` interface additions

```go
// gitstore-api/internal/datastore/datastore.go

// ServiceAccount operations
CreateServiceAccount(ctx context.Context, sa *ServiceAccount) error
GetServiceAccountByUID(ctx context.Context, uid string) (*ServiceAccount, error)
GetServiceAccountBySubject(ctx context.Context, namespace, name string) (*ServiceAccount, error)
ListServiceAccounts(ctx context.Context, page PageParams) (*PageResult[ServiceAccount], error)
// UpdateServiceAccountKeys adds/removes public keys and advances Generation;
// fails if expectedResourceVersion is stale (optimistic concurrency, matching
// UpdateFile/UpdateRepository's existing contract).
UpdateServiceAccountKeys(ctx context.Context, uid string, add []ServiceAccountPublicKey, removeKeyIDs []string, expectedResourceVersion string) (*ServiceAccount, error)
// SetServiceAccountDisabled toggles Disabled without touching PublicKeys or Generation.
SetServiceAccountDisabled(ctx context.Context, uid string, disabled bool) error
DeleteServiceAccount(ctx context.Context, uid string) error
```

### Scylla migration

`gitstore-api/internal/datastore/scylla/migrations/006_service_account.cql` (numbered immediately after `005_namespace_repository_fence.cql`, the latest existing migration after spec 047 / PR #370 landed):

```sql
CREATE TABLE IF NOT EXISTS service_accounts (
    uid              text PRIMARY KEY,
    namespace        text,
    name             text,
    disabled         boolean,
    generation       bigint,
    resource_version text,
    creation_ts      timestamp,
    creation_actor   text,
    update_ts        timestamp,
    update_actor     text,
    public_keys      text, -- JSON-encoded []ServiceAccountPublicKey, mirroring how Status/Spec json.RawMessage columns already store structured data elsewhere
    deletion_ts      timestamp
);

CREATE INDEX IF NOT EXISTS service_accounts_by_subject ON service_accounts (namespace, name);
```

## 2. Access-token claims (issued by `serviceaccount-jwt`'s issuer half; carried forward verbatim from doc 021 §9a)

| Claim | Meaning | Trust rule |
|---|---|---|
| `iss` | `auth.serviceaccount.issuer` (default `"gitstore"`) | Verifier MUST check exact match first — decides "is this even my token" (`OutcomeChallenge` vs. proceed). |
| `sub` | `serviceaccount:<namespace>:<name>` | Never accepted from an untrusted caller at issuance — derived from the `ServiceAccount` record being requested, gated by `issueServiceAccountToken`'s own authz. |
| `aud` | `["gitstore-api"]` | Verifier MUST reject if its own identifier is absent. |
| `exp` | issuance time + `clamp(requested TTL, max_ttl)` | MUST NOT exceed `auth.serviceaccount.max_ttl` regardless of requested value. |
| `iat` | issuance time | Informational only — access tokens are never refresh credentials. |
| `nbf` | equal to `iat` | Checked with `clock_skew` leeway. |
| `jti` | random UUID | Supports optional per-token revocation and connection observability. |
| `sa_uid` (private claim) | `ServiceAccount.UID` at issuance time | **Never accepted from an untrusted caller.** Guards against a deleted-then-recreated namespace/name pair being confused with its predecessor (FR-005/User Story 2 Acceptance Scenario 7). |
| `roles` | **absent** | Roles are resolved exclusively by `rbac-local`'s `role_bindings`, never embedded (FR-011). |

Signing: asymmetric only (Ed25519 preferred, ECDSA P-256 acceptable — FR-012). `github.com/golang-jwt/jwt/v5` already supports `jwt.SigningMethodEdDSA`; no new dependency.

## 3. Client-assertion claims (verified by `serviceaccount-assertion`; carried forward verbatim from doc 021 §9b)

| Field | Requirement |
|---|---|
| protected header `typ` | Exactly `gitstore-sa-assertion+jwt` |
| protected header `kid` | Must match one of the target `ServiceAccount`'s currently-enrolled `PublicKeys[].KeyID` |
| `iss` | Equal to the `ServiceAccount` subject |
| `sub` | Equal to `iss` |
| `sa_uid` | Equal to `ServiceAccount.UID` |
| `aud` | Exactly `auth.serviceaccount.assertion_audience` (default `gitstore-api/serviceaccount-token`) |
| `iat`/`nbf`/`exp` | `exp - iat <= 60s`, checked with `clock_skew` leeway |
| `jti` | Accepted once within the replay window (in-memory, single-instance scope per Assumptions) |

## 4. Config keys

### `gitstore-api` (`internal/config/config.go`)

```go
type AuthConfig struct {
	// ...existing fields unchanged (Admin/JWT/Grpc/AuthN/AuthZ/UserDir/RBAC)...
	ServiceAccount ServiceAccountConfig `mapstructure:"serviceaccount"`
}

type ServiceAccountConfig struct {
	Issuer            string `mapstructure:"issuer"`             // GITSTORE_AUTH__SERVICEACCOUNT__ISSUER, default "gitstore"
	Audience          string `mapstructure:"audience"`           // GITSTORE_AUTH__SERVICEACCOUNT__AUDIENCE, default "gitstore-api"
	AssertionAudience string `mapstructure:"assertion_audience"` // GITSTORE_AUTH__SERVICEACCOUNT__ASSERTION_AUDIENCE, default "gitstore-api/serviceaccount-token"
	SigningKey        string `mapstructure:"signing_key"`        // GITSTORE_AUTH__SERVICEACCOUNT__SIGNING_KEY, PEM, Ed25519/ECDSA — required only if serviceaccount-jwt/serviceaccount-assertion is chained in (mirrors spec 060's validateAuthChainConfig conditional-requirement pattern, not validate:"required")
	DefaultTTL        string `mapstructure:"default_ttl"`        // GITSTORE_AUTH__SERVICEACCOUNT__DEFAULT_TTL, default "10m"
	MaxTTL            string `mapstructure:"max_ttl"`            // GITSTORE_AUTH__SERVICEACCOUNT__MAX_TTL, default "1h"
	ClockSkew         string `mapstructure:"clock_skew"`         // GITSTORE_AUTH__SERVICEACCOUNT__CLOCK_SKEW, default "2m"
}
```

`auth.authn.chain` default remains `["static-admin","anonymous"]` unchanged (or, once spec 060 merges, `["static-users","anonymous"]`) — `serviceaccount-assertion`/`serviceaccount-jwt` are additive chain entries an operator opts into, e.g. `["static-users","serviceaccount-assertion","serviceaccount-jwt","anonymous"]`.

### `gitstore-controller-manager` (`internal/config/config.go`)

```go
type ControllerConfig struct {
	// ...existing ApiURI/ApiToken/... unchanged; ApiToken's doc comment
	// updated to state it is a deprecated dev/CI fallback used only when no
	// ServiceAccount signer below is configured...
	ServiceAccountNamespace string `mapstructure:"serviceaccount_namespace"` // GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE, e.g. "controllers"
	ServiceAccountName      string `mapstructure:"serviceaccount_name"`      // GITSTORE_CONTROLLER__SERVICEACCOUNT__NAME, default "category-taxonomy"
	ServiceAccountKeyID     string `mapstructure:"serviceaccount_key_id"`    // GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_ID

	// ServiceAccountKeyRef locates the signing private key as an ADR 0001
	// SecretRef, resolved through a bootstrap-tier SecretResolver per
	// ADR 0009 §3. This deliberately replaces a bespoke
	// GITSTORE_CONTROLLER__SERVICEACCOUNT__PRIVATE_KEY_FILE path: components
	// MUST NOT read secret material directly from a filesystem path.
	ServiceAccountKeyRef secret.Ref `mapstructure:"serviceaccount_key_ref"` // GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__{NAME,KEY}

	// SecretProviderBootstrap binds the bootstrap-tier provider (file | env |
	// k8s | vault | aws-secrets-manager). "file" is the local-development and
	// Docker-Compose default. ADR 0009 §3.
	SecretProviderBootstrap secret.BootstrapProviderConfig `mapstructure:"secret_provider_bootstrap"` // GITSTORE_CONTROLLER__SECRET_PROVIDER_BOOTSTRAP__*
}
```

Precedence (FR-015): if `ServiceAccountKeyRef` is set, `ServiceAccountSource` is used, with the key resolved at startup through the configured bootstrap-tier resolver; otherwise, if `ApiToken` is set, `StaticToken` is used; otherwise, startup fails (no credential source configured at all) — mirroring today's existing `ApiToken`-required validation, just made conditional on no signer being configured. A bootstrap resolution failure is fatal and fails closed using ADR 0001's error classes (`NotFound`, `MissingKey`, `Forbidden`, `ProviderUnavailable`), never silently degrading to `StaticToken`.

## 5. `gitstore-controller-manager`-side `Credential`/`CredentialSource` shapes

```go
// gitstore-controller-manager/internal/graphqlclient/credential.go

type Credential struct {
	AccessToken string
	ExpiresAt   time.Time // zero value means "does not expire" (StaticToken)
}

type CredentialSource interface {
	Current(ctx context.Context) (Credential, error)
}

// StaticToken wraps GITSTORE_CONTROLLER__API_TOKEN — the deprecated dev/CI
// compatibility path (FR-014). Never renews.
type StaticToken struct{ Token string }

func (s StaticToken) Current(context.Context) (Credential, error) {
	return Credential{AccessToken: s.Token}, nil
}

// ServiceAccountSource signs a short-lived client assertion with an enrolled
// private key, exchanges it via issueServiceAccountToken, and proactively
// renews at ExpiresAt minus a configured refresh threshold. Constructed
// exactly once in cmd/controller/main.go and shared across
// registerNamespace/registerCategoryTaxonomy/registerProductWatch
// (research.md Decision 3 — not one independent instance per call site).
type ServiceAccountSource struct {
	// namespace, name, keyID, private key/signer, issuer HTTP client,
	// mutex-guarded current Credential, singleflight, renewal goroutine
}
```

`graphqlclient.Client.token string` becomes `Client.credentials CredentialSource`; `do()`/`Subscribe()` call `c.credentials.Current(ctx)` in place of reading a field.
