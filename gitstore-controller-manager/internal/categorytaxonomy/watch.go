// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package categorytaxonomy

import "math/big"

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
	return newObj.Generation > oldObj.Generation
}
