// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	units "github.com/docker/go-units"
	"github.com/go-playground/validator/v10"
	"github.com/go-viper/mapstructure/v2"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Config holds the complete application configuration, grouped by owning
// service. gitstore-api owns everything under [api]; [log], [grpc_auth] and
// [push_limits] are shared with gitstore-git-service (and, for log, every
// service reading this file).
type Config struct {
	Api        ApiConfig        `mapstructure:"api"`
	GrpcAuth   GrpcAuthConfig   `mapstructure:"grpc_auth"`
	PushLimits PushLimitsConfig `mapstructure:"push_limits"`
	Log        LogConfig        `mapstructure:"log"`
}

// ApiConfig holds HTTP API server settings and everything gitstore-api owns.
type ApiConfig struct {
	Port     int `mapstructure:"port"      validate:"min=1,max=65535"`
	GitPort  int `mapstructure:"git_port"  validate:"min=1,max=65535"`
	GrpcPort int `mapstructure:"grpc_port" validate:"min=1,max=65535"`

	RateLimit  RateLimitConfig          `mapstructure:"rate_limit"`
	GitService GitServiceEndpointConfig `mapstructure:"git_service"`
	Auth       AuthConfig               `mapstructure:"auth"`
	Datastore  DatastoreConfig          `mapstructure:"datastore"`
	Watch      WatchConfig              `mapstructure:"watch"`
}

// RateLimitConfig is the sustained per-client-IP request rate allowed on
// /graphql before responses are rejected with HTTP 429, plus the token-bucket
// burst size layered on top of it.
type RateLimitConfig struct {
	PerSecond float64 `mapstructure:"per_second" validate:"gt=0"`
	Burst     int     `mapstructure:"burst" validate:"min=1"`
}

// GitServiceEndpointConfig holds the git-service gRPC endpoint URI, in
// dns:///host:port form.
type GitServiceEndpointConfig struct {
	Uri string `mapstructure:"uri" validate:"required"`
}

// AuthConfig holds authentication and JWT settings.
type AuthConfig struct {
	StaticUsers StaticUsersConfig `mapstructure:"static_users"`
	JWT         JWTConfig         `mapstructure:"jwt"`
	AuthN       AuthNConfig       `mapstructure:"authn"`
	AuthZ       AuthZConfig       `mapstructure:"authz"`
	UserDir     UserDirConfig     `mapstructure:"userdir"`
	RBACLocal   RBACConfig        `mapstructure:"rbac_local"`

	// ServiceAccount configures the serviceaccount-assertion/serviceaccount-jwt
	// AuthN providers (spec 061). SigningKey is required only when one of
	// those providers is present in AuthN.Chain (see
	// validateAuthChainConfig), not via a struct `validate:"required"` tag.
	ServiceAccount ServiceAccountConfig `mapstructure:"serviceaccount"`

	// OIDC configures the oidc-jwt AuthN provider: a generic,
	// issuer-agnostic OIDC Relying Party. IssuerURI and ClientID are
	// required only when "oidc-jwt" is present in AuthN.Chain (see
	// validateOIDCAuthChainConfig).
	OIDC OIDCConfig `mapstructure:"oidc_jwt"`
}

// OIDCConfig holds settings for the oidc-jwt AuthN provider: bearer JWTs are
// verified via OIDC Discovery + JWKS against the configured issuer.
type OIDCConfig struct {
	IssuerURI string `mapstructure:"issuer_uri"`
	// ClientID is optional for a pure resource server: it only serves as the
	// default for Audience. Operators who set Audience explicitly need not
	// configure a client_id at all.
	ClientID string `mapstructure:"client_id"`
	// Audience expected in the aud claim. Defaults to ClientID when empty.
	// At least one of Audience/ClientID is required when oidc-jwt is chained.
	Audience string `mapstructure:"audience"`
	// ClockSkew is a duration string restricted to ms|s|m|h.
	ClockSkew time.Duration `mapstructure:"clock_skew"`
	// UsernameClaim selects which token/userinfo claim becomes
	// Principal.Subject (the identity used for role bindings, ownership traits,
	// and audit logs) — the Kubernetes --oidc-username-claim / Spring Security
	// user-name-attribute pattern. Defaults to "sub" (unique and immutable per
	// issuer); "email" or "preferred_username" give human-readable bindings at
	// the cost of stability if the trait changes. The raw sub is always
	// preserved in Principal.Claims["sub"].
	UsernameClaim string `mapstructure:"username_claim"`
}

// ServiceAccountConfig holds settings for GitStore-issued service-account
// identities: JWT assertion verification (proof of possession) and
// short-lived access-token issuance/verification.
type ServiceAccountConfig struct {
	Issuer            string `mapstructure:"issuer"`
	Audience          string `mapstructure:"audience"`
	AssertionAudience string `mapstructure:"assertion_audience"`
	// SigningKey is a PEM-encoded Ed25519 or ECDSA P-256 private key used to
	// sign/verify access tokens. Required only when "serviceaccount-jwt" or
	// "serviceaccount-assertion" is chained in (FR-015c: it must never be
	// resolvable from a config file shared across services).
	SigningKey string        `mapstructure:"signing_key"`
	DefaultTTL time.Duration `mapstructure:"default_ttl"`
	MaxTTL     time.Duration `mapstructure:"max_ttl"`
	ClockSkew  time.Duration `mapstructure:"clock_skew"`
}

