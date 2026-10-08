// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearEnv unsets every GITSTORE_ env var touched by this file (new-schema
// and legacy) and returns a restore function, so a developer's shell or a
// prior test's os.Setenv call never leaks into the next test.
func clearEnv(t *testing.T) func() {
	t.Helper()
	keys := []string{
		// Current schema.
		"GITSTORE_API__PORT",
		"GITSTORE_API__GIT_PORT",
		"GITSTORE_API__GRPC_PORT",
		"GITSTORE_API__RATE_LIMIT__PER_SECOND",
		"GITSTORE_API__RATE_LIMIT__BURST",
		"GITSTORE_API__GIT_SERVICE__URI",
		"GITSTORE_LOG__LEVEL",
		"GITSTORE_LOG__FORMAT",
		"GITSTORE_GRPC_AUTH__HMAC_SECRET",
		"GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE",
		"GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE",
		"GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE",
		"GITSTORE_API__AUTH__JWT__SECRET",
		"GITSTORE_API__AUTH__JWT__TTL",
		"GITSTORE_API__AUTH__JWT__ISSUER",
		"GITSTORE_API__AUTH__JWT__REFRESH_GRACE",
		"GITSTORE_API__AUTH__AUTHN__CHAIN",
		"GITSTORE_API__AUTH__AUTHZ__PROVIDER",
		"GITSTORE_API__AUTH__USERDIR__PROVIDER",
		"GITSTORE_API__AUTH__RBAC_LOCAL__POLICY_FILE",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__ISSUER",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__AUDIENCE",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__ASSERTION_AUDIENCE",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__DEFAULT_TTL",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__MAX_TTL",
		"GITSTORE_API__AUTH__SERVICEACCOUNT__CLOCK_SKEW",
		"GITSTORE_API__AUTH__OIDC_JWT__ISSUER_URI",
		"GITSTORE_API__AUTH__OIDC_JWT__CLIENT_ID",
		"GITSTORE_API__AUTH__OIDC_JWT__AUDIENCE",
		"GITSTORE_API__AUTH__OIDC_JWT__CLOCK_SKEW",
		"GITSTORE_API__AUTH__OIDC_JWT__USERNAME_CLAIM",
		"GITSTORE_API__DATASTORE__BACKEND",
		"GITSTORE_API__DATASTORE__SCYLLA__HOSTS",
		"GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE",
		"GITSTORE_API__DATASTORE__SCYLLA__KEYSPACE",
		"GITSTORE_API__DATASTORE__SCYLLA__USERNAME",
		"GITSTORE_API__DATASTORE__SCYLLA__PASSWORD",
		"GITSTORE_API__DATASTORE__SCYLLA__TLS",
		"GITSTORE_API__DATASTORE__SCYLLA__DISABLE_SHARD_AWARE_PORT",
		"GITSTORE_API__DATASTORE__SCYLLA__IGNORE_PEER_ADDR",
		"GITSTORE_API__WATCH__JOURNAL__RETENTION",
		"GITSTORE_API__WATCH__JOURNAL__BOOKMARK_INTERVAL",
		"GITSTORE_API__WATCH__JOURNAL__CDC__CONFIDENCE_WINDOW",
		"GITSTORE_API__WATCH__JOURNAL__READ__BATCH_SIZE",
		"GITSTORE_API__WATCH__JOURNAL__READ__MAX_REPLAY_EVENTS",
		"GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BUFFER",
		"GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BACKPRESSURE",
		"GITSTORE_API__WATCH__JOURNAL__POLL__MIN",
		"GITSTORE_API__WATCH__JOURNAL__POLL__MAX",
		"GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__LEASE_TTL",
		"GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__LEASE_RENEW_INTERVAL",
		"GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__MAX_LAG",
		// Legacy keys exercised by rejection tests.
		"GITSTORE_API__RATE_LIMIT_PER_SECOND",
		"GITSTORE_API__RATE_LIMIT_BURST",
		"GITSTORE_GIT__GRPC__URI",
		"GITSTORE_DATASTORE__BACKEND",
		"GITSTORE_AUTH__STATICUSERS__USERS_FILE",
		"GITSTORE_AUTH__GRPC__HMAC_SECRET",
		"GITSTORE_AUTH__JWT__DURATION",
		"GITSTORE_WATCH__NAMESPACE__READERS_ENABLED",
		"GITSTORE_WATCH__NAMESPACE__MATERIALIZER_ENABLED",
		"GITSTORE_WATCH__NAMESPACE__BUCKET_SIZE",
		"GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE",
	}
	saved := make(map[string]string, len(keys))
	for _, k := range keys {
		saved[k] = os.Getenv(k)
		os.Unsetenv(k)
	}
	return func() {
		for k, v := range saved {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}
}

// setRequiredAuth sets the required auth env vars including the gRPC HMAC secret.
func setRequiredAuth(t *testing.T) {
	t.Helper()
	os.Setenv("GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE", "config/users.yaml")
	os.Setenv("GITSTORE_API__AUTH__JWT__SECRET", "supersecretkey-minimum-32-chars!!")
	os.Setenv("GITSTORE_GRPC_AUTH__HMAC_SECRET", "ci-test-grpc-hmac-secret")
}

// T005: layered loading tests

func TestLoad_DefaultsAppliedWhenNoSourceSet(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 4000, cfg.Api.Port)
	assert.Equal(t, 9000, cfg.Api.GitPort)
	assert.Equal(t, 6000, cfg.Api.GrpcPort)
	assert.Equal(t, float64(50), cfg.Api.RateLimit.PerSecond)
	assert.Equal(t, 100, cfg.Api.RateLimit.Burst)
	assert.Equal(t, "dns:///localhost:50051", cfg.Api.GitService.Uri)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)

	assert.Equal(t, 24*time.Hour, cfg.Api.Auth.JWT.TTL)
	assert.Equal(t, "gitstore", cfg.Api.Auth.JWT.Issuer)
	assert.Equal(t, 60*time.Second, cfg.Api.Auth.JWT.RefreshGrace)

	assert.Equal(t, "gitstore", cfg.Api.Auth.ServiceAccount.Issuer)
	assert.Equal(t, "gitstore-api", cfg.Api.Auth.ServiceAccount.Audience)
	assert.Equal(t, "gitstore-api/serviceaccount-token", cfg.Api.Auth.ServiceAccount.AssertionAudience)
	assert.Equal(t, "", cfg.Api.Auth.ServiceAccount.SigningKey)
	assert.Equal(t, 10*time.Minute, cfg.Api.Auth.ServiceAccount.DefaultTTL)
	assert.Equal(t, time.Hour, cfg.Api.Auth.ServiceAccount.MaxTTL)
	assert.Equal(t, 2*time.Minute, cfg.Api.Auth.ServiceAccount.ClockSkew)

	assert.Equal(t, "memdb", cfg.Api.Datastore.Backend)

	// Durable watch journal: always on, bounded by the following defaults.
	assert.Equal(t, 168*time.Hour, cfg.Api.Watch.Journal.Retention)
	assert.Equal(t, 30*time.Second, cfg.Api.Watch.Journal.BookmarkInterval)
	assert.Equal(t, 500*time.Millisecond, cfg.Api.Watch.Journal.CDC.ConfidenceWindow)
	assert.Equal(t, 256, cfg.Api.Watch.Journal.Read.BatchSize)
	assert.Equal(t, 100000, cfg.Api.Watch.Journal.Read.MaxReplayEvents)
	assert.Equal(t, 64, cfg.Api.Watch.Journal.Subscriber.Buffer)
	assert.Equal(t, 30*time.Second, cfg.Api.Watch.Journal.Subscriber.Backpressure)
	assert.Equal(t, 100*time.Millisecond, cfg.Api.Watch.Journal.Poll.Min)
	assert.Equal(t, 2*time.Second, cfg.Api.Watch.Journal.Poll.Max)
	assert.Equal(t, 30*time.Second, cfg.Api.Watch.Journal.Materializer.LeaseTTL)
	assert.Equal(t, 10*time.Second, cfg.Api.Watch.Journal.Materializer.LeaseRenewInterval)
	assert.Equal(t, 60*time.Second, cfg.Api.Watch.Journal.Materializer.MaxLag)

	assert.Equal(t, "512MiB", cfg.PushLimits.MaxPackSize)
	assert.Equal(t, "100MiB", cfg.PushLimits.MaxFileSize)
	assert.Equal(t, int64(512*1024*1024), cfg.PushLimits.MaxPackSizeBytes)
	assert.Equal(t, int64(100*1024*1024), cfg.PushLimits.MaxFileSizeBytes)
}

