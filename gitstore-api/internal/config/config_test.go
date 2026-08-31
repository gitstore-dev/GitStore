// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearEnv unsets all GITSTORE_ env vars and returns a restore function.
func clearEnv(t *testing.T) func() {
	t.Helper()
	keys := []string{
		"GITSTORE_API__PORT",
		"GITSTORE_API__RATE_LIMIT_PER_SECOND",
		"GITSTORE_API__RATE_LIMIT_BURST",
		"GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE",
		"GITSTORE_WATCH__NAMESPACE__READERS_ENABLED",
		"GITSTORE_WATCH__NAMESPACE__MATERIALIZER_ENABLED",
		"GITSTORE_WATCH__NAMESPACE__JOURNAL_RETENTION_SECONDS",
		"GITSTORE_WATCH__NAMESPACE__CDC_RETENTION_SECONDS",
		"GITSTORE_WATCH__NAMESPACE__CDC_CONFIDENCE_WINDOW_MILLIS",
		"GITSTORE_WATCH__NAMESPACE__BUCKET_SIZE",
		"GITSTORE_WATCH__NAMESPACE__READ_BATCH_SIZE",
		"GITSTORE_WATCH__NAMESPACE__MAX_REPLAY_EVENTS",
		"GITSTORE_WATCH__NAMESPACE__SUBSCRIBER_BUFFER",
		"GITSTORE_WATCH__NAMESPACE__POLL_MIN_MILLIS",
		"GITSTORE_WATCH__NAMESPACE__POLL_MAX_MILLIS",
		"GITSTORE_WATCH__NAMESPACE__BOOKMARK_INTERVAL_SECONDS",
		"GITSTORE_WATCH__NAMESPACE__LEASE_TTL_SECONDS",
		"GITSTORE_WATCH__NAMESPACE__LEASE_RENEW_INTERVAL_SECONDS",
		"GITSTORE_WATCH__NAMESPACE__MAX_MATERIALIZER_LAG_SECONDS",
		"GITSTORE_GIT__GRPC__URI",
		"GITSTORE_GIT__WS__URI",
		"GITSTORE_GIT__HTTP__URI",
		"GITSTORE_CACHE__TTL",
		"GITSTORE_LOG__LEVEL",
		"GITSTORE_LOG__FORMAT",
		"GITSTORE_AUTH__ADMIN__USERNAME",
		"GITSTORE_AUTH__ADMIN__PASSWORD_HASH",
		"GITSTORE_AUTH__JWT__SECRET",
		"GITSTORE_AUTH__JWT__DURATION",
		"GITSTORE_AUTH__JWT__ISSUER",
		"GITSTORE_AUTH__JWT__REFRESH_GRACE",
		"GITSTORE_DATASTORE__BACKEND",
		"GITSTORE_DATASTORE__SCYLLA__HOSTS",
		"GITSTORE_DATASTORE__SCYLLA__KEYSPACE",
		"GITSTORE_DATASTORE__SCYLLA__USERNAME",
		"GITSTORE_DATASTORE__SCYLLA__PASSWORD",
		"GITSTORE_DATASTORE__SCYLLA__TLS",
		"GITSTORE_AUTH__GRPC__HMAC_SECRET",
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
	os.Setenv("GITSTORE_AUTH__ADMIN__USERNAME", "admin")
	os.Setenv("GITSTORE_AUTH__ADMIN__PASSWORD_HASH", "$2a$12$hash")
	os.Setenv("GITSTORE_AUTH__JWT__SECRET", "supersecretkey-minimum-32-chars!!")
	os.Setenv("GITSTORE_AUTH__GRPC__HMAC_SECRET", "ci-test-grpc-hmac-secret")
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
	assert.Equal(t, 5000, cfg.Api.GitPort)
	assert.Equal(t, float64(50), cfg.Api.RateLimitPerSecond)
	assert.Equal(t, 100, cfg.Api.RateLimitBurst)
	assert.Equal(t, "dns:///localhost:50051", cfg.Git.Grpc.Uri)
	assert.Equal(t, 300, cfg.Cache.TTL)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "24h", cfg.Auth.JWT.Duration)
	assert.Equal(t, "gitstore", cfg.Auth.JWT.Issuer)
	assert.Equal(t, "60s", cfg.Auth.JWT.RefreshGrace)
	assert.True(t, cfg.Watch.Namespace.ReadersEnabled)
	assert.True(t, cfg.Watch.Namespace.MaterializerEnabled)
	assert.Equal(t, 7*24*60*60, cfg.Watch.Namespace.JournalRetentionSeconds)
	assert.Equal(t, 14*24*60*60, cfg.Watch.Namespace.CDCRetentionSeconds)
	assert.Equal(t, 500, cfg.Watch.Namespace.CDCConfidenceWindowMillis)
	assert.Equal(t, 4096, cfg.Watch.Namespace.BucketSize)
	assert.Equal(t, 256, cfg.Watch.Namespace.ReadBatchSize)
	assert.Equal(t, 100000, cfg.Watch.Namespace.MaxReplayEvents)
	assert.Equal(t, 64, cfg.Watch.Namespace.SubscriberBuffer)
	assert.Equal(t, 30000, cfg.Watch.Namespace.SubscriberBackpressureMillis)
	assert.Equal(t, 100, cfg.Watch.Namespace.PollMinMillis)
	assert.Equal(t, 2000, cfg.Watch.Namespace.PollMaxMillis)
	assert.Equal(t, 30, cfg.Watch.Namespace.BookmarkIntervalSeconds)
	assert.Equal(t, 30, cfg.Watch.Namespace.LeaseTTLSeconds)
	assert.Equal(t, 10, cfg.Watch.Namespace.LeaseRenewIntervalSeconds)
	assert.Equal(t, 60, cfg.Watch.Namespace.MaxMaterializerLagSeconds)
}

