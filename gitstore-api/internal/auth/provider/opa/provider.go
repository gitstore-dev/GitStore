// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"maps"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

const (
	translatedAllocationFactor = 8
	evaluationConcurrency      = 256
)

type publishedTier struct {
	data        *Translation
	source      auth.AuthorizationTier
	reservation *rbac.MemoryReservation
	readers     int
	originAt    time.Time
}

type PreparedTier struct {
	mu       sync.Mutex
	owner    *Provider
	tier     *publishedTier
	consumed bool
}

func (t *PreparedTier) Install() error { return t.owner.InstallTier(t) }

func (t *PreparedTier) SetOriginProgress(at time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.consumed || t.tier == nil || at.IsZero() || at.After(time.Now()) {
		return ErrUnavailable
	}
	t.tier.originAt = at
	return nil
}

func (t *PreparedTier) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.consumed {
		t.consumed = true
		if t.tier != nil {
			t.tier.reservation.Close()
		}
		t.tier = nil
	}
}

// Provider owns immutable publications and their pinned-reader reservations.
// Source continuity is established by the manager before InstallTier.
type Provider struct {
	mu              sync.Mutex
	engine          *Engine
	builtins        *rbac.BuiltinSet
	budget          *rbac.MemoryBudget
	tiers           map[string]*publishedTier
	namespaces      map[string]string
	evaluating      chan struct{}
	closed          bool
	namespaceLoader func(string)
	checkProgress   func(time.Time) error
	originMaxAge    time.Duration
}

func NewProvider(ctx context.Context) (*Provider, error) {
	return NewProviderWithMemoryLimit(ctx, rbac.TranslatedMemoryLimit)
}

func NewProviderWithMemoryLimit(ctx context.Context, bytes int64) (*Provider, error) {
	if bytes < 1 || bytes > rbac.TranslatedMemoryLimit {
		return nil, ErrUnavailable
	}
	engine, err := NewEngine(ctx)
	if err != nil {
		return nil, err
	}
	builtins, err := rbac.ReleaseBuiltins()
	if err != nil {
		return nil, err
	}
	return &Provider{engine: engine, builtins: builtins, budget: rbac.NewMemoryBudget(bytes),
		tiers: map[string]*publishedTier{}, namespaces: map[string]string{},
		evaluating: make(chan struct{}, evaluationConcurrency)}, nil
}

func (*Provider) Name() string { return "opa" }

func (p *Provider) SetNamespaceLoader(loader func(string)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.namespaceLoader = loader
}

func (p *Provider) SetProgressCheck(check func(time.Time) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkProgress = check
}

func (p *Provider) RequireOriginProgress(maxAge time.Duration) error {
	if maxAge <= 0 || maxAge > 5*time.Second {
		return ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.originMaxAge = maxAge
	return nil
}

func (p *Provider) RecordOriginProgress(tierID string, identity rbac.ProjectionIdentity, at time.Time) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	tier := p.tiers[tierID]
	if p.closed || tier == nil || tier.source.State.Projection != identity || at.IsZero() || at.After(time.Now()) {
		return ErrUnavailable
	}
	if at.After(tier.originAt) {
		tier.originAt = at
	}
	return nil
}

func (p *Provider) originReady(tier *publishedTier, now time.Time) bool {
	return tier != nil && (p.originMaxAge == 0 || !tier.originAt.IsZero() && !tier.originAt.After(now) && now.Sub(tier.originAt) <= p.originMaxAge)
}

func (p *Provider) reserveAllocation(ctx context.Context, reservation *rbac.MemoryReservation) func(int) bool {
	return func(size int) bool {
		return ctx.Err() == nil && reservation.Grow(int64(size)*translatedAllocationFactor) == nil
	}
}

