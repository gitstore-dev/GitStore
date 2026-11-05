// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ErrNotSupported is returned by providers for operations they do not implement.
var ErrNotSupported = errors.New("auth: operation not supported by this provider")

// ErrTokenTooOld is returned when a token has exceeded the refresh grace window.
var ErrTokenTooOld = errors.New("auth: token too old to refresh")

// ErrTokenRevoked is returned when a token has been explicitly revoked.
var ErrTokenRevoked = errors.New("auth: token has been revoked")

// ErrInvalidToken is returned when a refresh token cannot be parsed or validated.
var ErrInvalidToken = errors.New("auth: invalid token")

const MaxPrincipalGroups = 256

// VisibilityFromSpec exposes only an existing, typed visibility fact. Missing,
// null, malformed or wrong-type values remain absent, never implicitly public.
// Repository manifests already permit upper/lower-case enum spellings.
func VisibilityFromSpec(spec json.RawMessage) (string, bool) {
	var fields struct {
		Visibility *string `json:"visibility"`
	}
	if json.Unmarshal(spec, &fields) != nil || fields.Visibility == nil {
		return "", false
	}
	return strings.ToLower(*fields.Visibility), true
}

// ValidTrustedPrincipal checks a sanitized identity snapshot after its transport
// has been authenticated. It does not authenticate user-supplied claims.
func ValidTrustedPrincipal(p *Principal) bool {
	text := func(s string) bool {
		return s != "" && len(s) <= 256 && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
	}
	if p == nil || !text(p.Subject) || !text(p.AuthMethod) || len(p.Groups) > MaxPrincipalGroups {
		return false
	}
	for _, group := range p.Groups {
		if !text(group) {
			return false
		}
	}
	if p.Subject == "system:api" {
		return p.AuthMethod == "internal" && p.ServiceAccountUID == ""
	}
	if name, ok := strings.CutPrefix(p.Subject, "system:serviceaccount:"); ok {
		return text(name) && !strings.ContainsAny(name, ":/") && text(p.ServiceAccountUID) && (p.AuthMethod == "serviceaccount-jwt" || p.AuthMethod == "serviceaccount-assertion")
	}
	if p.Subject == "anon" {
		return p.AuthMethod == "none" && p.ServiceAccountUID == ""
	}
	return ValidExternalUsername(p.Subject) && p.ServiceAccountUID == "" && p.AuthMethod != "internal" && p.AuthMethod != "none" && !strings.HasPrefix(p.AuthMethod, "serviceaccount-")
}

// ValidExternalUsername excludes identities that only trusted in-process work
// or verified ServiceAccount providers may construct.
func ValidExternalUsername(subject string) bool {
	return subject != "" && subject != "anon" && len(subject) <= 256 && utf8.ValidString(subject) &&
		!strings.HasPrefix(subject, "system:") && !strings.HasPrefix(subject, "serviceaccount:")
}

// normalizeAuthenticatedPrincipal runs once at the successful AuthN boundary.
// It does not interpret token claims as group membership or authorization.
func normalizeAuthenticatedPrincipal(provider string, principal *Principal) (*Principal, bool) {
	if principal == nil || len(principal.Groups) > MaxPrincipalGroups {
		return nil, false
	}
	normalized := *principal
	normalized.Groups = nil
	anonymous := provider == "anonymous" && principal.Subject == "anon" && principal.AuthMethod == "none" && principal.ServiceAccountUID == ""
	saProvider := provider == "serviceaccount-jwt" || provider == "serviceaccount-assertion"
	if anonymous {
		normalized.Groups = []string{"system:unauthenticated"}
		return &normalized, true
	}
	if saProvider {
		name, ok := strings.CutPrefix(principal.Subject, "system:serviceaccount:")
		if !ok || name == "" || strings.ContainsAny(name, ":/") || len(principal.Subject) > 256 || !utf8.ValidString(name) || principal.ServiceAccountUID == "" || principal.AuthMethod != provider {
			return nil, false
		}
	} else if !ValidExternalUsername(principal.Subject) || principal.ServiceAccountUID != "" || principal.AuthMethod == "none" || principal.AuthMethod == "internal" || strings.HasPrefix(principal.AuthMethod, "serviceaccount-") {
		return nil, false
	}
	seen := make(map[string]bool, len(principal.Groups)+2)
	for _, group := range principal.Groups {
		if group == "" || len(group) > 256 || !utf8.ValidString(group) {
			return nil, false
		}
		if group == "system:authenticated" || group == "system:unauthenticated" || group == "system:serviceaccounts" || seen[group] {
			continue
		}
		seen[group] = true
		normalized.Groups = append(normalized.Groups, group)
	}
	normalized.Groups = append(normalized.Groups, "system:authenticated")
	if saProvider {
		normalized.Groups = append(normalized.Groups, "system:serviceaccounts")
	}
	if !ValidTrustedPrincipal(&normalized) {
		return nil, false
	}
	return &normalized, true
}

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
	// PolicyDigest identifies the immutable policy used, not a later snapshot.
	PolicyDigest string `json:"policy_digest,omitempty"`
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
	Scopes     []string       `json:"scopes,omitempty"`
	Claims     map[string]any `json:"claims,omitempty"`
	AuthMethod string         `json:"auth_method"`
	ExpiresAt  time.Time      `json:"exp,omitempty"`
	// TokenID is the JWT jti claim — unique per-token identifier used for revocation.
	// Empty for Basic Auth sessions and anonymous principals.
	TokenID           string `json:"jti,omitempty"`
	ServiceAccountUID string `json:"service_account_uid,omitempty"`
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
	// IssueSession mints a new session token for the given subject.
	// Returns ErrNotSupported for providers that cannot issue tokens.
	IssueSession(ctx context.Context, subject string) (token string, exp time.Time, err error)
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
	// Authorize returns Allow or Deny for a registered action, e.g. "namespace.delete".
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
