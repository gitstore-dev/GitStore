// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog_test

import (
	"testing"

	"github.com/google/cel-go/cel"
	"github.com/stretchr/testify/require"
)

func newCELEnv(t *testing.T) *cel.Env {
	t.Helper()
	env, err := cel.NewEnv()
	require.NoError(t, err)
	return env
}