func TestLoad_NamespaceWatchEnvOverrides(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__READERS_ENABLED", "false")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__MATERIALIZER_ENABLED", "false")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__CDC_CONFIDENCE_WINDOW_MILLIS", "750")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__READ_BATCH_SIZE", "128")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__SUBSCRIBER_BUFFER", "32")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__SUBSCRIBER_BACKPRESSURE_MILLIS", "1500")

	cfg, err := Load()
	require.NoError(t, err)
	assert.False(t, cfg.Watch.Namespace.ReadersEnabled)
	assert.False(t, cfg.Watch.Namespace.MaterializerEnabled)
	assert.Equal(t, 750, cfg.Watch.Namespace.CDCConfidenceWindowMillis)
	assert.Equal(t, 128, cfg.Watch.Namespace.ReadBatchSize)
	assert.Equal(t, 32, cfg.Watch.Namespace.SubscriberBuffer)
	assert.Equal(t, 1500, cfg.Watch.Namespace.SubscriberBackpressureMillis)
}

func TestLoad_RejectsUnsafeNamespaceWatchBounds(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__JOURNAL_RETENTION_SECONDS", "120")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__CDC_RETENTION_SECONDS", "60")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CDC retention")
}

func TestLoad_RejectsCDCWindowDifferentFromSchema(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__CDC_RETENTION_SECONDS", "2592000")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration 006 fixes CDC retention")
}

func TestLoad_RejectsJournalRetentionAboveTableTTL(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__JOURNAL_RETENTION_SECONDS", "604801")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration 006 limits journal retention to 604800 seconds")
}

func TestLoad_RejectsOversizedNamespaceSubscriberBuffer(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__SUBSCRIBER_BUFFER", "257")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Watch.Namespace.SubscriberBuffer")
	assert.Contains(t, err.Error(), "constraint \"max\" violated")
}

func TestLoad_RejectsOversizedNamespaceReplayLimit(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__MAX_REPLAY_EVENTS", "100001")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Watch.Namespace.MaxReplayEvents")
	assert.Contains(t, err.Error(), "constraint \"max\" violated")
}

