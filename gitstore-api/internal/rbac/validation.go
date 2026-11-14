// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbac

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

type ValidationCapture struct {
	Reader   SnapshotReader
	Registry *auth.ProviderRegistry
	Scratch  *MemoryBudget
	Timeout  time.Duration
}

var validationScratch = NewMemoryBudget(rebuildWorkers * RebuildScratchLimit)

type Validation struct {
	mu         sync.RWMutex
	ctx        context.Context
	cancel     context.CancelFunc
	evaluation auth.PinnedAuthorization
	principal  auth.Principal
	snapshots  map[string]Snapshot
	scratch    *MemoryReservation
}

func (c ValidationCapture) Begin(ctx context.Context, principal *auth.Principal, namespaceUID string) (*Validation, error) {
	if c.Registry == nil || !auth.ValidTrustedPrincipal(principal) {
		return nil, ErrUnavailable
	}
	selected := c.Registry.AuthZ()
	if _, ok := selected.(auth.AuthorizationPinProvider); !ok {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, auth.ErrNotSupported)
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	if timeout < 0 || timeout > 30*time.Second {
		return nil, ErrUnavailable
	}
	deadline := time.Now().Add(timeout)
	if !principal.ExpiresAt.IsZero() && principal.ExpiresAt.Before(deadline) {
		deadline = principal.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	result := &Validation{ctx: ctx, cancel: cancel, principal: *principal, snapshots: map[string]Snapshot{}}
	result.principal.Groups = slices.Clone(principal.Groups)
	result.principal.Scopes = slices.Clone(principal.Scopes)
	result.principal.Claims = nil
	fail := func(err error) (*Validation, error) {
		result.Close()
		return nil, snapshotUnavailable(err)
	}
	budget := c.Scratch
	if budget == nil {
		budget = validationScratch
	}
	size := c.Reader.MaxBytes
	if size == 0 {
		size = RebuildScratchLimit
	}
	if size < 1 || size > RebuildScratchLimit {
		return fail(ErrUnavailable)
	}
	if namespaceUID != "" {
		size *= 2
	}
	var err error
	result.scratch, err = budget.Reserve(size)
	if err != nil {
		auth.ObserveRBACUnavailable(ctx, "budget", namespaceUID == "")
		return fail(err)
	}
	cluster, err := c.Reader.Read(ctx, ClusterTierID)
	if err != nil {
		return fail(err)
	}
	result.snapshots[ClusterTierID] = cluster
	tier := func(source Snapshot) auth.AuthorizationTier {
		namespace := source.Origin.Namespace
		if source.State.TierID == ClusterTierID {
			namespace = ""
		}
		return auth.AuthorizationTier{
			Namespace: namespace, NamespaceVersion: source.Origin.NamespaceVersion, Active: source.Origin.Active,
			State: source.State, CatalogDigest: source.CatalogDigest, BuiltinDigest: source.BuiltinDigest,
			ContentDigest: source.ContentDigest, Resources: source.Resources,
		}
	}
	model := auth.AuthorizationModel{ModelVersion: ModelVersion, Cluster: tier(cluster)}
	if namespaceUID != "" {
		if namespaceUID == ClusterTierID {
			return fail(ErrInvalid)
		}
		namespace, err := c.Reader.Read(ctx, namespaceUID)
		if err != nil {
			return fail(err)
		}
		result.snapshots[namespaceUID] = namespace
		value := tier(namespace)
		model.Namespace = &value
	}
	result.evaluation, err = auth.PinAuthorization(ctx, selected, model)
	if err != nil {
		return fail(err)
	}
	if err := result.check(ctx); err != nil {
		return fail(err)
	}
	context.AfterFunc(ctx, result.Close)
	return result, nil
}

func (v *Validation) check(ctx context.Context) error {
	if err := v.ctx.Err(); err != nil {
		return snapshotUnavailable(err)
	}
	if err := ctx.Err(); err != nil {
		return snapshotUnavailable(err)
	}
	if v.evaluation == nil {
		return ErrUnavailable
	}
	return nil
}

func (v *Validation) Check(ctx context.Context) error {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.check(ctx)
}

func (v *Validation) queryContext(ctx context.Context) (context.Context, func()) {
	deadline, _ := v.ctx.Deadline()
	query, cancel := context.WithDeadline(ctx, deadline)
	stop := context.AfterFunc(v.ctx, cancel)
	return query, func() { stop(); cancel() }
}

func (v *Validation) Authorize(ctx context.Context, action string, resource auth.ResourceContext) (auth.Decision, error) {
	return v.authorizePrincipal(ctx, nil, action, resource)
}

// Review evaluates a target without changing the actor captured for admission.
// Callers must authorize the reviewer independently before resolving the target.
func (v *Validation) Review(ctx context.Context, target *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	if !auth.ValidTrustedPrincipal(target) {
		return auth.Decision{}, ErrInvalid
	}
	return v.authorizePrincipal(ctx, target, action, resource)
}

func (v *Validation) authorizePrincipal(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.check(ctx); err != nil {
		return auth.Deny("opa", "validation unavailable"), err
	}
	query, cancel := v.queryContext(ctx)
	defer cancel()
	if principal == nil {
		principal = &v.principal
	}
	decision, err := v.evaluation.Authorize(query, principal, action, resource)
	if checkErr := v.check(ctx); checkErr != nil {
		return auth.Deny("opa", "validation unavailable"), checkErr
	}
	return decision, err
}

func (v *Validation) Covers(ctx context.Context, request auth.AuthorizationCoverageRequest) (auth.AuthorizationCoverage, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.check(ctx); err != nil {
		return auth.AuthorizationCoverage{}, err
	}
	query, cancel := v.queryContext(ctx)
	defer cancel()
	result, err := auth.CheckAuthorizationCoverage(query, v.evaluation, &v.principal, request)
	if checkErr := v.check(ctx); checkErr != nil {
		return auth.AuthorizationCoverage{}, checkErr
	}
	return result, err
}

// Snapshot returns capture metadata, not mutable model rows.
func (v *Validation) Snapshot(tier string) (Snapshot, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.check(v.ctx); err != nil {
		return Snapshot{}, err
	}
	snapshot, ok := v.snapshots[tier]
	if !ok {
		return Snapshot{}, ErrUnavailable
	}
	snapshot.Resources = nil
	return snapshot, nil
}

func (v *Validation) Lookup(tier string, kind Kind, name string) (*Resource, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.check(v.ctx); err != nil {
		return nil, err
	}
	snapshot, ok := v.snapshots[tier]
	if !ok {
		return nil, ErrUnavailable
	}
	key := string(kind) + "/" + name
	index, found := slices.BinarySearchFunc(snapshot.Resources, key, func(resource *Resource, key string) int {
		return strings.Compare(string(resource.Kind)+"/"+resource.Metadata.Name, key)
	})
	if !found {
		return nil, ErrNotFound
	}
	return CloneResource(snapshot.Resources[index])
}

func (v *Validation) ProposedTreeDigest(tier string, changes []Change) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if err := v.check(v.ctx); err != nil {
		return "", err
	}
	snapshot, ok := v.snapshots[tier]
	if !ok {
		return "", ErrUnavailable
	}
	resources := make(map[string]*Resource, len(snapshot.Resources)+len(changes))
	for _, resource := range snapshot.Resources {
		resources[string(resource.Kind)+"/"+resource.Metadata.Name] = resource
	}
	for _, change := range changes {
		if change.Resource == nil {
			return "", ErrInvalid
		}
		key := string(change.Resource.Kind) + "/" + change.Resource.Metadata.Name
		if change.Remove || change.Terminate {
			delete(resources, key)
		} else {
			resources[key] = change.Resource
		}
	}
	values := make([]*Resource, 0, len(resources))
	for _, resource := range resources {
		values = append(values, resource)
	}
	return ManifestTreeDigest(values)
}

func (v *Validation) Close() {
	v.cancel()
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.evaluation != nil {
		v.evaluation.Close()
		v.evaluation = nil
	}
	v.snapshots = nil
	if v.scratch != nil {
		v.scratch.Close()
		v.scratch = nil
	}
	v.snapshots = nil
}
