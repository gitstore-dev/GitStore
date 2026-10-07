// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/require"
)

func TestFileCredentialProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec string
		want map[string]any
	}{
		{
			name: "legacy persisted Go-field spelling",
			spec: `{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero","CredentialsRef":{"Kind":"SecretRef","Name":"cloud","Key":"key","Namespace":"shop"}}}`,
			want: nil,
		},
		{
			name: "explicit wrapper",
			spec: `{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero","CredentialsRef":{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"cloud","namespace":"shop"}}}}`,
			want: map[string]any{"kind": "CredentialsRef", "type": "aws-access-key/v1", "secretRef": map[string]any{"kind": "SecretRef", "name": "cloud", "key": nil, "namespace": "shop"}, "name": nil, "key": nil, "namespace": nil},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := DatastoreFileToGraphQL(&datastore.File{
				UID: "00000000-0000-0000-0000-000000000201", Name: "hero", Namespace: "shop",
				Spec: json.RawMessage(tc.spec), OwnerReferences: json.RawMessage("[]"),
			})
			if tc.want == nil {
				require.Nil(t, file, "obsolete credential shapes must not be represented as typed credentials")
				return
			}
			require.NotNil(t, file)
			data, err := json.Marshal(file.Spec.Source.CredentialsRef)
			require.NoError(t, err)
			var got map[string]any
			require.NoError(t, json.Unmarshal(data, &got))
			for key, expected := range tc.want {
				if nested, ok := expected.(map[string]any); ok {
					actual, ok := got[key].(map[string]any)
					require.True(t, ok, "nested metadata missing")
					for field, value := range nested {
						require.Equal(t, value, actual[field], field)
					}
				} else {
					require.Equal(t, expected, got[key], key)
				}
			}
		})
	}
}
