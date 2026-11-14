// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
)

const NamespaceCleanupWorkKind = "RBACNamespaceCleanup"

const namespaceCleanupMutation = `mutation($input: ReconcileRBACNamespaceCleanupsInput!) {
  result: reconcileRBACNamespaceCleanups(input: $input) { nextCursor pending }
}`

func (c *GraphQLClient) ReconcileNamespaceCleanup(ctx context.Context, after string) (string, bool, error) {
	var response struct {
		Result *struct {
			NextCursor string
			Pending    bool
		}
	}
	input := map[string]any{}
	if after != "" {
		input["after"] = after
	}
	if err := c.client.Query(ctx, namespaceCleanupMutation, map[string]any{"input": input}, &response); err != nil {
		return "", false, clientError(err)
	}
	if response.Result == nil {
		return "", false, errors.New("rbac: missing namespace cleanup result")
	}
	return response.Result.NextCursor, response.Result.Pending, nil
}

type namespaceCleanupClient interface {
	ReconcileNamespaceCleanup(context.Context, string) (string, bool, error)
}

// NamespaceCleanupPoller belongs to the primary RBAC group. Only the poll
// schedule is local; authoritative work remains in the API's durable outbox.
type NamespaceCleanupPoller struct {
	mu      sync.Mutex
	client  namespaceCleanupClient
	cursor  string
	backoff *backoff.ExponentialBackOff
}

func NewNamespaceCleanupPoller(client namespaceCleanupClient) *NamespaceCleanupPoller {
	delay := backoff.NewExponentialBackOff()
	delay.InitialInterval = time.Second
	delay.MaxInterval = 10 * time.Second
	delay.Reset()
	return &NamespaceCleanupPoller{client: client, backoff: delay}
}

func (p *NamespaceCleanupPoller) Reconcile(ctx context.Context, _ types.WorkItemKey) types.ReconcileResult {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	next, pending, err := p.client.ReconcileNamespaceCleanup(ctx, p.cursor)
	if err != nil {
		// A sweep is never quarantined: discovery must recover even when no
		// new Namespace event arrives after an extended API outage.
		return types.ResultAfter(p.backoff.NextBackOff())
	}
	p.backoff.Reset()
	p.cursor = next
	if pending || next != "" {
		return types.ResultAfter(time.Second)
	}
	return types.ResultAfter(5 * time.Second)
}