// GrpcAuthConfig holds inter-service gRPC authentication settings, shared
// between gitstore-api and gitstore-git-service via the top-level
// [grpc_auth] table.
type GrpcAuthConfig struct {
	HmacSecret string `mapstructure:"hmac_secret" validate:"required"`
}

// AuthNConfig controls the authentication provider chain.
type AuthNConfig struct {
	// Chain is the ordered list of AuthN provider names. Defaults to ["static-users","anonymous"].
	Chain []string `mapstructure:"chain"`
}

// AuthZConfig selects the active authorization provider.
type AuthZConfig struct {
	// Provider is the AuthZ provider name. Defaults to "rbac-local".
	Provider string `mapstructure:"provider"`
}

// UserDirConfig selects the active user-directory provider.
type UserDirConfig struct {
	// Provider is the UserDir provider name. Defaults to "none".
	Provider string `mapstructure:"provider"`
}

// RBACConfig holds rbac-local provider settings.
type RBACConfig struct {
	// PolicyFile is the path to the YAML policy file. Defaults to "policy.yaml".
	PolicyFile string `mapstructure:"policy_file"`
}

// JWTConfig holds JWT token settings.
type JWTConfig struct {
	Secret       string        `mapstructure:"secret"`
	TTL          time.Duration `mapstructure:"ttl"`
	Issuer       string        `mapstructure:"issuer"`
	RefreshGrace time.Duration `mapstructure:"refresh_grace"`
}

// StaticUsersConfig configures the file-backed local user list.
type StaticUsersConfig struct {
	UsersFile string `mapstructure:"users_file"`
}

// WatchConfig holds durable-watch-journal settings shared by every watched
// kind (Namespace, Repository, Product, File, CategoryTaxonomy).
type WatchConfig struct {
	Journal WatchJournalConfig `mapstructure:"journal"`
}

// WatchJournalConfig bounds the CDC-backed durable watch journal and its
// materializer. Durable readers and the materializer are always on — there
// is no watch mechanism besides the journal (eventbus was removed) — so
// every bound here is a hard operational limit, not a feature toggle.
// cdc_retention (14 days) and bucket_size (4096) are fixed by the baseline
// Scylla schema and are not configurable; see JournalCDCRetention and
// JournalBucketSize.
type WatchJournalConfig struct {
	// Retention is how long the journal keeps bookmarks/catalog-up-to-date
	// state before a subscriber must cold-list. Duration string, ms|s|m|h.
	Retention        time.Duration           `mapstructure:"retention" validate:"required"`
	BookmarkInterval time.Duration           `mapstructure:"bookmark_interval" validate:"required"`
	CDC              CDCWatchConfig          `mapstructure:"cdc"`
	Read             ReadWatchConfig         `mapstructure:"read"`
	Poll             PollWatchConfig         `mapstructure:"poll"`
	Subscriber       SubscriberWatchConfig   `mapstructure:"subscriber"`
	Materializer     MaterializerWatchConfig `mapstructure:"materializer"`
}

// CDCWatchConfig bounds how long an event's postimage is awaited before it
// is considered a confirmed CDC delete (vs a reordering artifact).
type CDCWatchConfig struct {
	ConfidenceWindow time.Duration `mapstructure:"confidence_window" validate:"required"`
}

// ReadWatchConfig bounds a single journal read.
type ReadWatchConfig struct {
	BatchSize       int `mapstructure:"batch_size" validate:"min=1"`
	MaxReplayEvents int `mapstructure:"max_replay_events" validate:"min=1,max=100000"`
}

// PollWatchConfig bounds the subscriber's adaptive poll interval.
type PollWatchConfig struct {
	Min time.Duration `mapstructure:"min" validate:"required"`
	Max time.Duration `mapstructure:"max" validate:"required"`
}

// SubscriberWatchConfig bounds a single subscriber's buffered channel.
type SubscriberWatchConfig struct {
	Buffer       int           `mapstructure:"buffer" validate:"min=1,max=256"`
	Backpressure time.Duration `mapstructure:"backpressure" validate:"required"`
}

// MaterializerWatchConfig bounds the CDC materializer's leader lease and
// staleness budget.
type MaterializerWatchConfig struct {
	LeaseTTL           time.Duration `mapstructure:"lease_ttl" validate:"required"`
	LeaseRenewInterval time.Duration `mapstructure:"lease_renew_interval" validate:"required"`
	MaxLag             time.Duration `mapstructure:"max_lag" validate:"required"`
}

// JournalCDCRetention is the fixed Scylla CDC retention window for every
// watched table's full-preimage/postimage CDC log. Not configurable.
const JournalCDCRetention = 14 * 24 * time.Hour

// JournalBucketSize is the fixed journal sequence bucket width, persisted on
// first init of the resource_watch_clock row. Not configurable.
const JournalBucketSize = 4096

// LogConfig holds logger settings.
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

