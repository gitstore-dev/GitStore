// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package opa

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

type pinnedEvaluation struct {
	mu                 sync.RWMutex
	engine             *Engine
	cluster, namespace *Translation
	snapshot           auth.AuthorizationSnapshot
}

func (e *Engine) PinAuthorization(ctx context.Context, model auth.AuthorizationModel) (auth.PinnedAuthorization, error) {
	return e.pinAuthorization(ctx, model, nil)
}

func translateAuthorizationTier(source auth.AuthorizationTier, builtins *rbac.BuiltinSet, reserve func(int) bool) (*Translation, error) {
	content, err := hex.DecodeString(source.ContentDigest)
	if err != nil || len(content) != 32 || source.CatalogDigest != auth.AuthorizationCatalogDigest() ||
		source.BuiltinDigest != builtins.Digest() || !source.Active || source.NamespaceVersion == "" ||
		source.State.Revision == 0 || source.State.Projection.GitRevision == "" || source.State.ProjectionPending {
		return nil, ErrUnavailable
	}
	tier := Tier{ID: source.State.TierID, Namespace: source.Namespace, Active: source.Active, Resources: source.Resources}
	if tier.ID == rbac.ClusterTierID {
		tier.Builtins = builtins
	}
	return translate(tier, reserve)
}

func (e *Engine) pinAuthorization(ctx context.Context, model auth.AuthorizationModel, reserve func(int) bool) (*pinnedEvaluation, error) {
	if e == nil || e.digest == "" || model.ModelVersion != rbac.ModelVersion ||
		model.Cluster.State.TierID != rbac.ClusterTierID || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	builtins, err := rbac.ReleaseBuiltins()
	if err != nil {
		return nil, ErrUnavailable
	}
	cluster, err := translateAuthorizationTier(model.Cluster, builtins, reserve)
	if err != nil {
		return nil, err
	}
	result := &pinnedEvaluation{engine: e, cluster: cluster}
	tiers := []auth.AuthorizationTier{model.Cluster}
	if model.Namespace != nil {
		if model.Namespace.State.TierID == rbac.ClusterTierID {
			return nil, ErrUnavailable
		}
		result.namespace, err = translateAuthorizationTier(*model.Namespace, builtins, reserve)
		if err != nil {
			return nil, err
		}
		tiers = append(tiers, *model.Namespace)
	}
	result.snapshot, err = authorizationSnapshot(e.digest, builtins.Digest(), tiers...)
	if err != nil || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func authorizationSnapshot(policy, builtin string, tiers ...auth.AuthorizationTier) (auth.AuthorizationSnapshot, error) {
	snapshot := auth.AuthorizationSnapshot{
		Loaded: true, APIVersion: rbac.APIVersion, ModelVersion: rbac.ModelVersion,
		CatalogDigest: auth.AuthorizationCatalogDigest(), BuiltinDigest: builtin,
	}
	for _, tier := range tiers {
		snapshot.Tiers = append(snapshot.Tiers, auth.AuthorizationTierIdentity{
			TierID: tier.State.TierID, Revision: tier.State.Revision,
			GitRevision: tier.State.Projection.GitRevision, ContentDigest: tier.ContentDigest,
		})
		snapshot.Roles += tier.State.RoleCount
		snapshot.Bindings += tier.State.BindingCount
	}
	header, err := json.Marshal(struct {
		Policy string
		Model  auth.AuthorizationSnapshot
	}{policy, snapshot})
	if err != nil {
		return auth.AuthorizationSnapshot{}, ErrUnavailable
	}
	sum := sha256.Sum256(header)
	snapshot.Digest = hex.EncodeToString(sum[:])
	return snapshot, nil
}

func (*pinnedEvaluation) Name() string { return "opa" }

func (p *pinnedEvaluation) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cluster == nil {
		return auth.Deny("opa", "evaluation closed"), ErrUnavailable
	}
	decision, err := p.engine.Evaluate(ctx, p.cluster, p.namespace, principal, action, resource)
	decision.PolicyDigest = p.snapshot.Digest
	snapshot := p.snapshot
	snapshot.Tiers = append([]auth.AuthorizationTierIdentity(nil), snapshot.Tiers...)
	decision.AuthorizationSnapshot = &snapshot
	return decision, err
}

func (p *pinnedEvaluation) Covers(ctx context.Context, principal *auth.Principal, request auth.AuthorizationCoverageRequest) (auth.AuthorizationCoverage, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cluster == nil {
		return auth.AuthorizationCoverage{}, ErrUnavailable
	}
	result, err := p.engine.Covers(ctx, p.cluster, p.namespace, principal, request)
	if err != nil {
		return auth.AuthorizationCoverage{}, err
	}
	result.PolicyDigest = p.snapshot.Digest
	return result, nil
}

func (p *pinnedEvaluation) AuthorizationSnapshot() (auth.AuthorizationSnapshot, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.cluster == nil {
		return auth.AuthorizationSnapshot{}, false
	}
	result := p.snapshot
	result.Tiers = append([]auth.AuthorizationTierIdentity(nil), result.Tiers...)
	return result, true
}

func (p *pinnedEvaluation) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cluster, p.namespace = nil, nil
}
