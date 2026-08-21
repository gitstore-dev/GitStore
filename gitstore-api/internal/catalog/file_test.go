package catalog

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileStatusRoundTrip(t *testing.T) {
	status := FileStatus{
		ObservedGeneration:  2,
		LastAppliedRevision: "main@sha1:abc",
		Conditions: []Condition{
			{Type: ConditionAdmissionAccepted, Status: ConditionTrue},
			{Type: ConditionReady, Status: ConditionTrue},
		},
		Resolved: &ResolvedFileDefinition{ResolvedVariants: []ResolvedFileVariant{{Name: "thumbnail-webp"}}},
	}
	raw, err := json.Marshal(status)
	require.NoError(t, err)
	var got FileStatus
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, status, got)
}

func TestFileSpecSourceValidation(t *testing.T) {
	require.NoError(t, (FileSpec{
		ContentType: "image/jpeg",
		Source:      FileSourceDefinition{Type: "s3", URI: "s3://bucket/key"},
	}).Validate())
	require.Error(t, (FileSpec{ContentType: "image/jpeg"}).Validate())
}

func TestFileResourceContractIdentityDefaultsAndSystemStatus(t *testing.T) {
	resource := FileResource{
		APIVersion: FileAPIVersion,
		Kind:       "File",
		Metadata:   ObjectMeta{Name: "hero"},
		Spec: FileSpec{
			ContentType: "image/jpeg",
			Source:      FileSourceDefinition{Type: "git", URI: "media/hero.jpg"},
		},
	}
	require.Equal(t, FileAPIVersion, resource.APIVersion)
	require.Equal(t, "File", resource.Kind)
	require.Equal(t, "hero", resource.Metadata.Name)
	require.NoError(t, resource.Spec.Validate())

	raw, err := json.Marshal(FileStatus{
		ObservedGeneration: 1,
		Conditions: []Condition{
			{Type: ConditionAdmissionAccepted, Status: ConditionTrue},
			{Type: ConditionReady, Status: ConditionTrue},
		},
	})
	require.NoError(t, err)
	require.NotContains(t, string(raw), `"phase"`)
}