func TestLoad_WatchJournalEnvOverrides(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__CDC__CONFIDENCE_WINDOW", "750ms")
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__READ__BATCH_SIZE", "128")
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BUFFER", "32")
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BACKPRESSURE", "1500ms")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 750*time.Millisecond, cfg.Api.Watch.Journal.CDC.ConfidenceWindow)
	assert.Equal(t, 128, cfg.Api.Watch.Journal.Read.BatchSize)
	assert.Equal(t, 32, cfg.Api.Watch.Journal.Subscriber.Buffer)
	assert.Equal(t, 1500*time.Millisecond, cfg.Api.Watch.Journal.Subscriber.Backpressure)
}

func TestLoad_RejectsJournalRetentionAboveFixedCDCRetention(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__RETENTION", "337h")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.watch.journal.retention")
	assert.Contains(t, err.Error(), "limits journal retention")
}

func TestLoad_RejectsMaterializerMaxLagAtFixedCDCRetention(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__MAX_LAG", "337h")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "materializer.max_lag must be less than the fixed CDC retention")
	assert.Contains(t, err.Error(), JournalCDCRetention.String())
}

func TestLoad_RejectsCDCConfidenceWindowAtMaterializerMaxLag(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	// Default materializer.max_lag is 60s; set confidence_window equal to it.
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__CDC__CONFIDENCE_WINDOW", "60s")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cdc.confidence_window must be less than materializer.max_lag")
}

