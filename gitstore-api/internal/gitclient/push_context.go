// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
)

type pushContextKeyType struct{}

var pushContextKey = pushContextKeyType{}

// ContextWithPushContext returns a new context carrying the given PushContext.
// PushContextInserter middleware stores it here; ReceivePack reads it.
func ContextWithPushContext(ctx context.Context, pc *gitv1.PushContext) context.Context {
	return context.WithValue(ctx, pushContextKey, pc)
}

// PushContextFromContext retrieves the PushContext stored by ContextWithPushContext.
// Returns nil if no PushContext is present.
func PushContextFromContext(ctx context.Context) *gitv1.PushContext {
	pc, _ := ctx.Value(pushContextKey).(*gitv1.PushContext)
	return pc
}
