// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"testing"

	authpkg "github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/allowall"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestAllowAll_AllowsAnyActionAndPrincipal(t *testing.T) {
	p := allowall.New(zap.NewNop())

	cases := []struct {
		principal *authpkg.Principal
		action    string
	}{
		{adminPrincipal(), "namespace.create.organization"},
		{developerPrincipal(), "namespace.delete.any"},
		{anonPrincipal(), "repo.push"},
		{&authpkg.Principal{Subject: "unknown", AuthMethod: "test"}, "anything.goes"},
	}

	for _, tc := range cases {
		d, err := p.Authorize(context.Background(), tc.principal, tc.action, authpkg.ResourceContext{})
		require.NoError(t, err, "action %q", tc.action)
		assert.Equal(t, authpkg.OutcomeAllow, d.Outcome, "action %q should be allowed", tc.action)
	}
}

func TestAllowAll_StartupWarning_EmitsWarnLog(t *testing.T) {
	core, logs := observer.New(zapcore.WarnLevel)
	logger := zap.New(core)

	_ = allowall.New(logger)

	entries := logs.All()
	require.Len(t, entries, 1, "expected exactly one log entry")
	assert.Equal(t, zapcore.WarnLevel, entries[0].Level)
	assert.Contains(t, entries[0].Message, "SECURITY: authz provider is allow-all")
}