// PushLimitsConfig is the shared static platform push-size ceiling enforced
// by both gitstore-api (admission) and gitstore-git-service (the actual
// clamp during receive-pack). Size strings use IEC units (KiB/MiB/GiB) or
// plain bytes ("B").
type PushLimitsConfig struct {
	MaxPackSize string `mapstructure:"max_pack_size" validate:"required"`
	MaxFileSize string `mapstructure:"max_file_size" validate:"required"`

	// MaxPackSizeBytes/MaxFileSizeBytes are resolved from the strings above
	// during validation; not populated from config sources directly.
	MaxPackSizeBytes int64 `mapstructure:"-"`
	MaxFileSizeBytes int64 `mapstructure:"-"`
}

// DatastoreConfig selects the active storage backend.
type DatastoreConfig struct {
	Backend string       `mapstructure:"backend"`
	Scylla  ScyllaConfig `mapstructure:"scylla"`
}

// ScyllaConfig holds ScyllaDB connection parameters.
// Credentials and TLS are optional (FR-013).
type ScyllaConfig struct {
	AutoMigrate           bool     `mapstructure:"auto_migrate"`
	Hosts                 []string `mapstructure:"hosts"`
	Keyspace              string   `mapstructure:"keyspace"`
	Username              string   `mapstructure:"username"`
	Password              string   `mapstructure:"password"`
	TLS                   bool     `mapstructure:"tls"`
	DisableShardAwarePort bool     `mapstructure:"disable_shard_aware_port"`
	IgnorePeerAddr        bool     `mapstructure:"ignore_peer_addr"`
	// AddressTranslator is an optional runtime-only field (not populated from config files).
	// Set it when Scylla runs behind a NAT (e.g. Docker) to redirect peer addresses.
	AddressTranslator interface{} `mapstructure:"-"`
}

// legacyKey names a removed or renamed configuration path that must fail
// startup rather than silently falling back to a default. replacement names
// the new path (or explains the removal) in operator-facing form.
type legacyKey struct {
	path        string
	replacement string
}

// legacyKeys lists every TOML path gitstore-api used to own before the
// config-grouping refactor. Each one is checked independently of viper's
// final merged state so a renamed/removed key never silently resolves to a
// default. Ordered most-specific-first so the most helpful match wins.
var legacyKeys = []legacyKey{
	{"api.rate_limit_per_second", "api.rate_limit.per_second"},
	{"api.rate_limit_burst", "api.rate_limit.burst"},
	{"git.grpc.uri", "api.git_service.uri"},
	{"git.grpc", "api.git_service.uri"},
	{"git.repo.max_file_size", "push_limits.max_file_size (now a shared static ceiling)"},
	{"git.repo.max_pack_size_bytes", "push_limits.max_pack_size (now a shared static ceiling)"},
	{"git.repo", "push_limits"},
	{"git", "api.git_service.uri / push_limits"},
	{"datastore.backend", "api.datastore.backend"},
	{"datastore.scylla", "api.datastore.scylla"},
	{"datastore", "api.datastore"},
	{"auth.staticusers", "api.auth.static_users"},
	{"auth.grpc.hmac_secret", "grpc_auth.hmac_secret"},
	{"auth.grpc", "grpc_auth"},
	{"auth.jwt.duration", "api.auth.jwt.ttl"},
	{"auth.rbac.policy_file", "api.auth.rbac_local.policy_file"},
	{"auth.rbac", "api.auth.rbac_local"},
	{"auth.oidc", "api.auth.oidc_jwt"},
	{"auth.jwt", "api.auth.jwt"},
	{"auth.authn", "api.auth.authn"},
	{"auth.authz", "api.auth.authz"},
	{"auth.userdir", "api.auth.userdir"},
	{"auth.serviceaccount", "api.auth.serviceaccount"},
	{"auth", "api.auth"},
	{"watch.namespace.readers_enabled", "removed; the durable watch journal reader is always on"},
	{"watch.namespace.materializer_enabled", "removed; the CDC materializer is always on"},
	{"watch.namespace.cdc_retention_seconds", "removed; fixed at 14 days by the baseline schema"},
	{"watch.namespace.bucket_size", "removed; fixed at 4096, persisted on first init"},
	{"watch.namespace.journal_retention_seconds", "api.watch.journal.retention"},
	{"watch.namespace.cdc_confidence_window_millis", "api.watch.journal.cdc.confidence_window"},
	{"watch.namespace.read_batch_size", "api.watch.journal.read.batch_size"},
	{"watch.namespace.max_replay_events", "api.watch.journal.read.max_replay_events"},
	{"watch.namespace.subscriber_buffer", "api.watch.journal.subscriber.buffer"},
	{"watch.namespace.subscriber_backpressure_millis", "api.watch.journal.subscriber.backpressure"},
	{"watch.namespace.poll_min_millis", "api.watch.journal.poll.min"},
	{"watch.namespace.poll_max_millis", "api.watch.journal.poll.max"},
	{"watch.namespace.bookmark_interval_seconds", "api.watch.journal.bookmark_interval"},
	{"watch.namespace.lease_ttl_seconds", "api.watch.journal.materializer.lease_ttl"},
	{"watch.namespace.lease_renew_interval_seconds", "api.watch.journal.materializer.lease_renew_interval"},
	{"watch.namespace.max_materializer_lag_seconds", "api.watch.journal.materializer.max_lag"},
	{"watch.namespace", "api.watch.journal"},
	{"watch", "api.watch"},
	{"features.namespace_repository_fence", "removed; the namespace repository fence is always enabled"},
}