func TestLoad_AcceptsCDCConfidenceWindowBelowMaterializerMaxLag(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__CDC__CONFIDENCE_WINDOW", "59999ms")

	_, err := Load()
	require.NoError(t, err)
}

func TestLoad_RejectsReadBatchSizeAboveFixedBucketSize(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__READ__BATCH_SIZE", "4097")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read.batch_size must not exceed 4096")
}

func TestLoad_RejectsPollMinAbovePollMax(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__POLL__MIN", "3s")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll.min must not exceed poll.max")
}

func TestLoad_RejectsPollMaxAtJournalRetention(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	// Default poll.max is 2s; set retention equal to it.
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__RETENTION", "2s")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "poll.max must be less than retention")
}

func TestLoad_AcceptsPollMaxBelowJournalRetention(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__RETENTION", "3s")

	_, err := Load()
	require.NoError(t, err)
}

func TestLoad_RejectsMaterializerLeaseRenewIntervalAtLeaseTTL(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	// Default materializer.lease_ttl is 30s; set renew interval equal to it.
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__MATERIALIZER__LEASE_RENEW_INTERVAL", "30s")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "materializer.lease_renew_interval must be less than materializer.lease_ttl")
}

func TestLoad_RejectsOversizedSubscriberBuffer(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__SUBSCRIBER__BUFFER", "257")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Watch.Journal.Subscriber.Buffer")
	assert.Contains(t, err.Error(), "constraint \"max\" violated")
}

func TestLoad_RejectsOversizedReadMaxReplayEvents(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__WATCH__JOURNAL__READ__MAX_REPLAY_EVENTS", "100001")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Watch.Journal.Read.MaxReplayEvents")
	assert.Contains(t, err.Error(), "constraint \"max\" violated")
}

func TestLoad_EnvVarOverridesDefault(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__PORT", "8888")
	os.Setenv("GITSTORE_API__RATE_LIMIT__PER_SECOND", "5")
	os.Setenv("GITSTORE_API__RATE_LIMIT__BURST", "15")
	os.Setenv("GITSTORE_LOG__LEVEL", "debug")
	os.Setenv("GITSTORE_LOG__FORMAT", "text")
	os.Setenv("GITSTORE_API__AUTH__JWT__REFRESH_GRACE", "30s")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8888, cfg.Api.Port)
	assert.Equal(t, float64(5), cfg.Api.RateLimit.PerSecond)
	assert.Equal(t, 15, cfg.Api.RateLimit.Burst)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, 30*time.Second, cfg.Api.Auth.JWT.RefreshGrace)
}

func TestLoad_ConfigFileValueAppliedWhenNoEnvVar(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := `[log]
level = "warn"
format = "text"

[api]
port = 7777

[api.auth.jwt]
refresh_grace = "45s"
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	// Load() must discover config.toml from working directory.
	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 7777, cfg.Api.Port)
	assert.Equal(t, "warn", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, 45*time.Second, cfg.Api.Auth.JWT.RefreshGrace)
}

func TestLoad_EnvVarOverridesConfigFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := "[api]\nport = 7777\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	os.Setenv("GITSTORE_API__PORT", "9999")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 9999, cfg.Api.Port)
}

func TestLoadFrom_ExplicitSharedFileAndEnvPrecedence(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := filepath.Join(t.TempDir(), "shared.toml")
	content := `[api]
port = 7111
[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[controller]
port = 5001
[grpc]
port = 50051
[hooks.git_receive_pack]
pre_receive = { enabled = true }
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	t.Setenv("GITSTORE_API__PORT", "7222")

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.Equal(t, 7222, cfg.Api.Port)
}

func TestLoadFrom_MissingExplicitFileFails(t *testing.T) {
	_, err := LoadFrom(filepath.Join(t.TempDir(), "missing.toml"))
	require.Error(t, err)
}

func TestScyllaAutoMigrationCanBeDisabled(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(""), 0600))
	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	require.True(t, cfg.Api.Datastore.Scylla.AutoMigrate)
	require.NoError(t, os.WriteFile(path, []byte("[api.datastore.scylla]\nauto_migrate = false\n"), 0600))
	cfg, err = LoadFrom(path)
	require.NoError(t, err)
	require.False(t, cfg.Api.Datastore.Scylla.AutoMigrate)
	t.Setenv("GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE", "true")
	cfg, err = LoadFrom(path)
	require.NoError(t, err)
	require.True(t, cfg.Api.Datastore.Scylla.AutoMigrate)
}

