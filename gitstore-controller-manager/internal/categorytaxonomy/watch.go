// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import (
	"encoding/json"
	"fmt"
	"math/big"
	"slices"
)

// ProductRelationVersion changes only when category resolution can change for
// referencing Products, not when unrelated counts or conditions are refreshed.
func ProductRelationVersion(category CategoryTaxonomy) (string, error) {
	var resolved ResolvedCategoryTaxonomy
	if len(category.Status.Resolved) != 0 {
		if err := json.Unmarshal(category.Status.Resolved, &resolved); err != nil {
			return "", fmt.Errorf("decode category path for Product fan-out: %w", err)
		}
	}
	version, err := json.Marshal(struct {
		UID         string
		Namespace   string
		Name        string
		Terminating bool
		Path        []string `json:",omitempty"`
	}{
		UID: category.UID, Namespace: category.Namespace, Name: category.Name,
		Terminating: category.DeletionTimestamp != nil ||
			slices.Contains(category.Finalizers, datastoreForegroundDeletionFinalizer),
		Path: resolved.Path,
	})
	return string(version), err
}

// AcceptWatchUpdate rejects stale CategoryTaxonomy watch payloads. Generation
// orders spec changes; resourceVersion orders status updates within one
// generation when both values use the API's decimal concurrency version.
func AcceptWatchUpdate(oldObj, newObj CategoryTaxonomy) bool {
	if newObj.Generation != oldObj.Generation {
		return newObj.Generation > oldObj.Generation
	}

	oldVersion, oldOK := new(big.Int).SetString(oldObj.ResourceVersion, 10)
	newVersion, newOK := new(big.Int).SetString(newObj.ResourceVersion, 10)
	if !oldOK || !newOK {
		return true
	}
	return newVersion.Cmp(oldVersion) >= 0
}

// ShouldEnqueueWatchUpdate suppresses controller-authored status-only events.
// Product changes enqueue affected categories through the Product watcher, so
// only a newer spec generation needs direct CategoryTaxonomy reconciliation.
func ShouldEnqueueWatchUpdate(oldObj, newObj CategoryTaxonomy) bool {
	if newObj.Generation > oldObj.Generation {
		return true
	}
	// Foreground deletion changes system-owned lifecycle metadata without
	// advancing generation. It must nevertheless wake the reconciler so it can
	// remove the finalizer after dependents have been handled.
	return oldObj.DeletionTimestamp == nil && newObj.DeletionTimestamp != nil ||
		!slices.Contains(oldObj.Finalizers, datastoreForegroundDeletionFinalizer) &&
			slices.Contains(newObj.Finalizers, datastoreForegroundDeletionFinalizer)
}
