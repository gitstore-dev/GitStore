// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"encoding/json"
	"testing"
)

func FuzzCredentialReference(f *testing.F) {
	f.Add([]byte(`{"kind":"CredentialsRef","type":"aws-access-key/v1","secretRef":{"kind":"SecretRef","name":"catalog"}}`))
	f.Add([]byte(`{"kind":"CredentialsRef","secretRef":null}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxEncodedBytes {
			t.Skip()
		}
		var ref CredentialsRef
		if json.Unmarshal(data, &ref) == nil {
			namespace := ""
			if ref.SecretRef.Namespace != nil {
				namespace = *ref.SecretRef.Namespace
			}
			if err := ValidateCredentialsRef(ref, namespace); err != nil {
				t.Fatal("decoded invalid credential reference")
			}
		}
	})
}