func TestLoad_RejectsOversizedNamespaceWatchBucket(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__BUCKET_SIZE", "4097")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Watch.Namespace.BucketSize")
	assert.Contains(t, err.Error(), "constraint \"max\" violated")
}

func TestLoad_RejectsMaterializerLagAtCDCRetention(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__MAX_MATERIALIZER_LAG_SECONDS", "1209600")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "maximum materializer lag must be less than CDC retention")
}

func TestLoad_RejectsCDCConfidenceWindowAtMaterializerLag(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__CDC_CONFIDENCE_WINDOW_MILLIS", "60000")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__MAX_MATERIALIZER_LAG_SECONDS", "60")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "CDC confidence window must be less than maximum materializer lag")
}

func TestLoad_AcceptsCDCConfidenceWindowBelowMaterializerLag(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	t.Setenv("GITSTORE_WATCH__NAMESPACE__CDC_CONFIDENCE_WINDOW_MILLIS", "59999")
	t.Setenv("GITSTORE_WATCH__NAMESPACE__MAX_MATERIALIZER_LAG_SECONDS", "60")

	_, err := Load()
	require.NoError(t, err)
}

func TestLoad_EnvVarOverridesDefault(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__PORT", "8888")
	os.Setenv("GITSTORE_API__RATE_LIMIT_PER_SECOND", "5")
	os.Setenv("GITSTORE_API__RATE_LIMIT_BURST", "15")
	os.Setenv("GITSTORE_LOG__LEVEL", "debug")
	os.Setenv("GITSTORE_LOG__FORMAT", "text")
	os.Setenv("GITSTORE_AUTH__JWT__REFRESH_GRACE", "30s")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8888, cfg.Api.Port)
	assert.Equal(t, float64(5), cfg.Api.RateLimitPerSecond)
	assert.Equal(t, 15, cfg.Api.RateLimitBurst)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "30s", cfg.Auth.JWT.RefreshGrace)
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

[cache]
ttl = 600

[auth.jwt]
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
	assert.Equal(t, 600, cfg.Cache.TTL)
	assert.Equal(t, "warn", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
	assert.Equal(t, "45s", cfg.Auth.JWT.RefreshGrace)
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
[auth.admin]
username = "admin"
password_hash = "$2a$10$hash"
[auth.jwt]
secret = "explicit-file-secret-at-least-32-characters"
[auth.grpc]
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
	// cfg.Auth.Admin.Password and cfg.Auth.JWT.Secret must be redacted.
	assert.Equal(t, "<redacted>", redact(cfg.Auth.Admin.Password))
	assert.Equal(t, "<redacted>", redact(cfg.Auth.JWT.Secret))

	// Non-sensitive field must pass through.
	assert.Equal(t, "admin", cfg.Auth.Admin.Username)
}

// T027: .env loading tests (US3)

