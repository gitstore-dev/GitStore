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
