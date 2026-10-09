// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

const marker = "synthetic-material-MUST-NOT-APPEAR"

func ptr(s string) *string { return &s }

func reference() SecretRef { return SecretRef{Kind: "SecretRef", Name: "catalog"} }

func record(t *testing.T, values map[string]string) []byte {
	t.Helper()
	encoded := make(map[string]string, len(values))
	for k, v := range values {
		encoded[k] = base64.StdEncoding.EncodeToString([]byte(v))
	}
	data, err := json.Marshal(map[string]any{"format": "secret-record/v1", "values": encoded})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func atomicRecord(t *testing.T, root, name string, data []byte) {
	t.Helper()
	f, err := os.CreateTemp(root, ".record-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.Name(), filepath.Join(root, name)); err != nil {
		t.Fatal(err)
	}
}

type fakeProvider struct {
	calls atomic.Int32
	read  func(context.Context, SecretRef, Scope) ([]byte, error)
}

func (p *fakeProvider) Category() ProviderCategory { return ProviderFile }
func (p *fakeProvider) Format() Format             { return FormatJSONRecord }
func (p *fakeProvider) Read(ctx context.Context, ref SecretRef, scope Scope) ([]byte, error) {
	p.calls.Add(1)
	return p.read(ctx, ref, scope)
}

func runtimeResolver(t *testing.T, p Provider) *Resolver {
	t.Helper()
	r, err := NewRuntimeResolver(p, RuntimeBinding{
		Environment: "dev", Namespace: "shop",
		Authorize: func(context.Context, ResolutionRequest, SecretRef) error { return nil },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func request() ResolutionRequest {
	return ResolutionRequest{Principal: "operator", Resource: ResourceIdentity{
		Kind: "File", Namespace: "shop", Repository: "catalog", Name: "hero",
	}}
}