// Load reads configuration from all sources (defaults → config file → env vars)
// and returns the resolved, validated Config.
func Load() (*Config, error) {
	return load(nil)
}

// LoadFrom loads configuration from path. Unlike Load's current-directory
// discovery, an explicitly selected file is required to exist and be readable.
func LoadFrom(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config file path must not be empty")
	}
	return load([]string{path})
}

// LoadFromFiles loads configuration by reading paths[0] and additively
// merging each subsequent path on top (a later file's keys win), so an
// overlay only needs to specify the deltas from the base file. Each path
// is required to exist and be readable.
func LoadFromFiles(paths []string) (*Config, error) {
	if len(paths) == 0 {
		return nil, errors.New("config file path must not be empty")
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return nil, errors.New("config file path must not be empty")
		}
	}
	return load(paths)
}

func load(paths []string) (*Config, error) {
	// .env file is optional; ignore error if absent
	_ = godotenv.Load()

	v := viper.New()

	// Defaults — all known keys must have a default so AutomaticEnv populates them
	// during Unmarshal, even if the default is an empty string.
	v.SetDefault("api.port", 4000)
	v.SetDefault("api.git_port", 9000)
	v.SetDefault("api.grpc_port", 6000)
	v.SetDefault("api.rate_limit.per_second", 50)
	v.SetDefault("api.rate_limit.burst", 100)
	v.SetDefault("api.git_service.uri", "dns:///localhost:50051")
	v.SetDefault("log.level", "info")
	v.SetDefault("log.format", "json")
	v.SetDefault("grpc_auth.hmac_secret", "")
	v.SetDefault("push_limits.max_pack_size", "512MiB")
	v.SetDefault("push_limits.max_file_size", "100MiB")
	v.SetDefault("api.auth.static_users.users_file", "users.yaml")
	v.SetDefault("api.auth.jwt.secret", "")
	v.SetDefault("api.auth.jwt.ttl", "24h")
	v.SetDefault("api.auth.jwt.issuer", "gitstore")
	v.SetDefault("api.auth.jwt.refresh_grace", "60s")
	v.SetDefault("api.auth.authn.chain", []string{"static-users", "anonymous"})
	v.SetDefault("api.auth.authz.provider", "rbac-local")
	v.SetDefault("api.auth.userdir.provider", "none")
	v.SetDefault("api.auth.rbac_local.policy_file", "policy.yaml")
	v.SetDefault("api.auth.serviceaccount.issuer", "gitstore")
	v.SetDefault("api.auth.serviceaccount.audience", "gitstore-api")
	v.SetDefault("api.auth.serviceaccount.assertion_audience", "gitstore-api/serviceaccount-token")
	v.SetDefault("api.auth.serviceaccount.signing_key", "")
	v.SetDefault("api.auth.serviceaccount.default_ttl", "10m")
	v.SetDefault("api.auth.serviceaccount.max_ttl", "1h")
	v.SetDefault("api.auth.serviceaccount.clock_skew", "2m")
	v.SetDefault("api.auth.oidc_jwt.issuer_uri", "")
	v.SetDefault("api.auth.oidc_jwt.client_id", "")
	v.SetDefault("api.auth.oidc_jwt.audience", "")
	v.SetDefault("api.auth.oidc_jwt.clock_skew", "2m")
	v.SetDefault("api.auth.oidc_jwt.username_claim", "sub")
	v.SetDefault("api.datastore.backend", "memdb")
	v.SetDefault("api.datastore.scylla.hosts", []string{"localhost:9042"})
	v.SetDefault("api.datastore.scylla.auto_migrate", true)
	v.SetDefault("api.datastore.scylla.keyspace", "gitstore")
	v.SetDefault("api.datastore.scylla.username", "")
	v.SetDefault("api.datastore.scylla.password", "")
	v.SetDefault("api.datastore.scylla.tls", false)
	v.SetDefault("api.datastore.scylla.disable_shard_aware_port", false)
	v.SetDefault("api.datastore.scylla.ignore_peer_addr", false)
	v.SetDefault("api.watch.journal.retention", "168h")
	v.SetDefault("api.watch.journal.bookmark_interval", "30s")
	v.SetDefault("api.watch.journal.cdc.confidence_window", "500ms")
	v.SetDefault("api.watch.journal.read.batch_size", "256")
	v.SetDefault("api.watch.journal.read.max_replay_events", "100000")
	v.SetDefault("api.watch.journal.subscriber.buffer", "64")
	v.SetDefault("api.watch.journal.subscriber.backpressure", "30s")
	v.SetDefault("api.watch.journal.poll.min", "100ms")
	v.SetDefault("api.watch.journal.poll.max", "2s")
	v.SetDefault("api.watch.journal.materializer.lease_ttl", "30s")
	v.SetDefault("api.watch.journal.materializer.lease_renew_interval", "10s")
	v.SetDefault("api.watch.journal.materializer.max_lag", "60s")

	// Config discovery is optional for compatibility; explicit paths are not.
	// Each path after the first is additively merged on top of the previous
	// ones, so later files only need to specify the keys they override.
	if len(paths) == 0 {
		v.SetConfigName("config")
		v.SetConfigType("toml")
		v.AddConfigPath(".")
		if err := v.ReadInConfig(); err != nil {
			var notFound viper.ConfigFileNotFoundError
			if !errors.As(err, &notFound) {
				return nil, err
			}
		}
	} else {
		for i, p := range paths {
			v.SetConfigFile(p)
			if i == 0 {
				if err := v.ReadInConfig(); err != nil {
					return nil, err
				}
			} else if err := v.MergeInConfig(); err != nil {
				return nil, err
			}
		}
	}

	// Read independently of the merge above: a later overlay that sets
	// api.auth.serviceaccount.signing_key = "" would otherwise erase the shared
	// file's value from v before it's inspected, letting key material that
	// physically still sits in the shared mount (read directly by git-service
	// and controller-manager, bypassing this process's merge order) slip past
	// validateServiceAccountSigningKeySource undetected.
	sharedFileServiceAccountSigningKey, err := signingKeyInFile(paths, sharedServiceConfigMountPath)
	if err != nil {
		return nil, err
	}

	// Environment variables
	v.SetEnvPrefix("GITSTORE")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "__"))
	v.AutomaticEnv()

	if err := checkLegacyKeys(v); err != nil {
		return nil, err
	}

	var cfg Config
	decodeHook := mapstructure.ComposeDecodeHookFunc(
		strictDurationHookFunc,
		mapstructure.StringToSliceHookFunc(","),
	)
	if err := v.Unmarshal(&cfg, viper.DecodeHook(decodeHook)); err != nil {
		return nil, err
	}

	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	if err := validateServiceAccountSigningKeySource(&cfg, paths, sharedFileServiceAccountSigningKey); err != nil {
		return nil, err
	}

	logger, _ := zap.NewProduction()
	defer logger.Sync() //nolint:errcheck

	logger.Info("Configuration loaded", zap.Object("config", &cfg))

	return &cfg, nil
}

