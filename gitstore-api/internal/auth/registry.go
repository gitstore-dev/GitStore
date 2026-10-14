// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

// ServiceAccountTokenIssuer is implemented by AuthN providers (currently only
// serviceaccount-jwt) that can mint ServiceAccount access tokens for the
// issueServiceAccountToken GraphQL mutation (spec 061). It is a narrower,
// optional capability than the generic IssueSession — issuance needs the
// full ServiceAccount record (for its UID and enrolled keys) plus explicit
// audience/TTL parameters that IssueSession's single-subject signature
// cannot express.
type ServiceAccountTokenIssuer interface {
	IssueAccessToken(sa *datastore.ServiceAccount, audience string, requestedTTL time.Duration) (token string, expiresAt time.Time, err error)
}

// ProviderRegistry holds the active provider for each auth plane.
type ProviderRegistry struct {
	mu         sync.RWMutex
	authnChain *ChainedAuthN
	authz      AuthZProvider
	userdir    UserDirProvider
}

// AuthorizationSnapshot contains bounded, non-secret metadata from one immutable
// policy snapshot. Providers implementing it must not perform I/O.
type AuthorizationSnapshot struct {
	Loaded     bool   `json:"loaded"`
	APIVersion string `json:"apiVersion"`
	Digest     string `json:"digest"`
	Roles      int    `json:"roles"`
	Bindings   int    `json:"bindings"`
	Rules      int    `json:"rules"`
}

// AuthorizationSnapshotProvider preserves optional metadata through decorators;
// false means the wrapped provider does not support snapshot diagnostics.
type AuthorizationSnapshotProvider interface {
	AuthorizationSnapshot() (AuthorizationSnapshot, bool)
}

type ProviderDiagnostics struct {
	AuthN         []string               `json:"authn"`
	AuthZ         string                 `json:"authz"`
	UserDir       string                 `json:"userdir"`
	CatalogDigest string                 `json:"catalogDigest"`
	Policy        *AuthorizationSnapshot `json:"policy,omitempty"`
}

// Hash the compiled, canonically ordered tuples rather than a deployment file.
var authorizationCatalogDigest = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint(authorizationActions))))

func (r *ProviderRegistry) Diagnostics() ProviderDiagnostics {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := ProviderDiagnostics{CatalogDigest: authorizationCatalogDigest, AuthN: []string{}}
	if r.authnChain != nil {
		seen := make(map[string]bool)
		for _, provider := range r.authnChain.providers {
			name := provider.Name()
			if !seen[name] {
				result.AuthN = append(result.AuthN, name)
				seen[name] = true
			}
		}
	}
	if r.authz != nil {
		result.AuthZ = r.authz.Name()
		if provider, ok := r.authz.(AuthorizationSnapshotProvider); ok {
			if snapshot, available := provider.AuthorizationSnapshot(); available {
				result.Policy = &snapshot
			}
		}
	}
	if r.userdir != nil {
		result.UserDir = r.userdir.Name()
	}
	return result
}

// EffectivePermissionChecker is an optional local-policy validation capability;
// it does not change the runtime AuthZProvider contract or enumerate resources.
type EffectivePermissionChecker interface {
	HasEffectivePermission(*Principal) bool
}

// ValidateActiveUserPermissions checks every configured active user, including
// the groups synthesized at authentication. A bound admin cannot mask an
// unbound user. Disabled directory profiles do not need a grant.
func ValidateActiveUserPermissions(checker EffectivePermissionChecker, users []*UserProfile) error {
	for _, user := range users {
		if user == nil || !user.Active {
			continue
		}
		groups := append([]string(nil), user.Groups...)
		groups = append(groups, "system:authenticated")
		if !checker.HasEffectivePermission(&Principal{Subject: user.Subject, AuthMethod: "static-users", Groups: groups}) {
			return fmt.Errorf("rbac-local: active local user %q has no satisfiable v1beta1 grant; add a User or Group binding", user.Subject)
		}
	}
	return nil
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
			normalized, valid := normalizeAuthenticatedPrincipal(p.Name(), principal)
			if !valid {
				return nil, Deny(p.Name(), "invalid or reserved authenticated identity"), nil
			}
			return normalized, decision, nil
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

// IssueServiceAccountToken delegates to the first chained provider
// implementing ServiceAccountTokenIssuer (currently only serviceaccount-jwt).
// Returns ErrNotSupported if no provider in the chain supports it, e.g. when
// "serviceaccount-jwt" is not configured in auth.authn.chain.
func (c *ChainedAuthN) IssueServiceAccountToken(sa *datastore.ServiceAccount, audience string, requestedTTL time.Duration) (string, time.Time, error) {
	for _, p := range c.providers {
		issuer, ok := p.(ServiceAccountTokenIssuer)
		if !ok {
			continue
		}
		return issuer.IssueAccessToken(sa, audience, requestedTTL)
	}
	return "", time.Time{}, ErrNotSupported
}