func TestLoadFromFiles_OverlayMergesOnTopOfBase(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	dir := t.TempDir()

	basePath := filepath.Join(dir, "base.toml")
	base := `
[api]
port = 4000
[api.git_service]
uri = "dns:///localhost:50051"
[api.auth.jwt]
secret = "base-secret-at-least-32-characters-x"
[grpc_auth]
hmac_secret = "base-hmac-secret"
[api.datastore]
backend = "memdb"
`
	require.NoError(t, os.WriteFile(basePath, []byte(base), 0600))

	overlayPath := filepath.Join(dir, "overlay.toml")
	overlay := `
[api.datastore]
backend = "scylla"
[api.datastore.scylla]
hosts = ["scylla:9042"]
keyspace = "gitstore"
`
	require.NoError(t, os.WriteFile(overlayPath, []byte(overlay), 0600))

	cfg, err := LoadFromFiles([]string{basePath, overlayPath})
	require.NoError(t, err)
	// Overlay-only key wins.
	assert.Equal(t, "scylla", cfg.Api.Datastore.Backend)
	assert.Equal(t, []string{"scylla:9042"}, cfg.Api.Datastore.Scylla.Hosts)
	// Base-only key is preserved, not wiped by the overlay.
	assert.Equal(t, 4000, cfg.Api.Port)
	assert.Equal(t, "dns:///localhost:50051", cfg.Api.GitService.Uri)
}

func TestLoadFromFiles_MissingOverlayFails(t *testing.T) {
	dir := t.TempDir()
	basePath := filepath.Join(dir, "base.toml")
	require.NoError(t, os.WriteFile(basePath, []byte("[api]\nport = 4000\n"), 0600))

	_, err := LoadFromFiles([]string{basePath, filepath.Join(dir, "missing-overlay.toml")})
	require.Error(t, err)
}

func TestLoadFromFiles_EmptyFails(t *testing.T) {
	_, err := LoadFromFiles(nil)
	require.Error(t, err)
}

// T007: startup log redaction test

func TestLoad_StartupLogRedactsSensitiveFields(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Sensitive fields must not appear in the log representation.
	// We test via the MarshalLogObject-based redact helper indirectly:
	// JWT secrets must be redacted; users are loaded from a separate file.
	assert.Equal(t, "<redacted>", redact(cfg.Api.Auth.JWT.Secret))
	assert.NotEmpty(t, cfg.Api.Auth.StaticUsers.UsersFile)
}

// T027: .env loading tests (US3)

