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
		"GITSTORE_DATASTORE__BACKEND",
		"GITSTORE_DATASTORE__SCYLLA__HOSTS",
		"GITSTORE_DATASTORE__SCYLLA__KEYSPACE",
		"GITSTORE_DATASTORE__SCYLLA__USERNAME",
		"GITSTORE_DATASTORE__SCYLLA__PASSWORD",
		"GITSTORE_DATASTORE__SCYLLA__TLS",
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

// setRequiredAuth sets the three required auth env vars.
func setRequiredAuth(t *testing.T) {
	t.Helper()
	os.Setenv("GITSTORE_AUTH__ADMIN__USERNAME", "admin")
	os.Setenv("GITSTORE_AUTH__ADMIN__PASSWORD_HASH", "$2a$12$hash")
	os.Setenv("GITSTORE_AUTH__JWT__SECRET", "supersecretkey-minimum-32-chars!!")
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
	assert.Equal(t, "dns:///localhost:50051", cfg.Git.Grpc.Uri)
	assert.Equal(t, "ws://localhost:8080", cfg.Git.Ws.Uri)
	assert.Equal(t, "http://localhost:9418", cfg.Git.Http.Uri)
	assert.Equal(t, 300, cfg.Cache.TTL)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.Equal(t, "24h", cfg.Auth.JWT.Duration)
	assert.Equal(t, "gitstore", cfg.Auth.JWT.Issuer)
}

func TestLoad_EnvVarOverridesDefault(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API__PORT", "8888")
	os.Setenv("GITSTORE_LOG__LEVEL", "debug")
	os.Setenv("GITSTORE_LOG__FORMAT", "text")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8888, cfg.Api.Port)
	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "text", cfg.Log.Format)
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
