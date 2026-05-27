// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package logger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitLoggerSupportsJSONFormat(t *testing.T) {
	require.NoError(t, InitLogger("info", "json"))
	Sync()
}

func TestInitLoggerSupportsTextFormat(t *testing.T) {
	require.NoError(t, InitLogger("debug", "text"))
	Sync()
}

func TestInitLoggerRejectsInvalidFormat(t *testing.T) {
	require.ErrorContains(t, InitLogger("info", "xml"), "invalid log format")
}
