// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package product owns finalizer completion for terminating Products.
package product

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/cache"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/categorytaxonomy"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const foregroundDeletionFinalizer = "gitstore.dev/foreground-deletion"

type CompletionClient interface {
	CompleteDeletion(context.Context, string, string, string) error
}

type Reconciler struct {
	cache      cache.CacheAccessor[categorytaxonomy.Product]
	completion CompletionClient
}

func NewReconciler(c cache.CacheAccessor[categorytaxonomy.Product], completion CompletionClient) *Reconciler {
	return &Reconciler{cache: c, completion: completion}
}

func (r *Reconciler) Reconcile(ctx context.Context, key types.WorkItemKey) types.ReconcileResult {
	p, ok := r.cache.Get(key)
	if !ok {
		return types.ResultOK()
	}
	if p.DeletionTimestamp == nil || !hasFinalizer(p.Finalizers) {
		return types.ResultOK()
	}
	if err := r.completion.CompleteDeletion(ctx, p.Namespace, p.Name, p.ResourceVersion); err != nil {
		if errors.Is(err, types.ErrConflict) {
			return types.ResultAfter(100 * time.Millisecond)
		}
		return types.ResultTransient(fmt.Errorf("product: complete deletion: %w", err))
	}
	return types.ResultOK()
}

func hasFinalizer(values []string) bool {
	for _, value := range values {
		if value == foregroundDeletionFinalizer {
			return true
		}
	}
	return false
}