// checkLegacyKeys fails startup when any removed/renamed configuration path
// (TOML or env var) is present, naming its replacement. One file is mounted
// into every service, so this cannot use a global strict/deny-unknown-fields
// check — each service checks only the keys it used to own.
func checkLegacyKeys(v *viper.Viper) error {
	for _, legacy := range legacyKeys {
		if v.IsSet(legacy.path) {
			envName := "GITSTORE_" + strings.ToUpper(strings.ReplaceAll(legacy.path, ".", "__"))
			return fmt.Errorf(
				"config: %s (env: %s) is no longer recognised; use %s instead",
				legacy.path, envName, legacy.replacement,
			)
		}
	}
	return nil
}

// validateConfig runs all struct validations and returns a combined error.
func validateConfig(cfg *Config) error {
	validate := validator.New()
	if err := validate.Struct(cfg); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) {
			msgs := make([]string, 0, len(ve))
			for _, fe := range ve {
				msgs = append(msgs, fmt.Sprintf(
					"%s: constraint %q violated (value: %q)",
					fe.StructNamespace(), fe.Tag(), fe.Value(),
				))
			}
			return fmt.Errorf("invalid configuration (%d error(s)):\n  %s", len(msgs), strings.Join(msgs, "\n  "))
		}
		return err
	}
	if err := validateDatastoreConfig(&cfg.Api.Datastore); err != nil {
		return err
	}
	if err := validateAuthChainConfig(cfg); err != nil {
		return err
	}
	if err := validatePushLimitsConfig(&cfg.PushLimits); err != nil {
		return err
	}
	if err := validateWatchJournalConfig(&cfg.Api.Watch.Journal); err != nil {
		return err
	}
	if err := validateServiceAccountAuthChainConfig(&cfg.Api.Auth); err != nil {
		return err
	}
	if err := validateOIDCAuthChainConfig(&cfg.Api.Auth); err != nil {
		return err
	}
	return validateLogFormat(&cfg.Log)
}

func validateAuthChainConfig(cfg *Config) error {
	for _, provider := range cfg.Api.Auth.AuthN.Chain {
		if strings.EqualFold(strings.TrimSpace(provider), "static-users") && cfg.Api.Auth.JWT.Secret == "" {
			return errors.New("startup failed: api.auth.jwt.secret is required\n\n  Problem: static-users is present in api.auth.authn.chain, but api.auth.jwt.secret (env: GITSTORE_API__AUTH__JWT__SECRET) is empty. static-users cannot issue or verify session tokens without it\n\n  To fix, do ONE of the following:\n    1. Set GITSTORE_API__AUTH__JWT__SECRET to a random string (32+ chars). You can generate one with: make secret TARGET=jwt\n    2. If you don't intend to use static-users, remove it from api.auth.authn.chain (GITSTORE_API__AUTH__AUTHN__CHAIN)\n\n  See specs/060-local-multiuser-authn/quickstart.md, step 4, for a worked example")
		}
	}
	return nil
}