func TestLoad_EnvFileLoadsWithoutShellVars(t *testing.T) {
	restore := clearEnv(t)
	defer restore()

	dir := t.TempDir()
	envContent := `GITSTORE_AUTH__ADMIN__USERNAME=envfileuser
GITSTORE_AUTH__ADMIN__PASSWORD_HASH=$2a$12$hash
GITSTORE_AUTH__JWT__SECRET=supersecretkey-minimum-32-chars!!
GITSTORE_AUTH__GRPC__HMAC_SECRET=ci-test-grpc-hmac-secret
GITSTORE_LOG__LEVEL=warn
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "envfileuser", cfg.Auth.Admin.Username)
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
	assert.Contains(t, err.Error(), "Admin.Username")
}

func TestLoad_EmptyStringForRequiredKeyIsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	os.Setenv("GITSTORE_AUTH__ADMIN__USERNAME", "")
	os.Setenv("GITSTORE_AUTH__ADMIN__PASSWORD_HASH", "")
	os.Setenv("GITSTORE_AUTH__JWT__SECRET", "")

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
	// No auth set at all — should report all three required fields

	_, err := Load()
	require.Error(t, err)
	// All three required fields should appear in the single error string
	assert.Contains(t, err.Error(), "Admin.Username")
	assert.Contains(t, err.Error(), "Admin.Password")
	assert.Contains(t, err.Error(), "JWT.Secret")
}

// T028: missing HMAC secret causes startup failure
func TestLoad_MissingGrpcHmacSecretReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	os.Setenv("GITSTORE_AUTH__ADMIN__USERNAME", "admin")
	os.Setenv("GITSTORE_AUTH__ADMIN__PASSWORD_HASH", "$2a$12$hash")
	os.Setenv("GITSTORE_AUTH__JWT__SECRET", "supersecretkey-minimum-32-chars!!")
	// GITSTORE_AUTH__GRPC__HMAC_SECRET intentionally absent

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Grpc")
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

// T009: datastore backend config validation tests

func TestLoad_DatastoreBackendDefaultsToMemdb(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "memdb", cfg.Datastore.Backend)
}

func TestLoad_DatastoreBackendMemdbIsValid(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_DATASTORE__BACKEND", "memdb")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "memdb", cfg.Datastore.Backend)
}

func TestLoad_DatastoreBackendScyllaIsValid(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_DATASTORE__BACKEND", "scylla")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "scylla", cfg.Datastore.Backend)
}

func TestLoad_NamespaceRepositoryFenceAutoPreservesDevAndBlocksScylla(t *testing.T) {
	t.Run("memdb enabled", func(t *testing.T) {
		restore := clearEnv(t)
		defer restore()
		setRequiredAuth(t)

		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "auto", cfg.Features.NamespaceRepositoryFence)
		assert.True(t, cfg.NamespaceRepositoryFenceEnabled())
	})

	t.Run("scylla disabled", func(t *testing.T) {
		restore := clearEnv(t)
		defer restore()
		setRequiredAuth(t)
		t.Setenv("GITSTORE_DATASTORE__BACKEND", "scylla")

		cfg, err := Load()
		require.NoError(t, err)
		assert.Equal(t, "auto", cfg.Features.NamespaceRepositoryFence)
		assert.False(t, cfg.NamespaceRepositoryFenceEnabled())
	})
}

func TestLoad_NamespaceRepositoryFenceExplicitModes(t *testing.T) {
	t.Run("enabled permits Scylla activation", func(t *testing.T) {
		restore := clearEnv(t)
		defer restore()
		setRequiredAuth(t)
		t.Setenv("GITSTORE_DATASTORE__BACKEND", "scylla")
		t.Setenv("GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE", "enabled")

		cfg, err := Load()
		require.NoError(t, err)
		assert.True(t, cfg.NamespaceRepositoryFenceEnabled())
	})

	t.Run("disabled blocks memdb", func(t *testing.T) {
		restore := clearEnv(t)
		defer restore()
		setRequiredAuth(t)
		t.Setenv("GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE", "disabled")

		cfg, err := Load()
		require.NoError(t, err)
		assert.False(t, cfg.NamespaceRepositoryFenceEnabled())
	})

	t.Run("invalid mode rejected", func(t *testing.T) {
		restore := clearEnv(t)
		defer restore()
		setRequiredAuth(t)
		t.Setenv("GITSTORE_FEATURES__NAMESPACE_REPOSITORY_FENCE", "sometimes")

		_, err := Load()
		require.ErrorContains(t, err, "namespace repository fence")
	})
}

func TestLoad_DatastoreBackendUnknownValueReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_DATASTORE__BACKEND", "badvalue")

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
	os.Setenv("GITSTORE_DATASTORE__SCYLLA__PASSWORD", "s3cr3t")

	cfg, err := Load()
	require.NoError(t, err)
	// The raw value must be populated from env
	assert.Equal(t, "s3cr3t", cfg.Datastore.Scylla.Password)
	// And redact() must mask it in logs
	assert.Equal(t, "<redacted>", redact(cfg.Datastore.Scylla.Password))
}
