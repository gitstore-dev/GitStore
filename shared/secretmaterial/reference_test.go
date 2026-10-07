// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReferenceFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/references.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Valid bool
		Ref   json.RawMessage
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		var ref SecretRef
		err := json.Unmarshal(tc.Ref, &ref)
		if err == nil {
			err = ValidateSecretRef(ref, "shop")
		}
		if (err == nil) != tc.Valid {
			t.Fatalf("valid=%v, error=%v", tc.Valid, err)
		}
	}
}

func TestReferenceBoundaries(t *testing.T) {
	for _, name := range []string{"", "A", "-a", "a-", "a.b", "a_b", "a/b", "a:b", "a b", "a#b", "é", strings.Repeat("a", 64)} {
		ref := reference()
		ref.Name = name
		if !errors.Is(ValidateSecretRef(ref, "shop"), ErrInvalidRef) {
			t.Errorf("accepted invalid name %q", name)
		}
	}
	ref := reference()
	ref.Name = strings.Repeat("a", 63)
	ref.Key = ptr(strings.Repeat("K", 253))
	if err := ValidateSecretRef(ref, "shop"); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", ".", "..", "a/b", "a\\b", "a b", strings.Repeat("a", 254)} {
		ref.Key = ptr(key)
		if !errors.Is(ValidateSecretRef(ref, "shop"), ErrInvalidRef) {
			t.Errorf("accepted invalid key %q", key)
		}
	}
	for _, raw := range []string{
		`null`, `{"kind":"SecretRef","kind":"SecretRef","name":"catalog"}`,
		`{"kind":"SecretRef","name":"catalog","namespace":null}`,
		`{"kind":"SecretRef","name":"catalog","Key":"x"}`,
	} {
		if err := json.Unmarshal([]byte(raw), &ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("accepted noncanonical JSON: %v", err)
		}
	}
}

func TestCredentialsGrammar(t *testing.T) {
	for _, typ := range []string{"aws-access-key/v1", "future/v99", "a/v1"} {
		ref := CredentialsRef{Kind: "CredentialsRef", Type: typ, SecretRef: reference()}
		if err := ValidateCredentialsRef(ref, "shop"); err != nil {
			t.Fatal(err)
		}
	}
	for _, typ := range []string{"aws", "a/v0", "a/v01", "A/v1", "a/v1/x", strings.Repeat("a", 126) + "/v1"} {
		ref := CredentialsRef{Kind: "CredentialsRef", Type: typ, SecretRef: reference()}
		if !errors.Is(ValidateCredentialsRef(ref, "shop"), ErrInvalidRef) {
			t.Errorf("accepted invalid type %q", typ)
		}
	}
	for _, raw := range []string{
		`null`,
		`{"kind":"CredentialsRef","type":"a/v1","secretRef":null}`,
		`{"kind":"CredentialsRef","type":"a/v1","secretRef":{"kind":"SecretRef","name":"catalog"},"value":"synthetic"}`,
	} {
		var ref CredentialsRef
		if err := json.Unmarshal([]byte(raw), &ref); !errors.Is(err, ErrInvalidRef) {
			t.Errorf("accepted malformed wrapper: %v", err)
		}
	}
}