// validateOIDCAuthChainConfig enforces that api.auth.oidc_jwt.issuer_uri and
// api.auth.oidc_jwt.client_id are configured when "oidc-jwt" is present in
// api.auth.authn.chain, mirroring validateAuthChainConfig's
// conditional-requirement pattern.
func validateOIDCAuthChainConfig(auth *AuthConfig) error {
	chained := false
	for _, provider := range auth.AuthN.Chain {
		if strings.EqualFold(strings.TrimSpace(provider), "oidc-jwt") {
			chained = true
			break
		}
	}
	if !chained {
		return nil
	}
	if strings.TrimSpace(auth.OIDC.IssuerURI) == "" {
		return errors.New("startup failed: api.auth.oidc_jwt.issuer_uri is required\n\n  Problem: oidc-jwt is present in api.auth.authn.chain, but api.auth.oidc_jwt.issuer_uri (env: GITSTORE_API__AUTH__OIDC_JWT__ISSUER_URI) is empty. oidc-jwt cannot verify bearer tokens without an OIDC issuer to run discovery against\n\n  To fix, do ONE of the following:\n    1. Point api.auth.oidc_jwt.issuer_uri at any standards-compliant OIDC issuer (e.g. the optional reference stack from `make compose IDENTITY=oidc`, Keycloak, Auth0)\n    2. If you don't intend to use OIDC, remove oidc-jwt from api.auth.authn.chain (GITSTORE_API__AUTH__AUTHN__CHAIN)\n\n  See specs/059-optional-oidc-provider/quickstart.md for a worked example")
	}
	if strings.TrimSpace(auth.OIDC.Audience) == "" && strings.TrimSpace(auth.OIDC.ClientID) == "" {
		return errors.New("startup failed: api.auth.oidc_jwt.audience or api.auth.oidc_jwt.client_id is required\n\n  Problem: oidc-jwt is present in api.auth.authn.chain, but neither api.auth.oidc_jwt.audience nor api.auth.oidc_jwt.client_id is set. gitstore-api is a resource server: it must know which aud value to expect — set api.auth.oidc_jwt.audience explicitly, or set api.auth.oidc_jwt.client_id and the audience defaults to it\n\n  To fix, do ONE of the following:\n    1. Set GITSTORE_API__AUTH__OIDC_JWT__AUDIENCE to the audience your clients request (e.g. gitstore)\n    2. Set GITSTORE_API__AUTH__OIDC_JWT__CLIENT_ID to the registered client id (audience defaults to it)\n    3. If you don't intend to use OIDC, remove oidc-jwt from api.auth.authn.chain (GITSTORE_API__AUTH__AUTHN__CHAIN)")
	}
	return nil
}

// serviceAccountChainProviders are the api.auth.authn.chain entries that
// require api.auth.serviceaccount.signing_key to be configured.
var serviceAccountChainProviders = map[string]bool{
	"serviceaccount-jwt":       true,
	"serviceaccount-assertion": true,
}

// chainRequiresServiceAccountSigningKey reports whether chain includes a
// service-account AuthN provider.
func chainRequiresServiceAccountSigningKey(chain []string) bool {
	for _, name := range chain {
		if serviceAccountChainProviders[strings.ToLower(strings.TrimSpace(name))] {
			return true
		}
	}
	return false
}

// validateServiceAccountAuthChainConfig enforces conditional requirements driven by
// api.auth.authn.chain membership: api.auth.serviceaccount.signing_key is
// required only when "serviceaccount-jwt" or "serviceaccount-assertion" is
// chained in, not via a struct `validate:"required"` tag (which would force
// every deployment to set it even when no service-account provider is in use).
func validateServiceAccountAuthChainConfig(auth *AuthConfig) error {
	if chainRequiresServiceAccountSigningKey(auth.AuthN.Chain) && strings.TrimSpace(auth.ServiceAccount.SigningKey) == "" {
		return errors.New(
			"api.auth.serviceaccount.signing_key is required when \"serviceaccount-jwt\" or " +
				"\"serviceaccount-assertion\" is present in api.auth.authn.chain",
		)
	}
	if chainRequiresServiceAccountSigningKey(auth.AuthN.Chain) {
		sa := auth.ServiceAccount
		if sa.DefaultTTL <= 0 {
			return errors.New("api.auth.serviceaccount.default_ttl must be positive")
		}
		if sa.MaxTTL <= 0 {
			return errors.New("api.auth.serviceaccount.max_ttl must be positive")
		}
		if sa.ClockSkew < 0 {
			return errors.New("api.auth.serviceaccount.clock_skew must not be negative")
		}
	}
	return nil
}

// sharedServiceConfigMountPath is the container path GitStore's local/dev
// compose profile (compose.local.yml) mounts a single host config file into
// git-service, api, and controller-manager alike, read-only. Per FR-015c,
// api.auth.serviceaccount.signing_key must never be resolvable from that
// file: doing so would let any of those three services mint or forge a
// service-account access token for the others, bypassing the
// assertion/proof-of-possession flow and every least-privilege guarantee in
// User Story 3.
// A var (not const) so tests can safely override it to a temp path instead
// of writing to the real /config directory on the host.
var sharedServiceConfigMountPath = "/etc/gitstore/gitstore.toml"

