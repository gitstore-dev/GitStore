// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"time"
)

type Resolver struct {
	provider  Provider
	scope     Scope
	owner     string
	ref       SecretRef
	authorize Authorizer
	observer  Observer
	labels    Observation
	slots     chan struct{}
}

func newResolver(p Provider, scope Scope, observer Observer) (*Resolver, error) {
	if p == nil {
		return nil, failure(ErrInvalidRef, "provider", nil)
	}
	if p.Category() != ProviderFile && p.Category() != ProviderEnv {
		return nil, failure(ErrUnsupportedType, "provider", nil)
	}
	if p.Format() != FormatRaw && p.Format() != FormatJSONRecord {
		return nil, failure(ErrUnsupportedType, "format", nil)
	}
	labels := Observation{Tier: scope.Tier, Provider: p.Category(), Consumer: "runtime-contract", Purpose: "integration"}
	if scope.Tier == TierBootstrap {
		labels.Consumer, labels.Purpose = "controller-manager", "identity"
	}
	return &Resolver{provider: p, scope: scope, observer: observer, labels: labels, slots: make(chan struct{}, MaxInflight)}, nil
}

func NewBootstrapResolver(p Provider, binding BootstrapBinding, observer Observer) (*Resolver, error) {
	if binding.Owner == "" {
		return nil, failure(ErrForbidden, "owner", nil)
	}
	if err := ValidateSecretRef(binding.Ref, ""); err != nil {
		return nil, err
	}
	r, err := newResolver(p, Scope{Tier: TierBootstrap}, observer)
	if err != nil {
		return nil, err
	}
	if p.Format() == FormatRaw && binding.Ref.Key == nil {
		return nil, failure(ErrInvalidRef, "key", nil)
	}
	r.owner, r.ref = binding.Owner, binding.Ref
	if binding.Ref.Key != nil {
		key := *binding.Ref.Key
		r.ref.Key = &key
	}
	return r, nil
}

func NewRuntimeResolver(p Provider, binding RuntimeBinding, observer Observer) (*Resolver, error) {
	if binding.Authorize == nil {
		return nil, failure(ErrForbidden, "authorizer", nil)
	}
	if !validName(binding.Environment) || !validName(binding.Namespace) {
		return nil, failure(ErrInvalidRef, "scope", nil)
	}
	r, err := newResolver(p, Scope{Tier: TierRuntime, Environment: binding.Environment, Namespace: binding.Namespace}, observer)
	if err != nil {
		return nil, err
	}
	r.authorize = binding.Authorize
	return r, nil
}

func (r *Resolver) ResolveSecret(ctx context.Context, ref SecretRef, req ResolutionRequest) (SecretMaterial, error) {
	return r.resolve(ctx, ref, req, "")
}

func (r *Resolver) ResolveCredentials(ctx context.Context, ref CredentialsRef, req ResolutionRequest) (SecretMaterial, error) {
	if err := ValidateCredentialsRef(ref, r.scope.Namespace); err != nil {
		r.observe(err, 0)
		return SecretMaterial{}, err
	}
	return r.resolve(ctx, ref.SecretRef, req, ref.Type)
}

func (r *Resolver) observe(err error, duration time.Duration) {
	if r.observer != nil {
		r.observer.Observe(r.labels, outcome(err), duration)
	}
}

func (r *Resolver) resolve(ctx context.Context, ref SecretRef, req ResolutionRequest, credentialType string) (result SecretMaterial, err error) {
	start := time.Now()
	defer func() { r.observe(err, time.Since(start)) }()
	ctx, cancel := context.WithTimeout(ctx, ResolutionLimit)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return SecretMaterial{}, failure(ErrProviderUnavailable, "context", err)
	}
	if err := ValidateSecretRef(ref, r.scope.Namespace); err != nil {
		return SecretMaterial{}, err
	}
	if r.scope.Tier == TierBootstrap {
		if req.Principal != r.owner || req.Resource != (ResourceIdentity{}) || ref.Name != r.ref.Name ||
			!sameOptional(ref.Key, r.ref.Key) || credentialType != "" {
			return SecretMaterial{}, failure(ErrForbidden, "binding", nil)
		}
	} else if req.Principal == "" || req.Resource.Namespace != r.scope.Namespace || req.Resource.Kind == "" ||
		req.Resource.Repository == "" || (req.Resource.Name == "" && req.Resource.UID == "") {
		return SecretMaterial{}, failure(ErrForbidden, "resource", nil)
	}
	if credentialType != "" && credentialType != "aws-access-key/v1" {
		return SecretMaterial{}, failure(ErrUnsupportedType, "type", nil)
	}
	select {
	case r.slots <- struct{}{}:
	default:
		return SecretMaterial{}, failure(ErrProviderUnavailable, "saturated", nil)
	}
	if r.observer != nil {
		r.observer.Inflight(r.labels, 1)
	}
	defer func() {
		if r.observer != nil {
			r.observer.Inflight(r.labels, -1)
		}
		<-r.slots
	}()
	if r.authorize != nil {
		if err := r.authorize(ctx, req, ref); err != nil {
			return SecretMaterial{}, failure(ErrForbidden, "authorization", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return SecretMaterial{}, failure(ErrProviderUnavailable, "context", err)
	}
	data, err := r.provider.Read(ctx, ref, r.scope)
	defer clear(data)
	if err != nil {
		return SecretMaterial{}, failure(classified(err), "provider", err)
	}
	if err := ctx.Err(); err != nil {
		return SecretMaterial{}, failure(ErrProviderUnavailable, "context", err)
	}
	material, err := parseRecord(data, r.provider.Format(), ref)
	if err != nil {
		return SecretMaterial{}, err
	}
	if r.scope.Tier == TierRuntime && material.RecordFormat() == "serviceaccount-signing-key/v1" {
		material.Clear()
		return SecretMaterial{}, failure(ErrForbidden, "record-tier", nil)
	}
	if credentialType != "" {
		if err := validateCredentialMaterial(material); err != nil {
			material.Clear()
			return SecretMaterial{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		material.Clear()
		return SecretMaterial{}, failure(ErrProviderUnavailable, "context", err)
	}
	return material, nil
}

func sameOptional(a, b *string) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
