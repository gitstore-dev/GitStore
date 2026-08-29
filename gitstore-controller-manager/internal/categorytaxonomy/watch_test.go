// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"testing"
	"time"
)

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