func (p *Provider) PrepareTier(ctx context.Context, source auth.AuthorizationTier) (*PreparedTier, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	reservation, err := p.budget.Reserve(0)
	if err != nil {
		return nil, err
	}
	data, err := translateAuthorizationTier(source, p.builtins, p.reserveAllocation(ctx, reservation))
	if err != nil || ctx.Err() != nil {
		reservation.Close()
		return nil, ErrUnavailable
	}
	source.Resources = nil
	return &PreparedTier{owner: p, tier: &publishedTier{data: data, source: source, reservation: reservation, readers: 1}}, nil
}

func (p *Provider) InstallTier(prepared *PreparedTier) error {
	if prepared == nil {
		return ErrUnavailable
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || prepared.owner != p || prepared.consumed {
		return ErrUnavailable
	}
	next := prepared.tier
	tierID := next.source.State.TierID
	previous := p.tiers[tierID]
	if previous != nil && previous.source.State.Revision > next.source.State.Revision {
		return ErrUnavailable
	}
	if next.source.Namespace != "" {
		if uid := p.namespaces[next.source.Namespace]; uid != "" && uid != tierID {
			return ErrUnavailable
		}
		p.namespaces[next.source.Namespace] = tierID
	}
	p.tiers[tierID], prepared.consumed, prepared.tier = next, true, nil
	if previous != nil {
		p.releaseLocked(previous)
	}
	return nil
}

func (p *Provider) PublishTier(ctx context.Context, source auth.AuthorizationTier) error {
	prepared, err := p.PrepareTier(ctx, source)
	if err != nil {
		return err
	}
	defer prepared.Close()
	return p.InstallTier(prepared)
}

func (p *Provider) releaseLocked(tier *publishedTier) {
	tier.readers--
	if tier.readers == 0 {
		tier.reservation.Close()
	}
}

func (p *Provider) InvalidateTier(tierID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if tier := p.tiers[tierID]; tier != nil {
		delete(p.tiers, tierID)
		if p.namespaces[tier.source.Namespace] == tierID {
			delete(p.namespaces, tier.source.Namespace)
		}
		p.releaseLocked(tier)
	}
}

type reservedEvaluation struct {
	*pinnedEvaluation
	once    sync.Once
	release func()
}

func (p *reservedEvaluation) Close() {
	p.once.Do(func() {
		p.pinnedEvaluation.Close()
		p.release()
	})
}

func (p *Provider) pinServing(ctx context.Context, resource auth.ResourceContext) (*reservedEvaluation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cluster := p.tiers[rbac.ClusterTierID]
	now := time.Now()
	if p.closed || !p.originReady(cluster, now) || ctx.Err() != nil || p.checkProgress != nil && p.checkProgress(now) != nil {
		return nil, ErrUnavailable
	}
	var namespace *publishedTier
	sources := []auth.AuthorizationTier{cluster.source}
	name, _ := resource.Attrs["namespace"].(string)
	if name != "" && name != "gitstore-system" {
		namespace = p.tiers[p.namespaces[name]]
		if !p.originReady(namespace, now) {
			return nil, ErrUnavailable
		}
		sources = append(sources, namespace.source)
	}
	snapshot, err := authorizationSnapshot(p.engine.digest, p.builtins.Digest(), sources...)
	if err != nil {
		return nil, err
	}
	evaluation := &pinnedEvaluation{engine: p.engine, cluster: cluster.data, snapshot: snapshot}
	cluster.readers++
	if namespace != nil {
		namespace.readers++
		evaluation.namespace = namespace.data
	}
	return &reservedEvaluation{pinnedEvaluation: evaluation, release: func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.releaseLocked(cluster)
		if namespace != nil {
			p.releaseLocked(namespace)
		}
	}}, nil
}

