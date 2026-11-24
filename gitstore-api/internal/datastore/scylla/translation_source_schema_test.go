// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTranslationSourceCoordinationSchema(t *testing.T) {
	const file = "015_translation.cql"
	authority := strings.Join(strings.Fields(createStatement(t, file, "translations_by_key")), " ")
	for _, column := range []string{
		"coordination_version text", "phase text", "binding_snapshot text",
	} {
		require.Contains(t, authority, column)
	}
	require.Contains(t, authority, fullImageCDC)
	identity := strings.Join(strings.Fields(createStatement(t, file, "translations_by_uid")), " ")
	require.Contains(t, identity, "expected_name_token uuid")
	require.NotContains(t, identity, "cdc")
}
