// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ProviderRegistry holds the active provider for each auth plane.
type ProviderRegistry struct {
	mu         sync.RWMutex
	authnChain *ChainedAuthN
	authz      AuthZProvider
	userdir    UserDirProvider
}

func NewProviderRegistry(chain *ChainedAuthN, authz AuthZProvider, userdir UserDirProvider) *ProviderRegistry {
	return &ProviderRegistry{authnChain: chain, authz: authz, userdir: userdir}
}

func (r *ProviderRegistry) AuthN() *ChainedAuthN {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authnChain
}

func (r *ProviderRegistry) AuthZ() AuthZProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.authz
}

func (r *ProviderRegistry) UserDir() UserDirProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.userdir
}

// Swap atomically replaces the active providers (used on SIGHUP reload).
func (r *ProviderRegistry) Swap(chain *ChainedAuthN, authz AuthZProvider, userdir UserDirProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.authnChain = chain
	r.authz = authz
	r.userdir = userdir
}

// ChainedAuthN tries each provider in order; first Allow wins.
// An explicit Deny from any provider short-circuits the chain immediately.
// A nil Principal + OutcomeChallenge means "not my token, continue".
// If all providers return Challenge the chain returns Deny — credentials were
// present but no provider accepted them.
type ChainedAuthN struct {
	providers []AuthNProvider
}

func NewChainedAuthN(providers ...AuthNProvider) *ChainedAuthN {
	return &ChainedAuthN{providers: providers}
}

func (c *ChainedAuthN) Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error) {
	for _, p := range c.providers {
		principal, decision, err := p.Authenticate(ctx, req)
		if err != nil {
			return nil, Deny(p.Name(), fmt.Sprintf("provider error: %v", err)), err
		}
		switch decision.Outcome {
		case OutcomeAllow:
			return principal, decision, nil
		case OutcomeDeny:
			return nil, decision, nil
		case OutcomeChallenge:
			continue
		}
	}
	// All providers returned Challenge — credentials present but none accepted.
	return nil, Deny("chain", "credentials present but no provider accepted them"), nil
}

// RevokeSession delegates to every provider that supports it so all session
// stores that may hold the JTI are invalidated.
func (c *ChainedAuthN) RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error {
	supported := false
	for _, p := range c.providers {
		err := p.RevokeSession(ctx, jti, expiresAt)
		if err == nil {
			supported = true
			continue
		}
		if !errors.Is(err, ErrNotSupported) {
			return err
		}
	}
	if !supported {
		return ErrNotSupported
	}
	return nil
}

// RefreshSession delegates to the first provider that supports it.
func (c *ChainedAuthN) RefreshSession(ctx context.Context, oldToken string) (string, time.Time, error) {
	for _, p := range c.providers {
		token, exp, err := p.RefreshSession(ctx, oldToken)
		if err == nil {
			return token, exp, nil
		}
		if !errors.Is(err, ErrNotSupported) {
			return "", time.Time{}, err
		}
	}
	return "", time.Time{}, ErrNotSupported
}

// IssueSession delegates to the first provider that supports it.
func (c *ChainedAuthN) IssueSession(ctx context.Context, subject string) (string, time.Time, error) {
	for _, p := range c.providers {
		token, exp, err := p.IssueSession(ctx, subject)
		if err == nil {
			return token, exp, nil
		}
		if !errors.Is(err, ErrNotSupported) {
			return "", time.Time{}, err
		}
	}
	return "", time.Time{}, ErrNotSupported
}