func TestLoad_EnvFileLoadsWithoutShellVars(t *testing.T) {
	restore := clearEnv(t)
	defer restore()

	dir := t.TempDir()
	envContent := `GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE=users-from-env.yaml
GITSTORE_API__AUTH__JWT__SECRET=supersecretkey-minimum-32-chars!!
GITSTORE_GRPC_AUTH__HMAC_SECRET=ci-test-grpc-hmac-secret
GITSTORE_LOG__LEVEL=warn
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "users-from-env.yaml", cfg.Api.Auth.StaticUsers.UsersFile)
	assert.Equal(t, "warn", cfg.Log.Level)
}

func TestLoad_ShellVarOverridesEnvFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()

	dir := t.TempDir()
	envContent := "GITSTORE_LOG__LEVEL=warn\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	// Shell var takes priority over .env
	setRequiredAuth(t)
	os.Setenv("GITSTORE_LOG__LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg.Log.Level)
}

func TestLoad_AbsentEnvFileIsNoOp(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)
	// No .env file — Load must still succeed with defaults

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, 4000, cfg.Api.Port)
}

// T019: validation tests (US2)

func TestLoad_MissingRequiredKeyReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	// Do NOT set required auth fields

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GrpcAuth.HmacSecret")
}

func TestLoad_EmptyStringForRequiredKeyIsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	os.Setenv("GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE", "")
	os.Setenv("GITSTORE_API__AUTH__JWT__SECRET", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_InvalidPortReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__PORT", "99999")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Port")
}

func TestLoad_InvalidLogFormatReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_LOG__FORMAT", "xml")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid log format")
	assert.Contains(t, err.Error(), "json, text")
}

func TestLoad_MultipleValidationErrorsReportedTogether(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	t.Setenv("GITSTORE_API__AUTH__AUTHN__CHAIN", "anonymous")
	// No auth set at all — JWT.Secret is provider-conditional and is not
	// required when the static-users provider is not selected explicitly,
	// but the struct-level required grpc_auth.hmac_secret fails first.

	_, err := Load()
	require.Error(t, err)
	// The always-required fields should appear in the single error string.
	assert.Contains(t, err.Error(), "GrpcAuth.HmacSecret")
	assert.NotContains(t, err.Error(), "JWT.Secret")
}

func TestValidateAuthChainConfig_StaticUsersRequiresJWTSecret(t *testing.T) {
	err := validateAuthChainConfig(&Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"static-users"}}}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.jwt.secret")
}

func TestValidateAuthChainConfig_AnonymousDoesNotRequireJWTSecret(t *testing.T) {
	require.NoError(t, validateAuthChainConfig(&Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"anonymous"}}}}}))
}

// T028: missing HMAC secret causes startup failure
func TestLoad_MissingGrpcHmacSecretReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	os.Setenv("GITSTORE_API__AUTH__STATIC_USERS__USERS_FILE", "config/users.yaml")
	os.Setenv("GITSTORE_API__AUTH__JWT__SECRET", "supersecretkey-minimum-32-chars!!")
	// GITSTORE_GRPC_AUTH__HMAC_SECRET intentionally absent

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GrpcAuth")
}

// T021: unknown keys in config file produce a log warning and do not abort startup

func TestLoad_UnknownKeyInConfigFileDoesNotAbortStartup(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := "unknown_key = \"oops\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestLoad_ScyllaDockerAddressOptionsAreKnown(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	path := filepath.Join(t.TempDir(), "config.toml")
	content := "[api.datastore.scylla]\ndisable_shard_aware_port = true\nignore_peer_addr = true\n"
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.True(t, cfg.Api.Datastore.Scylla.DisableShardAwarePort)
	assert.True(t, cfg.Api.Datastore.Scylla.IgnorePeerAddr)
}

// T009: datastore backend config validation tests

func TestLoad_DatastoreBackendDefaultsToMemdb(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "memdb", cfg.Api.Datastore.Backend)
}

func TestLoad_RejectsLegacyNamespaceRepositoryFenceEnvVar(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE", "enabled")

	_, err := Load()
	require.ErrorContains(t, err, "features.namespace_repository_fence")
	require.ErrorContains(t, err, "always enabled")
}

func TestLoad_RejectsLegacyNamespaceRepositoryFenceConfigFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := "[features]\nnamespace_repository_fence = \"auto\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	_, err := Load()
	require.ErrorContains(t, err, "features.namespace_repository_fence")
	require.ErrorContains(t, err, "always enabled")
}

func TestLoad_DatastoreBackendMemdbIsValid(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__DATASTORE__BACKEND", "memdb")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "memdb", cfg.Api.Datastore.Backend)
}

func TestLoad_DatastoreBackendScyllaIsValid(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__DATASTORE__BACKEND", "scylla")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "scylla", cfg.Api.Datastore.Backend)
}

func TestLoad_DatastoreBackendUnknownValueReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__DATASTORE__BACKEND", "badvalue")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "badvalue")
	assert.Contains(t, err.Error(), "memdb")
	assert.Contains(t, err.Error(), "scylla")
}

func TestLoad_DatastoreScyllaPasswordLoadedAndRedactable(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__DATASTORE__SCYLLA__PASSWORD", "s3cr3t")

	cfg, err := Load()
	require.NoError(t, err)
	// The raw value must be populated from env
	assert.Equal(t, "s3cr3t", cfg.Api.Datastore.Scylla.Password)
	// And redact() must mask it in logs
	assert.Equal(t, "<redacted>", redact(cfg.Api.Datastore.Scylla.Password))
}

// T007: ServiceAccountConfig defaults and validation

func TestLoad_ServiceAccountDefaultsApplied(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "gitstore", cfg.Api.Auth.ServiceAccount.Issuer)
	assert.Equal(t, "gitstore-api", cfg.Api.Auth.ServiceAccount.Audience)
	assert.Equal(t, "gitstore-api/serviceaccount-token", cfg.Api.Auth.ServiceAccount.AssertionAudience)
	assert.Equal(t, "", cfg.Api.Auth.ServiceAccount.SigningKey)
	assert.Equal(t, 10*time.Minute, cfg.Api.Auth.ServiceAccount.DefaultTTL)
	assert.Equal(t, time.Hour, cfg.Api.Auth.ServiceAccount.MaxTTL)
	assert.Equal(t, 2*time.Minute, cfg.Api.Auth.ServiceAccount.ClockSkew)
}

func TestLoad_ServiceAccountSigningKeyNotRequiredWhenProviderNotChained(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	// Default chain is ["static-users", "anonymous"] — no service-account provider.

	_, err := Load()
	require.NoError(t, err)
}

func TestLoad_ServiceAccountSigningKeyRequiredWhenJWTProviderChained(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := filepath.Join(t.TempDir(), "gitstore.toml")
	content := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-jwt", "anonymous"]
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	_, err := LoadFrom(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.serviceaccount.signing_key is required")
	assert.Contains(t, err.Error(), "serviceaccount-jwt")
}

func TestLoad_ServiceAccountSigningKeyRequiredWhenAssertionProviderChained(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := filepath.Join(t.TempDir(), "gitstore.toml")
	content := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-assertion", "anonymous"]
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	_, err := LoadFrom(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.serviceaccount.signing_key is required")
}

func TestLoad_ServiceAccountSigningKeySatisfiesRequirementWhenChained(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := filepath.Join(t.TempDir(), "gitstore.toml")
	content := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-jwt", "anonymous"]
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	os.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----")

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.Api.Auth.ServiceAccount.SigningKey)
}

func TestLoad_ServiceAccountSigningKeyRedactedInStartupLog(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "<redacted>", redact(cfg.Api.Auth.ServiceAccount.SigningKey))
}

// T007a: FR-015c — signing key must not be sourced from a shared config file.

// withTempSharedServiceConfigMountPath overrides sharedServiceConfigMountPath
// to a path under t.TempDir() for the duration of the test, so these tests
// never touch the real /config directory on the host.
func withTempSharedServiceConfigMountPath(t *testing.T) string {
	t.Helper()
	original := sharedServiceConfigMountPath
	path := filepath.Join(t.TempDir(), "gitstore.toml")
	sharedServiceConfigMountPath = path
	t.Cleanup(func() { sharedServiceConfigMountPath = original })
	return path
}

func TestLoadFrom_RefusesServiceAccountSigningKeyFromSharedMountPath(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := withTempSharedServiceConfigMountPath(t)

	content := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-jwt", "anonymous"]
[api.auth.serviceaccount]
signing_key = "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----"
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	_, err := LoadFrom(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be set in")
	assert.Contains(t, err.Error(), path)
	assert.Contains(t, err.Error(), "per-service")
}

func TestLoadFrom_AllowsServiceAccountSigningKeyFromEnvVarEvenAtSharedMountPath(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	path := withTempSharedServiceConfigMountPath(t)

	content := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-jwt", "anonymous"]
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))

	os.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----")

	cfg, err := LoadFrom(path)
	require.NoError(t, err)
	assert.NotEmpty(t, cfg.Api.Auth.ServiceAccount.SigningKey)
}

func TestLoadFromFiles_RefusesServiceAccountSigningKeyFromSharedMountPathEvenWhenOverlayClearsIt(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	sharedPath := withTempSharedServiceConfigMountPath(t)

	sharedContent := `[api.auth.static_users]
users_file = "users.yaml"
[api.auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[grpc_auth]
hmac_secret = "explicit-hmac"
[api.auth.authn]
chain = ["static-users", "serviceaccount-jwt", "anonymous"]
[api.auth.serviceaccount]
signing_key = "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----"
`
	require.NoError(t, os.WriteFile(sharedPath, []byte(sharedContent), 0600))

	// An overlay that merges on top and explicitly blanks the key — e.g. an
	// operator trying to signal "the real key comes from the env" — must not
	// be able to hide the fact that the shared file on disk still carries
	// key material: git-service and controller-manager mount and read that
	// same file directly, independent of this process's merge order.
	overlayPath := filepath.Join(t.TempDir(), "overlay.toml")
	require.NoError(t, os.WriteFile(overlayPath, []byte("[api.auth.serviceaccount]\nsigning_key = \"\"\n"), 0600))

	os.Setenv("GITSTORE_API__AUTH__SERVICEACCOUNT__SIGNING_KEY", "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----")

	_, err := LoadFromFiles([]string{sharedPath, overlayPath})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not be set in")
	assert.Contains(t, err.Error(), sharedPath)
}

func TestValidateServiceAccountSigningKeySource_IgnoresNonSharedPath(t *testing.T) {
	cfg := &Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"serviceaccount-jwt"}}}}}
	err := validateServiceAccountSigningKeySource(cfg, []string{"/some/other/path.toml"}, "signing-key-material")
	assert.NoError(t, err)
}

func TestValidateServiceAccountSigningKeySource_IgnoresWhenProviderNotChained(t *testing.T) {
	cfg := &Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"static-users", "anonymous"}}}}}
	err := validateServiceAccountSigningKeySource(cfg, []string{sharedServiceConfigMountPath}, "signing-key-material")
	assert.NoError(t, err)
}

func TestValidateServiceAccountSigningKeySource_RejectsSharedPathWithKeyMaterial(t *testing.T) {
	cfg := &Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"serviceaccount-jwt"}}}}}
	err := validateServiceAccountSigningKeySource(cfg, []string{sharedServiceConfigMountPath}, "signing-key-material")
	require.Error(t, err)
	assert.Contains(t, err.Error(), sharedServiceConfigMountPath)
}

func TestValidateServiceAccountSigningKeySource_RejectsSharedPathAmongOverlays(t *testing.T) {
	cfg := &Config{Api: ApiConfig{Auth: AuthConfig{AuthN: AuthNConfig{Chain: []string{"serviceaccount-jwt"}}}}}
	err := validateServiceAccountSigningKeySource(cfg, []string{"/some/other/path.toml", sharedServiceConfigMountPath}, "signing-key-material")
	require.Error(t, err)
	assert.Contains(t, err.Error(), sharedServiceConfigMountPath)
}

func TestValidateOIDCAuthChainConfig_NotChained(t *testing.T) {
	cfg := &AuthConfig{AuthN: AuthNConfig{Chain: []string{"static-users", "anonymous"}}}
	assert.NoError(t, validateOIDCAuthChainConfig(cfg))
}

func TestValidateOIDCAuthChainConfig_RequiresIssuerURI(t *testing.T) {
	cfg := &AuthConfig{
		AuthN: AuthNConfig{Chain: []string{"oidc-jwt"}},
		OIDC:  OIDCConfig{Audience: "gitstore"},
	}
	err := validateOIDCAuthChainConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.oidc_jwt.issuer_uri")
}

func TestValidateOIDCAuthChainConfig_RequiresAudienceOrClientID(t *testing.T) {
	cfg := &AuthConfig{
		AuthN: AuthNConfig{Chain: []string{"oidc-jwt", "anonymous"}},
		OIDC:  OIDCConfig{IssuerURI: "http://localhost:4444/"},
	}
	err := validateOIDCAuthChainConfig(cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.oidc_jwt.audience")
	assert.Contains(t, err.Error(), "api.auth.oidc_jwt.client_id")
}

func TestValidateOIDCAuthChainConfig_AudienceOnlySuffices(t *testing.T) {
	// Pure resource-server mode: no client_id configured at all.
	cfg := &AuthConfig{
		AuthN: AuthNConfig{Chain: []string{"oidc-jwt"}},
		OIDC:  OIDCConfig{IssuerURI: "http://localhost:4444/", Audience: "gitstore-api"},
	}
	assert.NoError(t, validateOIDCAuthChainConfig(cfg))
}

func TestValidateOIDCAuthChainConfig_ClientIDOnlySuffices(t *testing.T) {
	cfg := &AuthConfig{
		AuthN: AuthNConfig{Chain: []string{"oidc-jwt"}},
		OIDC:  OIDCConfig{IssuerURI: "http://localhost:4444/", ClientID: "gitstore"},
	}
	assert.NoError(t, validateOIDCAuthChainConfig(cfg))
}

func TestValidateOIDCAuthChainConfig_Satisfied(t *testing.T) {
	cfg := &AuthConfig{
		AuthN: AuthNConfig{Chain: []string{"oidc-jwt"}},
		OIDC:  OIDCConfig{IssuerURI: "http://localhost:4444/", ClientID: "gitstore", ClockSkew: 2 * time.Minute},
	}
	assert.NoError(t, validateOIDCAuthChainConfig(cfg))
}

// Strict duration grammar (ms|s|m|h single magnitude only).

func TestLoad_StrictDurationGrammarAcceptsSingleMagnitude(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__AUTH__JWT__REFRESH_GRACE", "500ms")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, 500*time.Millisecond, cfg.Api.Auth.JWT.RefreshGrace)
}

func TestLoad_StrictDurationGrammarRejectsCompoundDuration(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__AUTH__JWT__REFRESH_GRACE", "1h30m")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid duration")
}

func TestLoad_StrictDurationGrammarRejectsDayUnit(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__AUTH__JWT__REFRESH_GRACE", "1d")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid duration")
}

func TestLoad_StrictDurationGrammarRejectsSubMillisecondUnit(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_API__AUTH__JWT__REFRESH_GRACE", "500us")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid duration")
}

// push_limits: shared static push-size ceiling (IEC size strings only).

func TestParseIECBytes(t *testing.T) {
	cases := []struct {
		raw     string
		want    int64
		wantErr bool
	}{
		{raw: "512KiB", want: 512 * 1024},
		{raw: "50MiB", want: 50 * 1024 * 1024},
		{raw: "1GiB", want: 1024 * 1024 * 1024},
		{raw: "10B", want: 10},
		{raw: "50MB", wantErr: true},
		{raw: "1000", wantErr: true},
		{raw: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := ParseIECBytes(tc.raw)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLoad_PushLimitsDefaultsResolveBytes(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "512MiB", cfg.PushLimits.MaxPackSize)
	assert.Equal(t, int64(512*1024*1024), cfg.PushLimits.MaxPackSizeBytes)
	assert.Equal(t, "100MiB", cfg.PushLimits.MaxFileSize)
	assert.Equal(t, int64(100*1024*1024), cfg.PushLimits.MaxFileSizeBytes)
}

func TestLoad_PushLimitsCustomSizeResolvesBytes(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE", "1GiB")
	t.Setenv("GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE", "512KiB")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, int64(1024*1024*1024), cfg.PushLimits.MaxPackSizeBytes)
	assert.Equal(t, int64(512*1024), cfg.PushLimits.MaxFileSizeBytes)
}

func TestLoad_PushLimitsRejectsDecimalUnits(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_PUSH_LIMITS__MAX_PACK_SIZE", "50MB")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid size")
}

func TestLoad_PushLimitsRejectsBareNumber(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_PUSH_LIMITS__MAX_FILE_SIZE", "1000")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a valid size")
}

// grpc_auth: shared inter-service HMAC secret.

func TestLoad_GrpcAuthHmacSecretLoadedAndRedactable(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_GRPC_AUTH__HMAC_SECRET", "my-hmac-secret")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "my-hmac-secret", cfg.GrpcAuth.HmacSecret)
	assert.Equal(t, "<redacted>", redact(cfg.GrpcAuth.HmacSecret))
}

// Legacy-key rejection: a representative sample of renamed/removed paths,
// each confirmed to fail Load() naming its replacement. The full mapping
// lives in config.go's legacyKeys table; this is not exhaustive.

func TestLoad_RejectsLegacyConfigKeysViaEnv(t *testing.T) {
	cases := []struct {
		name            string
		envVar          string
		envValue        string
		wantReplacement string
	}{
		{"rate_limit_per_second_renamed", "GITSTORE_API__RATE_LIMIT_PER_SECOND", "10", "api.rate_limit.per_second"},
		{"git_grpc_uri_renamed", "GITSTORE_GIT__GRPC__URI", "dns:///x:1", "api.git_service.uri"},
		{"auth_jwt_duration_renamed", "GITSTORE_AUTH__JWT__DURATION", "1h", "api.auth.jwt.ttl"},
		{"datastore_backend_renamed", "GITSTORE_DATASTORE__BACKEND", "scylla", "api.datastore.backend"},
		{"auth_grpc_hmac_secret_renamed", "GITSTORE_AUTH__GRPC__HMAC_SECRET", "x", "grpc_auth.hmac_secret"},
		{"watch_namespace_bucket_size_removed", "GITSTORE_WATCH__NAMESPACE__BUCKET_SIZE", "4096", "fixed at 4096"},
		{"watch_namespace_readers_enabled_removed", "GITSTORE_WATCH__NAMESPACE__READERS_ENABLED", "false", "always on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := clearEnv(t)
			defer restore()
			setRequiredAuth(t)
			t.Setenv(tc.envVar, tc.envValue)

			_, err := Load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantReplacement)
		})
	}
}

func TestLoad_RejectsLegacyGitGrpcUriConfigFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := "[git.grpc]\nuri = \"dns:///localhost:1\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.git_service.uri")
}

func TestLoad_RejectsLegacyAuthStaticusersConfigFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := "[auth.staticusers]\nusers_file = \"legacy-users.yaml\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.toml"), []byte(content), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "api.auth.static_users")
}