func (p *Provider) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	proofScope := resource
	if clusterScopedNamespaceCleanup(principal, action, resource) {
		proofScope.Attrs = maps.Clone(resource.Attrs)
		delete(proofScope.Attrs, "namespace")
	}
	select {
	case p.evaluating <- struct{}{}:
		defer func() { <-p.evaluating }()
	default:
		auth.ObserveRBACUnavailable(ctx, "evaluation", false)
		return auth.Deny("opa", "evaluation saturated"), ErrUnavailable
	}
	if capture := auth.ServingAuthorizationCaptureFromContext(ctx); capture != nil {
		return capture.Authorize(ctx, p, principal, action, resource, func() (auth.PinnedAuthorization, error) {
			evaluation, err := p.pinServing(ctx, proofScope)
			if err != nil {
				return nil, err
			}
			return evaluation, nil
		})
	}
	pinned, err := p.pinServing(ctx, proofScope)
	if err != nil {
		auth.ObserveRBACUnavailable(ctx, "tier", proofScope.Attrs["namespace"] == nil || proofScope.Attrs["namespace"] == "")
		name, _ := proofScope.Attrs["namespace"].(string)
		p.mu.Lock()
		loader := p.namespaceLoader
		p.mu.Unlock()
		if loader != nil && name != "" {
			loader(name)
		}
		return auth.Deny("opa", "tier unavailable"), err
	}
	defer pinned.Close()
	return pinned.Authorize(ctx, principal, action, resource)
}

// Retired namespace grants stay suppressed while verified storage cleanup uses
// current cluster policy. The Git client derives the marker from lifecycle rows.
func clusterScopedNamespaceCleanup(principal *auth.Principal, action string, resource auth.ResourceContext) bool {
	return principal != nil && principal.Subject == "system:api" && principal.AuthMethod == "internal" &&
		action == auth.ActionRepositoryDelete && resource.Kind == "repository" && resource.Name == "gitstore-system" &&
		resource.Attrs["namespaceCleanup"] == true
}

func (p *Provider) PinAuthorization(ctx context.Context, model auth.AuthorizationModel) (auth.PinnedAuthorization, error) {
	select {
	case p.evaluating <- struct{}{}:
	default:
		auth.ObserveRBACUnavailable(ctx, "evaluation", false)
		return nil, ErrUnavailable
	}
	retained := false
	defer func() {
		if !retained {
			<-p.evaluating
		}
	}()
	p.mu.Lock()
	ready := !p.closed && p.tiers[rbac.ClusterTierID] != nil
	p.mu.Unlock()
	if !ready {
		return nil, ErrUnavailable
	}
	reservation, err := p.budget.Reserve(0)
	if err != nil {
		return nil, err
	}
	pinned, err := p.engine.pinAuthorization(ctx, model, p.reserveAllocation(ctx, reservation))
	if err != nil {
		reservation.Close()
		return nil, err
	}
	retained = true
	return &reservedEvaluation{pinnedEvaluation: pinned, release: func() { reservation.Close(); <-p.evaluating }}, nil
}

func (p *Provider) AuthorizationReadiness(tierID string) (auth.AuthorizationReadiness, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !rbac.ValidTier(tierID) {
		return auth.AuthorizationReadiness{}, false
	}
	now := time.Now()
	if !p.closed && p.originReady(p.tiers[rbac.ClusterTierID], now) && p.originReady(p.tiers[tierID], now) &&
		(p.checkProgress == nil || p.checkProgress(now) == nil) {
		return auth.AuthorizationReadiness{Ready: true, State: "Ready"}, true
	}
	return auth.AuthorizationReadiness{State: "Unavailable", Reason: "tier-unavailable"}, true
}

func (p *Provider) AuthorizationSnapshot() (auth.AuthorizationSnapshot, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cluster := p.tiers[rbac.ClusterTierID]
	if p.closed || cluster == nil {
		return auth.AuthorizationSnapshot{}, false
	}
	snapshot, err := authorizationSnapshot(p.engine.digest, p.builtins.Digest(), cluster.source)
	return snapshot, err == nil
}

func (p *Provider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		for _, tier := range p.tiers {
			p.releaseLocked(tier)
		}
		p.tiers, p.namespaces = nil, nil
	}
}