// signingKeyInFile reads api.auth.serviceaccount.signing_key from path on its
// own, independent of any other file in paths, if path appears in paths.
// Used instead of inspecting the final merged viper state, because a later
// overlay that sets the key to "" would otherwise erase evidence that the
// shared file itself carries key material — material git-service and
// controller-manager would still read directly from that same file on disk,
// regardless of what this process's merge order computes.
func signingKeyInFile(paths []string, path string) (string, error) {
	if !slices.Contains(paths, path) {
		return "", nil
	}
	single := viper.New()
	single.SetConfigFile(path)
	if err := single.ReadInConfig(); err != nil {
		return "", err
	}
	return single.GetString("api.auth.serviceaccount.signing_key"), nil
}

// validateServiceAccountSigningKeySource enforces FR-015c: refuses startup
// if a service-account AuthN provider is chained in and its signing key was
// sourced from fileSigningKey — the value read from the config file at path
// before environment variables were applied. This specifically targets
// compose.local.yml's shared /etc/gitstore/gitstore.toml mount; an
// env-var-sourced key (even in a container that also mounts that shared file
// for other settings) is unaffected, since the file itself never carries the
// secret.
func validateServiceAccountSigningKeySource(cfg *Config, paths []string, fileSigningKey string) error {
	if !chainRequiresServiceAccountSigningKey(cfg.Api.Auth.AuthN.Chain) {
		return nil
	}
	if !slices.Contains(paths, sharedServiceConfigMountPath) {
		return nil
	}
	if strings.TrimSpace(fileSigningKey) == "" {
		return nil
	}
	return fmt.Errorf(
		"api.auth.serviceaccount.signing_key must not be set in %q: this file is mounted read-only "+
			"into git-service, api, and controller-manager alike (see compose.local.yml), so any "+
			"of those services could forge a service-account access token; instead, mount a "+
			"per-service file containing only the signing key (e.g. "+
			"./config/api/serviceaccount-signing-key.toml -> /config/serviceaccount-signing-key.toml, "+
			"mounted into the api service alone, read-only) and set "+
			"GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY from it, or resolve it from a per-service "+
			"secret store instead",
		sharedServiceConfigMountPath,
	)
}

func validateWatchJournalConfig(w *WatchJournalConfig) error {
	if w.Retention > JournalCDCRetention {
		return fmt.Errorf("invalid api.watch.journal.retention: the baseline schema limits journal retention to %s", JournalCDCRetention)
	}
	if w.Materializer.MaxLag >= JournalCDCRetention {
		return fmt.Errorf("invalid api.watch.journal bounds: materializer.max_lag must be less than the fixed CDC retention of %s", JournalCDCRetention)
	}
	if w.CDC.ConfidenceWindow >= w.Materializer.MaxLag {
		return fmt.Errorf("invalid api.watch.journal bounds: cdc.confidence_window must be less than materializer.max_lag")
	}
	if w.Read.BatchSize > JournalBucketSize {
		return fmt.Errorf("invalid api.watch.journal bounds: read.batch_size must not exceed %d", JournalBucketSize)
	}
	if w.Poll.Min > w.Poll.Max {
		return fmt.Errorf("invalid api.watch.journal bounds: poll.min must not exceed poll.max")
	}
	if w.Poll.Max >= w.Retention {
		return fmt.Errorf("invalid api.watch.journal bounds: poll.max must be less than retention")
	}
	if w.Materializer.LeaseRenewInterval >= w.Materializer.LeaseTTL {
		return fmt.Errorf("invalid api.watch.journal bounds: materializer.lease_renew_interval must be less than materializer.lease_ttl")
	}
	return nil
}

// validateDatastoreConfig validates backend selection and ScyllaDB settings.
func validateDatastoreConfig(ds *DatastoreConfig) error {
	switch strings.ToLower(ds.Backend) {
	case "memdb":
		ds.Backend = "memdb"
		return nil
	case "scylla":
		ds.Backend = "scylla"
		return nil
	default:
		return fmt.Errorf("invalid datastore backend %q; valid values: memdb, scylla", ds.Backend)
	}
}

// iecSizePattern restricts size strings to IEC-unit byte counts (plain bytes,
// or binary-multiple units) so values are portable between Go
// (github.com/docker/go-units) and Rust (bytesize) without the ambiguity of
// decimal (KB/MB/GB) units.
var iecSizePattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(B|KiB|MiB|GiB|TiB)$`)

// ParseIECBytes parses a size string restricted to IEC units (plain "B" or
// binary-multiple KiB/MiB/GiB/TiB), returning the resolved byte count.
func ParseIECBytes(raw string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if !iecSizePattern.MatchString(trimmed) {
		return 0, fmt.Errorf("%q is not a valid size: expected IEC units (B, KiB, MiB, GiB, TiB), e.g. \"512KiB\"", raw)
	}
	bytes, err := units.RAMInBytes(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid size: %w", raw, err)
	}
	return bytes, nil
}

// durationPattern restricts duration strings to a single magnitude with one
// of the ms|s|m|h units (rejecting humantime/Go's ns/us and any multi-unit
// compound form such as "1h30m") so values are portable between Go
// (time.ParseDuration) and Rust (humantime-serde, gated the same way).
var durationPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?(ms|s|m|h)$`)

