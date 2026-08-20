// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"time"
)

type mutationAuditKey struct{}

type MutationAudit struct {
	Actor     string
	Timestamp time.Time
}

func WithMutationAudit(ctx context.Context, actor string, timestamp time.Time) context.Context {
	return context.WithValue(ctx, mutationAuditKey{}, MutationAudit{
		Actor:     actor,
		Timestamp: timestamp,
	})
}

func MutationAuditFromContext(ctx context.Context) (MutationAudit, bool) {
	audit, ok := ctx.Value(mutationAuditKey{}).(MutationAudit)
	return audit, ok
}
