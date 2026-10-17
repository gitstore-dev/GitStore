// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/status"
)

func TestProductRelationVersionTracksPathsNotUnrelatedStatus(t *testing.T) {
	category := CategoryTaxonomy{UID: "category-id", Namespace: "shop", Name: "leaf",
		Status: status.ResourceStatus{Resolved: json.RawMessage(`{"path":["root","leaf"],"childCount":1,"productCount":3}`)}}
	before, err := ProductRelationVersion(category)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		mutate  func(*CategoryTaxonomy)
		changed bool
	}{
		{"path cascade without generation change", func(c *CategoryTaxonomy) {
			c.Status.Resolved = json.RawMessage(`{"path":["anchor","root","leaf"],"childCount":1,"productCount":3}`)
		}, true},
		{"path not yet resolved", func(c *CategoryTaxonomy) { c.Status.Resolved = nil }, true},
		{"category replaced", func(c *CategoryTaxonomy) { c.UID = "new-category-id" }, true},
		{"category renamed", func(c *CategoryTaxonomy) { c.Name = "new-leaf" }, true},
		{"different namespace", func(c *CategoryTaxonomy) { c.Namespace = "other-shop" }, true},
		{"termination", func(c *CategoryTaxonomy) { now := time.Now(); c.DeletionTimestamp = &now }, true},
		{"foreground deletion", func(c *CategoryTaxonomy) { c.Finalizers = []string{datastoreForegroundDeletionFinalizer} }, true},
		{"count update", func(c *CategoryTaxonomy) {
			c.Status.Resolved = json.RawMessage(`{"path":["root","leaf"],"childCount":2,"productCount":4}`)
		}, false},
		{"JSON formatting", func(c *CategoryTaxonomy) {
			c.Status.Resolved = json.RawMessage(`{ "productCount": 3, "childCount": 1, "path": [ "root", "leaf" ] }`)
		}, false},
		{"generation and resource version", func(c *CategoryTaxonomy) {
			c.Generation++
			c.ResourceVersion = "20"
			c.Status.ObservedGeneration++
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := category
			test.mutate(&current)
			after, err := ProductRelationVersion(current)
			if err != nil {
				t.Fatal(err)
			}
			if (before != after) != test.changed {
				t.Fatalf("relation version changed = %t, want %t", before != after, test.changed)
			}
		})
	}
	category.Status.Resolved = nil
	absent, err := ProductRelationVersion(category)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"path":[]}`, `{"path":null}`} {
		category.Status.Resolved = json.RawMessage(raw)
		version, err := ProductRelationVersion(category)
		if err != nil || version != absent {
			t.Fatalf("empty path changed unresolved version: %q, %v", raw, err)
		}
	}
	for _, raw := range []string{`{"path":[`, `{"path":42}`, `{"path":[true]}`} {
		category.Status.Resolved = json.RawMessage(raw)
		if _, err := ProductRelationVersion(category); err == nil {
			t.Fatalf("invalid path must fail explicitly: %q", raw)
		}
	}
}

func TestAcceptWatchUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		old  CategoryTaxonomy
		new  CategoryTaxonomy
		want bool
	}{
		{
			name: "new spec generation",
			old:  CategoryTaxonomy{Generation: 1, ResourceVersion: "9"},
			new:  CategoryTaxonomy{Generation: 2, ResourceVersion: "1"},
			want: true,
		},
		{
			name: "stale spec generation",
			old:  CategoryTaxonomy{Generation: 2, ResourceVersion: "1"},
			new:  CategoryTaxonomy{Generation: 1, ResourceVersion: "99"},
			want: false,
		},
		{
			name: "newer status version",
			old:  CategoryTaxonomy{Generation: 2, ResourceVersion: "9"},
			new:  CategoryTaxonomy{Generation: 2, ResourceVersion: "10"},
			want: true,
		},
		{
			name: "stale status version",
			old:  CategoryTaxonomy{Generation: 2, ResourceVersion: "10"},
			new:  CategoryTaxonomy{Generation: 2, ResourceVersion: "9"},
			want: false,
		},
		{
			name: "opaque version remains accepted",
			old:  CategoryTaxonomy{Generation: 2, ResourceVersion: "old"},
			new:  CategoryTaxonomy{Generation: 2, ResourceVersion: "new"},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := AcceptWatchUpdate(test.old, test.new); got != test.want {
				t.Fatalf("AcceptWatchUpdate() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestShouldEnqueueWatchUpdate(t *testing.T) {
	t.Parallel()

	oldObj := CategoryTaxonomy{Generation: 2, ResourceVersion: "9"}
	if ShouldEnqueueWatchUpdate(oldObj, CategoryTaxonomy{Generation: 2, ResourceVersion: "10"}) {
		t.Fatal("status-only update should not enqueue reconciliation")
	}
	if !ShouldEnqueueWatchUpdate(oldObj, CategoryTaxonomy{Generation: 3, ResourceVersion: "11"}) {
		t.Fatal("new spec generation should enqueue reconciliation")
	}
	deletedAt := time.Now().UTC()
	if !ShouldEnqueueWatchUpdate(oldObj, CategoryTaxonomy{
		Generation:        2,
		ResourceVersion:   "11",
		DeletionTimestamp: &deletedAt,
		Finalizers:        []string{datastoreForegroundDeletionFinalizer},
	}) {
		t.Fatal("deletion lifecycle update should enqueue reconciliation")
	}
}