// strictDurationHookFunc is a viper/mapstructure DecodeHookFuncType that
// converts a config string into time.Duration, restricted to the ms|s|m|h
// grammar. Used in place of mapstructure's default
// StringToTimeDurationHookFunc, which also accepts ns/us and compound
// expressions like "1h30m".
func strictDurationHookFunc(from reflect.Type, to reflect.Type, data any) (any, error) {
	if from.Kind() != reflect.String || to != reflect.TypeFor[time.Duration]() {
		return data, nil
	}
	raw, _ := data.(string)
	if !durationPattern.MatchString(strings.TrimSpace(raw)) {
		return nil, fmt.Errorf("%q is not a valid duration: expected a single magnitude with unit ms, s, m, or h, e.g. \"30s\"", raw)
	}
	return time.ParseDuration(raw)
}

// validatePushLimitsConfig parses and bounds-checks the shared push-size
// ceiling, resolving MaxPackSizeBytes/MaxFileSizeBytes for callers.
func validatePushLimitsConfig(p *PushLimitsConfig) error {
	packBytes, err := ParseIECBytes(p.MaxPackSize)
	if err != nil {
		return fmt.Errorf("invalid push_limits.max_pack_size: %w", err)
	}
	fileBytes, err := ParseIECBytes(p.MaxFileSize)
	if err != nil {
		return fmt.Errorf("invalid push_limits.max_file_size: %w", err)
	}
	if packBytes <= 0 {
		return errors.New("invalid push_limits.max_pack_size: must be greater than zero")
	}
	if fileBytes <= 0 {
		return errors.New("invalid push_limits.max_file_size: must be greater than zero")
	}
	p.MaxPackSizeBytes = packBytes
	p.MaxFileSizeBytes = fileBytes
	return nil
}

// validateLogFormat validates and normalizes the configured log encoding.
func validateLogFormat(log *LogConfig) error {
	switch strings.ToLower(log.Format) {
	case "json":
		log.Format = "json"
		return nil
	case "text":
		log.Format = "text"
		return nil
	default:
		return fmt.Errorf("invalid log format %q; valid values: json, text", log.Format)
	}
}

// MarshalLogObject implements zap.ObjectMarshaler for structured startup logging.
// Sensitive fields are always redacted.
func (c *Config) MarshalLogObject(enc zapcore.ObjectEncoder) error {
	enc.AddInt("api.port", c.Api.Port)
	enc.AddInt("api.git_port", c.Api.GitPort)
	enc.AddInt("api.grpc_port", c.Api.GrpcPort)
	enc.AddString("api.git_service.uri", c.Api.GitService.Uri)
	enc.AddString("api.auth.static_users.users_file", c.Api.Auth.StaticUsers.UsersFile)
	enc.AddString("api.auth.jwt.secret", redact(c.Api.Auth.JWT.Secret))
	enc.AddDuration("api.auth.jwt.ttl", c.Api.Auth.JWT.TTL)
	enc.AddString("api.auth.jwt.issuer", c.Api.Auth.JWT.Issuer)
	enc.AddDuration("api.auth.jwt.refresh_grace", c.Api.Auth.JWT.RefreshGrace)
	enc.AddString("grpc_auth.hmac_secret", redact(c.GrpcAuth.HmacSecret))
	enc.AddString("api.auth.serviceaccount.issuer", c.Api.Auth.ServiceAccount.Issuer)
	enc.AddString("api.auth.serviceaccount.audience", c.Api.Auth.ServiceAccount.Audience)
	enc.AddString("api.auth.serviceaccount.assertion_audience", c.Api.Auth.ServiceAccount.AssertionAudience)
	enc.AddString("api.auth.serviceaccount.signing_key", redact(c.Api.Auth.ServiceAccount.SigningKey))
	enc.AddDuration("api.auth.serviceaccount.default_ttl", c.Api.Auth.ServiceAccount.DefaultTTL)
	enc.AddDuration("api.auth.serviceaccount.max_ttl", c.Api.Auth.ServiceAccount.MaxTTL)
	enc.AddDuration("api.auth.serviceaccount.clock_skew", c.Api.Auth.ServiceAccount.ClockSkew)
	enc.AddString("log.level", c.Log.Level)
	enc.AddString("log.format", c.Log.Format)
	enc.AddString("api.datastore.backend", c.Api.Datastore.Backend)
	enc.AddString("api.datastore.scylla.password", redact(c.Api.Datastore.Scylla.Password))
	enc.AddString("push_limits.max_pack_size", c.PushLimits.MaxPackSize)
	enc.AddString("push_limits.max_file_size", c.PushLimits.MaxFileSize)
	return nil
}

// redact returns "<redacted>" if the value is non-empty, "<unset>" if empty.
func redact(s string) string {
	if s == "" {
		return "<unset>"
	}
	return "<redacted>"
}
