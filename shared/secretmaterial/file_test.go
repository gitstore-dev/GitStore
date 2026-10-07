// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
)

func TestFileProviderContainmentAndBounds(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	p, err := NewFileProvider(root, FormatJSONRecord)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	scope := Scope{Tier: TierBootstrap}
	atomicRecord(t, root, "catalog.json", record(t, map[string]string{"key": marker}))
	if _, err := p.Read(context.Background(), reference(), scope); err != nil {
		t.Fatal(err)
	}
	atomicRecord(t, root, "catalog.json", []byte(strings.Repeat("x", MaxEncodedBytes+1)))
	if _, err := p.Read(context.Background(), reference(), scope); !errors.Is(err, ErrValueTooLarge) {
		t.Fatal("unbounded provider read")
	}
	atomicRecord(t, outside, "outside.json", []byte(marker))
	if err := os.Remove(filepath.Join(root, "catalog.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.json"), filepath.Join(root, "catalog.json")); err != nil {
		t.Fatal(err)
	}
	if data, err := p.Read(context.Background(), reference(), scope); err == nil || len(data) != 0 {
		t.Fatal("symlink escaped root")
	}
	if err := os.Remove(filepath.Join(root, "catalog.json")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "catalog.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(context.Background(), reference(), scope); !errors.Is(err, ErrForbidden) {
		t.Fatal("nonregular file not rejected")
	}
	ref := reference()
	ref.Name = "../outside"
	if _, err := p.Read(context.Background(), ref, scope); !errors.Is(err, ErrInvalidRef) {
		t.Fatal("provider accepted invalid reference")
	}
}

func TestFileProviderScopeAndRawCompatibility(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dev", "shop")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	atomicRecord(t, dir, "catalog.json", record(t, map[string]string{"key": marker}))
	p, err := NewFileProvider(root, FormatJSONRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	scope := Scope{Tier: TierRuntime, Environment: "dev", Namespace: "shop"}
	if _, err := p.Read(context.Background(), reference(), scope); err != nil {
		t.Fatal(err)
	}
	scope.Environment = "../dev"
	if _, err := p.Read(context.Background(), reference(), scope); !errors.Is(err, ErrInvalidRef) {
		t.Fatal("unsafe deployment path accepted")
	}
	raw, err := NewFileProvider(root, FormatRaw)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if err := os.Mkdir(filepath.Join(root, "catalog"), 0o700); err != nil {
		t.Fatal(err)
	}
	atomicRecord(t, filepath.Join(root, "catalog"), "private_key.pem", []byte(marker))
	ref := reference()
	ref.Key = ptr("private_key.pem")
	data, err := raw.Read(context.Background(), ref, Scope{Tier: TierBootstrap})
	if err != nil || string(data) != marker {
		t.Fatal("legacy raw mapping changed")
	}
	ref.Key = ptr("missing")
	if _, err := raw.Read(context.Background(), ref, Scope{Tier: TierBootstrap}); !errors.Is(err, ErrMissingKey) {
		t.Fatal("missing item misclassified")
	}
}

func TestFilePathSwapCannotEscape(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	atomicRecord(t, outside, "secret.json", []byte(marker))
	p, err := NewFileProvider(root, FormatJSONRecord)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := 0; i < 200; i++ {
			atomicRecord(t, root, "catalog.json", []byte("safe"))
			link := filepath.Join(root, "pending-link")
			if err := os.Symlink(filepath.Join(outside, "secret.json"), link); err != nil {
				t.Error(err)
				return
			}
			if err := os.Rename(link, filepath.Join(root, "catalog.json")); err != nil {
				t.Error(err)
				return
			}
		}
	})
	for i := 0; i < 400; i++ {
		data, err := p.Read(context.Background(), reference(), Scope{Tier: TierBootstrap})
		if err == nil && string(data) != "safe" {
			t.Error("path-swap read escaped configured root")
		}
	}
	wg.Wait()
}
