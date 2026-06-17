// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

// PanicError wraps a reconciler panic for structured logging and metric emission.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("reconciler panic: %v", e.Value)
}

// safeReconcile wraps r.Reconcile in a deferred recover. Any panic is captured
// as a PanicError and returned as ResultTransient so the retry engine handles it.
func safeReconcile(r Reconciler, key WorkItemKey) func(context.Context) types.ReconcileResult {
	return func(ctx context.Context) types.ReconcileResult {
		return func() (result types.ReconcileResult) {
			defer func() {
				if v := recover(); v != nil {
					result = types.ResultTransient(&PanicError{
						Value: v,
						Stack: debug.Stack(),
					})
				}
			}()
			return r.Reconcile(ctx, key)
		}()
	}
}
