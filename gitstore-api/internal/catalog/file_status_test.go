// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package catalog

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateFileConditions(t *testing.T) {
	require.NoError(t, ValidateFileConditions([]Condition{{Type: ConditionReady, Status: ConditionTrue}}))
	require.Error(t, ValidateFileConditions([]Condition{{Type: ConditionPublished, Status: ConditionTrue}}))
	require.Error(t, ValidateFileConditions([]Condition{{Type: ConditionReady, Status: "Invalid"}}))
}
