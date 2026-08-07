// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint"
)

func TestFilesystemStore_SaveThenLoad_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	want := checkpoint.Record{Kind: "Widget", ResourceVersion: "42", WrittenAt: time.Now().Truncate(time.Second)}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Load(context.Background(), "Widget")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Kind != want.Kind || got.ResourceVersion != want.ResourceVersion {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

func TestFilesystemStore_AtomicWrite_NoPartialFileOnCrash(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if err := store.Save(context.Background(), checkpoint.Record{Kind: "Widget", ResourceVersion: "1"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || filepath.Ext(filepath.Ext(e.Name())) == ".tmp" {
			t.Errorf("temp file left behind after Save: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "Widget.checkpoint.json")); err != nil {
		t.Errorf("expected final checkpoint file to exist: %v", err)
	}
}

func TestFilesystemStore_CorruptFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Widget.checkpoint.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "Widget"); err == nil {
		t.Error("expected error loading corrupt checkpoint file, got nil")
	}
}

func TestFilesystemStore_MissingFile_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if _, err := store.Load(context.Background(), "DoesNotExist"); err == nil {
		t.Error("expected error loading missing checkpoint file, got nil")
	}
}

func TestFilesystemStore_OneFilePerKind_Isolated(t *testing.T) {
	dir := t.TempDir()
	store, err := checkpoint.NewFilesystemStore(dir)
	if err != nil {
		t.Fatalf("NewFilesystemStore: %v", err)
	}

	if err := store.Save(context.Background(), checkpoint.Record{Kind: "A", ResourceVersion: "1"}); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := store.Save(context.Background(), checkpoint.Record{Kind: "B", ResourceVersion: "99"}); err != nil {
		t.Fatalf("Save B: %v", err)
	}

	// Corrupt B's file; A must remain readable.
	if err := os.WriteFile(filepath.Join(dir, "B.checkpoint.json"), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	recA, err := store.Load(context.Background(), "A")
	if err != nil {
		t.Fatalf("Load A after B corrupted: %v", err)
	}
	if recA.ResourceVersion != "1" {
		t.Errorf("A.ResourceVersion = %q, want 1", recA.ResourceVersion)
	}

	if _, err := store.Load(context.Background(), "B"); err == nil {
		t.Error("expected B to be unreadable after corruption")
	}
}
