// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
)

func newOwnerReferenceBackfillStore(t *testing.T) datastore.OwnerReferenceBackfillStore {
	t.Helper()
	store, err := memdb.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close memdb: %v", err)
		}
	})
	return store.(datastore.OwnerReferenceBackfillStore)
}

func TestOwnerReferenceBackfillProgressResumesWithFencedLease(t *testing.T) {
	ctx := context.Background()
	store := newOwnerReferenceBackfillStore(t)
	start := time.Now().UTC()

	first, acquired, err := store.AcquireOwnerReferenceBackfillLease(ctx, "operator-a", start, time.Hour)
	if err != nil || !acquired {
		t.Fatalf("acquire first lease: acquired=%t err=%v", acquired, err)
	}
	if _, acquired, err := store.AcquireOwnerReferenceBackfillLease(ctx, "operator-b", start, time.Hour); err != nil || acquired {
		t.Fatalf("concurrent lease acquisition: acquired=%t err=%v, want unavailable", acquired, err)
	}

	progress := datastore.OwnerReferenceBackfillProgress{
		Kinds: map[string]datastore.OwnerReferenceBackfillKindProgress{
			"Product": {Cursor: "stable-page-1", Scanned: 10, Repaired: 4},
		},
		UpdatedAt: start,
	}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, first, progress); err != nil {
		t.Fatalf("save first checkpoint: %v", err)
	}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, first, progress); err != nil {
		t.Fatalf("retry checkpoint: %v", err)
	}

	second, acquired, err := store.AcquireOwnerReferenceBackfillLease(ctx, "operator-b", start.Add(2*time.Hour), time.Hour)
	if err != nil || !acquired {
		t.Fatalf("acquire replacement lease: acquired=%t err=%v", acquired, err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("replacement fencing token %d did not advance beyond %d", second.FencingToken, first.FencingToken)
	}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, first, progress); !errors.Is(err, datastore.ErrStaleBackfillLease) {
		t.Fatalf("stale operator checkpoint error = %v, want ErrStaleBackfillLease", err)
	}

	resumed, err := store.LoadOwnerReferenceBackfillProgress(ctx)
	if err != nil {
		t.Fatalf("load resumed checkpoint: %v", err)
	}
	if got := resumed.Kinds["Product"].Cursor; got != "stable-page-1" {
		t.Fatalf("resumed cursor = %q, want stable-page-1", got)
	}
	resumed.Kinds["Product"] = datastore.OwnerReferenceBackfillKindProgress{
		Cursor: "stable-page-2", Scanned: 20, Repaired: 8,
	}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, second, resumed); err != nil {
		t.Fatalf("save resumed checkpoint: %v", err)
	}
}

func TestOwnerReferenceBackfillCompletionRequiresVerifiedKinds(t *testing.T) {
	ctx := context.Background()
	store := newOwnerReferenceBackfillStore(t)
	lease, acquired, err := store.AcquireOwnerReferenceBackfillLease(ctx, "operator", time.Now().UTC(), time.Hour)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: acquired=%t err=%v", acquired, err)
	}

	incomplete := datastore.OwnerReferenceBackfillProgress{
		Kinds:     map[string]datastore.OwnerReferenceBackfillKindProgress{"Product": {Complete: true}},
		Completed: true,
		UpdatedAt: time.Now().UTC(),
	}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, incomplete); !errors.Is(err, datastore.ErrInvalidArgument) {
		t.Fatalf("incomplete completion error = %v, want ErrInvalidArgument", err)
	}

	complete := datastore.OwnerReferenceBackfillProgress{
		Kinds:     make(map[string]datastore.OwnerReferenceBackfillKindProgress),
		Completed: true,
		UpdatedAt: time.Now().UTC(),
	}
	for _, kind := range datastore.OwnerReferenceBackfillKinds() {
		complete.Kinds[kind] = datastore.OwnerReferenceBackfillKindProgress{Complete: true}
	}
	productState := complete.Kinds["Product"]
	productState.Unresolved = 1
	complete.Kinds["Product"] = productState
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, complete); !errors.Is(err, datastore.ErrInvalidArgument) {
		t.Fatalf("unresolved completion error = %v, want ErrInvalidArgument", err)
	}

	complete.Kinds["Product"] = datastore.OwnerReferenceBackfillKindProgress{Complete: true}
	if err := store.SaveOwnerReferenceBackfillProgress(ctx, lease, complete); err != nil {
		t.Fatalf("save verified completion: %v", err)
	}
	persisted, err := store.LoadOwnerReferenceBackfillProgress(ctx)
	if err != nil {
		t.Fatalf("load verified completion: %v", err)
	}
	if !persisted.Completed {
		t.Fatal("verified completion marker was not persisted")
	}
}
