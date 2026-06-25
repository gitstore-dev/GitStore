// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// ErrNotSupported is returned by providers for operations they do not implement.
var ErrNotSupported = errors.New("auth: operation not supported by this provider")

// Outcome is the result of an auth decision.
type Outcome uint8

const (
	OutcomeAllow Outcome = iota
	OutcomeDeny
	OutcomeChallenge // credentials present but not owned by this provider — try next
)

// Decision is returned by AuthNProvider.Authenticate and AuthZProvider.Authorize.
type Decision struct {
	Outcome   Outcome   `json:"outcome"`
	Reason    string    `json:"reason"`
	RequestID string    `json:"request_id"`
	At        time.Time `json:"at"`
	Provider  string    `json:"provider"`
}

func Allow(provider, reason string) Decision {
	return Decision{Outcome: OutcomeAllow, Provider: provider, Reason: reason, At: time.Now()}
}

func Deny(provider, reason string) Decision {
	return Decision{Outcome: OutcomeDeny, Provider: provider, Reason: reason, At: time.Now()}
}

func Challenge(provider, reason string) Decision {
	return Decision{Outcome: OutcomeChallenge, Provider: provider, Reason: reason, At: time.Now()}
}

// Principal is the provider-agnostic identity passed through the system.
type Principal struct {
	Subject    string         `json:"sub"`
	Issuer     string         `json:"iss"`
	Tenant     string         `json:"tenant,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Groups     []string       `json:"groups,omitempty"`
	Roles      []string       `json:"roles,omitempty"`
	Scopes     []string       `json:"scopes,omitempty"`
	Claims     map[string]any `json:"claims,omitempty"`
	AuthMethod string         `json:"auth_method"`
	ExpiresAt  time.Time      `json:"exp,omitempty"`
}

// IsAdmin returns true when the principal carries the built-in "admin" role.
func (p *Principal) IsAdmin() bool {
	for _, r := range p.Roles {
		if r == "admin" {
			return true
		}
	}
	return false
}

// Anonymous returns a Principal with no identity.
func Anonymous() *Principal {
	return &Principal{Subject: "anon", Issuer: "gitstore", AuthMethod: "none"}
}

// Capability flags returned by AuthNProvider.Capabilities.
type Capability uint32

const (
	CapAuthenticate Capability = 1 << iota
	CapIssueSession
	CapIntrospect
	CapGroupResolution
	CapUserLookup
)

// AuthRequest wraps the inbound HTTP request passed to AuthNProvider.Authenticate.
type AuthRequest struct {
	Header     http.Header
	RemoteAddr string
	// ForwardedSubject is non-empty when the request arrived via gRPC metadata.
	ForwardedSubject string
}

// AuthNProvider authenticates an inbound request and returns a Principal + Decision.
type AuthNProvider interface {
	Name() string
	Capabilities() Capability
	// Authenticate returns OutcomeAllow+Principal on success, OutcomeDeny to hard-fail,
	// or nil Principal+OutcomeChallenge to signal "not my token, try next".
	Authenticate(ctx context.Context, req AuthRequest) (*Principal, Decision, error)
	RevokeSession(ctx context.Context, jti string, expiresAt time.Time) error
	RefreshSession(ctx context.Context, oldToken string) (newToken string, exp time.Time, err error)
}

// ResourceContext carries the resource identifier for AuthZProvider.Authorize.
type ResourceContext struct {
	Kind     string // e.g. "namespace", "repository"
	Name     string
	OwnerSub string
	Attrs    map[string]any
}

// AuthZProvider makes access-control decisions for a given (principal, action, resource).
type AuthZProvider interface {
	Name() string
	// Authorize returns Allow or Deny. action follows dot-notation: "namespace.delete.any".
	Authorize(ctx context.Context, p *Principal, action string, res ResourceContext) (Decision, error)
}

// UserProfile is a provider-agnostic user record.
type UserProfile struct {
	Subject     string
	DisplayName string
	Email       string
	Groups      []string
	Active      bool
}

// UserDirProvider provides user-directory operations.
type UserDirProvider interface {
	Name() string
	GetBySubject(ctx context.Context, subject string) (*UserProfile, error)
	ListGroups(ctx context.Context, subject string) ([]string, error)
	SearchUsers(ctx context.Context, query string, limit int) ([]*UserProfile, error)
	UpsertProfile(ctx context.Context, p *UserProfile) error
	Deactivate(ctx context.Context, subject string) error
}
