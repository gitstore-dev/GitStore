// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func consumeCredentials(ctx context.Context, resolver *Resolver, ref CredentialsRef, req ResolutionRequest, operation func(SecretMaterial)) error {
	material, err := resolver.ResolveCredentials(ctx, ref, req)
	if err != nil {
		return err
	}
	defer material.Clear()
	operation(material)
	return nil
}

func TestRuntimeOperationResolvesAtomicReplacementAfterFailure(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "shop")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	provider, err := NewFileProvider(root, FormatJSONRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	resolver := runtimeResolver(t, provider)
	ref := CredentialsRef{Kind: "CredentialsRef", Type: "aws-access-key/v1", SecretRef: reference()}
	operations := 0
	for _, step := range []struct {
		revision string
		values   map[string]string
		want     error
	}{
		{"first", map[string]string{"accessKeyId": "first", "secretAccessKey": marker}, nil},
		{"", nil, ErrNotFound},
		{"", map[string]string{"accessKeyId": "partial"}, ErrMissingKey},
		{"second", map[string]string{"accessKeyId": "second", "secretAccessKey": marker}, nil},
	} {
		if step.values == nil {
			if err := os.Remove(filepath.Join(dir, "catalog.json")); err != nil {
				t.Fatal(err)
			}
		} else {
			atomicRecord(t, dir, "catalog.json", record(t, step.values))
		}
		before := operations
		err := consumeCredentials(t.Context(), resolver, ref, request(), func(material SecretMaterial) {
			operations++
			if string(material.Value("accessKeyId")) != step.revision {
				t.Error("operation received stale provider revision")
			}
		})
		if !errors.Is(err, step.want) {
			t.Fatalf("unexpected classified outcome: %v", err)
		}
		if step.want != nil && operations != before {
			t.Fatal("dependent operation ran with missing or partial material")
		}
	}
	if operations != 2 {
		t.Fatalf("successful operations = %d, want 2", operations)
	}
}

func TestRuntimeDependentOperation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		values      map[string]string
		key         *string
		typ         string
		namespace   string
		principal   string
		providerErr error
		want        error
		wantReads   int32
	}{
		{name: "whole-record", wantReads: 1},
		{name: "session-token", values: map[string]string{"accessKeyId": "id", "secretAccessKey": marker, "sessionToken": "session"}, wantReads: 1},
		{name: "missing-access-key", values: map[string]string{"secretAccessKey": marker}, want: ErrMissingKey, wantReads: 1},
		{name: "empty-secret", values: map[string]string{"accessKeyId": "id", "secretAccessKey": ""}, want: ErrMissingKey, wantReads: 1},
		{name: "selected-item-cannot-load-siblings", key: ptr("accessKeyId"), want: ErrMissingKey, wantReads: 1},
		{name: "unsupported-type", typ: "future/v1", want: ErrUnsupportedType},
		{name: "foreign-namespace", namespace: "other", want: ErrForbidden},
		{name: "denied-principal", principal: "intruder", want: ErrForbidden},
		{name: "missing-record", providerErr: ErrNotFound, want: ErrNotFound, wantReads: 1},
		{name: "provider-outage", providerErr: ErrProviderUnavailable, want: ErrProviderUnavailable, wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := tc.values
			if values == nil {
				values = map[string]string{"accessKeyId": "id", "secretAccessKey": marker}
			}
			provider := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
				if tc.providerErr != nil {
					return nil, tc.providerErr
				}
				return record(t, values), nil
			}}
			resolver, err := NewRuntimeResolver(provider, RuntimeBinding{
				Environment: "dev", Namespace: "shop",
				Authorize: func(_ context.Context, req ResolutionRequest, _ SecretRef) error {
					if req.Principal != "operator" {
						return ErrForbidden
					}
					return nil
				},
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ref := CredentialsRef{Kind: "CredentialsRef", Type: "aws-access-key/v1", SecretRef: reference()}
			ref.SecretRef.Key = tc.key
			if tc.typ != "" {
				ref.Type = tc.typ
			}
			req := request()
			if tc.namespace != "" {
				req.Resource.Namespace = tc.namespace
			}
			if tc.principal != "" {
				req.Principal = tc.principal
			}
			operations := 0
			err = consumeCredentials(context.Background(), resolver, ref, req, func(material SecretMaterial) {
				operations++
				for key, value := range values {
					if string(material.Value(key)) != value {
						t.Errorf("unexpected material for required item")
					}
				}
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("unexpected resolution outcome: %v", err)
			}
			wantOperations := 0
			if tc.want == nil {
				wantOperations = 1
			}
			if operations != wantOperations || provider.calls.Load() != tc.wantReads {
				t.Fatalf("operation/provider counts = %d/%d", operations, provider.calls.Load())
			}
		})
	}
}
