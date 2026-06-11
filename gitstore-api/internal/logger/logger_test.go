// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package logger

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitLoggerSupportsJSONFormat(t *testing.T) {
	log, err := InitLogger("info", "json")
	require.NoError(t, err)
	require.NotNil(t, log)
	Sync()
}

func TestInitLoggerSupportsTextFormat(t *testing.T) {
	log, err := InitLogger("debug", "text")
	require.NoError(t, err)
	require.NotNil(t, log)
	Sync()
}

func TestInitLoggerRejectsInvalidFormat(t *testing.T) {
	_, err := InitLogger("info", "xml")
	require.ErrorContains(t, err, "invalid log format")
}
