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
		"GITSTORE_API_PORT",
		"GITSTORE_GIT_GRPC",
		"GITSTORE_GIT_WS",
		"GITSTORE_GIT_HTTP_URL",
		"GITSTORE_CACHE_TTL",
		"GITSTORE_LOG_LEVEL",
		"GITSTORE_AUTH_ADMIN_USERNAME",
		"GITSTORE_AUTH_ADMIN_PASSWORD_HASH",
		"GITSTORE_AUTH_JWT_SECRET",
		"GITSTORE_AUTH_JWT_DURATION",
		"GITSTORE_AUTH_JWT_ISSUER",
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
	os.Setenv("GITSTORE_AUTH_ADMIN_USERNAME", "admin")
	os.Setenv("GITSTORE_AUTH_ADMIN_PASSWORD_HASH", "$2a$12$hash")
	os.Setenv("GITSTORE_AUTH_JWT_SECRET", "supersecretkey-minimum-32-chars!!")
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
	assert.Equal(t, "localhost:50051", cfg.Git.GRPC)
	assert.Equal(t, "ws://localhost:8080", cfg.Git.WS)
	assert.Equal(t, "http://localhost:9418", cfg.Git.HttpURL)
	assert.Equal(t, 300, cfg.Cache.TTL)
	assert.Equal(t, "info", cfg.LogLevel)
	assert.Equal(t, "24h", cfg.Auth.JWTDuration)
	assert.Equal(t, "gitstore", cfg.Auth.JWTIssuer)
}

func TestLoad_EnvVarOverridesDefault(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API_PORT", "8888")
	os.Setenv("GITSTORE_LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, 8888, cfg.Api.Port)
	assert.Equal(t, "debug", cfg.LogLevel)
}

func TestLoad_ConfigFileValueAppliedWhenNoEnvVar(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)

	dir := t.TempDir()
	content := `log_level = "warn"

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
	assert.Equal(t, "warn", cfg.LogLevel)
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

	os.Setenv("GITSTORE_API_PORT", "9999")

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
	// cfg.Auth.AdminPasswordHash and cfg.Auth.JWTSecret must be redacted.
	assert.Equal(t, "<redacted>", redact(cfg.Auth.AdminPasswordHash))
	assert.Equal(t, "<redacted>", redact(cfg.Auth.JWTSecret))

	// Non-sensitive field must pass through.
	assert.Equal(t, "admin", cfg.Auth.AdminUsername)
}

// T027: .env loading tests (US3)

func TestLoad_EnvFileLoadsWithoutShellVars(t *testing.T) {
	restore := clearEnv(t)
	defer restore()

	dir := t.TempDir()
	envContent := `GITSTORE_AUTH_ADMIN_USERNAME=envfileuser
GITSTORE_AUTH_ADMIN_PASSWORD_HASH=$2a$12$hash
GITSTORE_AUTH_JWT_SECRET=supersecretkey-minimum-32-chars!!
GITSTORE_LOG_LEVEL=warn
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	cfg, err := Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, "envfileuser", cfg.Auth.AdminUsername)
	assert.Equal(t, "warn", cfg.LogLevel)
}

func TestLoad_ShellVarOverridesEnvFile(t *testing.T) {
	restore := clearEnv(t)
	defer restore()

	dir := t.TempDir()
	envContent := "GITSTORE_LOG_LEVEL=warn\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0600))

	orig, _ := os.Getwd()
	require.NoError(t, os.Chdir(dir))
	defer os.Chdir(orig)

	// Shell var takes priority over .env
	setRequiredAuth(t)
	os.Setenv("GITSTORE_LOG_LEVEL", "debug")

	cfg, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "debug", cfg.LogLevel)
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
	assert.Contains(t, err.Error(), "AdminUsername")
}

func TestLoad_EmptyStringForRequiredKeyIsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	os.Setenv("GITSTORE_AUTH_ADMIN_USERNAME", "")
	os.Setenv("GITSTORE_AUTH_ADMIN_PASSWORD_HASH", "")
	os.Setenv("GITSTORE_AUTH_JWT_SECRET", "")

	_, err := Load()
	require.Error(t, err)
}

func TestLoad_InvalidPortReturnsError(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	setRequiredAuth(t)
	os.Setenv("GITSTORE_API_PORT", "99999")

	_, err := Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Port")
}

func TestLoad_MultipleValidationErrorsReportedTogether(t *testing.T) {
	restore := clearEnv(t)
	defer restore()
	// No auth set at all — should report all three required fields

	_, err := Load()
	require.Error(t, err)
	// All three required fields should appear in the single error string
	assert.Contains(t, err.Error(), "AdminUsername")
	assert.Contains(t, err.Error(), "AdminPasswordHash")
	assert.Contains(t, err.Error(), "JWTSecret")
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
