// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func credentialDocument(ref string) string {
	return fmt.Sprintf(`---
apiVersion: storage.gitstore.dev/v1beta1
kind: File
metadata:
  name: hero
  namespace: shop
spec:
  contentType: image/jpeg
  source:
    type: s3
    uri: s3://bucket/hero
    credentialsRef: %s
---
hero`, ref)
}

func TestFilePreservesTypedCredentials(t *testing.T) {
	for _, typ := range []string{"aws-access-key/v1", "future/v3"} {
		ref := fmt.Sprintf(`{"kind":"CredentialsRef","type":%q,"secretRef":{"kind":"SecretRef","name":"cloud","namespace":"shop"}}`, typ)
		parsed, _, err := NewParser().ParseResource(strings.NewReader(credentialDocument(ref)))
		require.NoError(t, err)
		raw, err := json.Marshal(parsed.File.Spec.Source.CredentialsRef)
		require.NoError(t, err)
		var got map[string]any
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Equal(t, typ, got["type"])
		require.Equal(t, "cloud", got["secretRef"].(map[string]any)["name"])
		require.NotContains(t, got, "name", "typed credentials must not fabricate a legacy name")
	}
}

func TestFileRejectsInvalidTypedInput(t *testing.T) {
	for _, ref := range []string{
		`null`,
		`{"kind":"CredentialsRef","secretRef":{"kind":"SecretRef","name":"cloud"}}`,
		`{"kind":"CredentialsRef","type":"bad","secretRef":{"kind":"SecretRef","name":"cloud"}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":null}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","key":null}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","key":""}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","namespace":null}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","namespace":""}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","namespace":"other"}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"../cloud"}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","value":"MUST-NOT-LEAK"}}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud"},"value":"MUST-NOT-LEAK"}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud"},"name":"legacy"}`,
		`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud"},"Type":"other/v1"}`,
	} {
		_, _, err := NewParser().ParseResource(strings.NewReader(credentialDocument(ref)))
		require.Error(t, err, "invalid reference accepted")
		require.NotContains(t, err.Error(), "MUST-NOT-LEAK")
	}
}

func TestFileRejectsBareCredentials(t *testing.T) {
	for _, ref := range []string{
		`{"kind":"SecretRef","name":"cloud"}`,
		`{"kind":"Secret","name":"cloud","namespace":"shop"}`,
		`{"kind":"SecretRef","name":"cloud","key":null}`,
	} {
		_, _, err := NewParser().ParseResource(strings.NewReader(credentialDocument(ref)))
		require.Error(t, err, "bare credential references must not be accepted")
		require.Contains(t, err.Error(), "credentialsRef")
	}
}

func TestFileCredentialBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wrapper bool
		field   string
		value   string
		valid   bool
	}{
		{"wrapper-kind", true, "kind", "SecretRef", false},
		{"empty-wrapper-kind", true, "kind", "", false},
		{"secret-kind", false, "kind", "Secret", false},
		{"empty-secret-kind", false, "kind", "", false},
		{"short-name", false, "name", "a", true},
		{"max-name", false, "name", strings.Repeat("a", 63), true},
		{"long-name", false, "name", strings.Repeat("a", 64), false},
		{"empty-name", false, "name", "", false},
		{"uppercase-name", false, "name", "Cloud", false},
		{"short-key", false, "key", "A", true},
		{"max-key", false, "key", strings.Repeat("A", 253), true},
		{"long-key", false, "key", strings.Repeat("A", 254), false},
		{"dot-key", false, "key", ".", false},
		{"parent-key", false, "key", "..", false},
		{"path-key", false, "key", "cloud/key", false},
		{"space-key", false, "key", "cloud key", false},
		{"max-type", true, "type", strings.Repeat("a", 125) + "/v1", true},
		{"long-type", true, "type", strings.Repeat("a", 126) + "/v1", false},
		{"zero-version", true, "type", "cloud/v0", false},
		{"padded-version", true, "type", "cloud/v01", false},
		{"uppercase-type", true, "type", "Cloud/v1", false},
		{"foreign-namespace", false, "namespace", "other", false},
		{"long-namespace", false, "namespace", strings.Repeat("a", 64), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			secret := map[string]any{"kind": "SecretRef", "name": "cloud"}
			wrapper := map[string]any{"kind": "CredentialsRef", "type": "aws-access-key/v1", "secretRef": secret}
			if tc.wrapper {
				wrapper[tc.field] = tc.value
			} else {
				secret[tc.field] = tc.value
			}
			raw, err := json.Marshal(wrapper)
			require.NoError(t, err)
			_, _, err = NewParser().ParseResource(strings.NewReader(credentialDocument(string(raw))))
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestFileCredentialsRemainOptional(t *testing.T) {
	doc := strings.Replace(credentialDocument("null"), "    credentialsRef: null\n", "", 1)
	parsed, _, err := NewParser().ParseResource(strings.NewReader(doc))
	require.NoError(t, err)
	require.Nil(t, parsed.File.Spec.Source.CredentialsRef)
}
