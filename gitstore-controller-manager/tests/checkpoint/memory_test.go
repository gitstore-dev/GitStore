// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint_test

import (
	"context"
	"testing"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
)

func TestMemoryStore_SaveThenLoad(t *testing.T) {
	store := checkpoint.NewMemoryStore()

	if _, err := store.Load(context.Background(), "Widget"); err == nil {
		t.Error("expected error loading before any Save, got nil")
	}

	want := checkpoint.Record{Kind: "Widget", ResourceVersion: "7", Snapshot: []byte("[]")}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(context.Background(), "Widget")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ResourceVersion != "7" {
		t.Errorf("ResourceVersion = %q, want 7", got.ResourceVersion)
	}
}
