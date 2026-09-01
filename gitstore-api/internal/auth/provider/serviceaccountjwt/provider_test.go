// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountjwt

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// generateEd25519PEM returns a fresh Ed25519 private key PEM-encoded as
// PKCS#8, suitable for auth.serviceaccount.signing_key.
func generateEd25519PEM(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	block := &pem.Block{Type: "PRIVATE KEY", Bytes: der}
	return string(pem.EncodeToMemory(block))
}

// stubLookup is a minimal ServiceAccountLookup test double.
type stubLookup struct {
	accounts map[string]*datastore.ServiceAccount // keyed by "namespace/name"
	err      error
}

func (s *stubLookup) GetServiceAccountBySubject(_ context.Context, namespace, name string) (*datastore.ServiceAccount, error) {
	if s.err != nil {
		return nil, s.err
	}
	sa, ok := s.accounts[namespace+"/"+name]
	if !ok {
		return nil, datastore.ErrNotFound
	}
	return sa, nil
}

func newTestProvider(t *testing.T, pem string, lookup ServiceAccountLookup) *Provider {
	t.Helper()
	p, err := New(config.ServiceAccountConfig{
		Issuer:     "gitstore",
		Audience:   "gitstore-api",
		SigningKey: pem,
		DefaultTTL: "10m",
		MaxTTL:     "1h",
		ClockSkew:  "2m",
	}, lookup, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(p.Shutdown)
	return p
}

func bearerRequest(token string) auth.AuthRequest {
	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return auth.AuthRequest{Header: h}
}

func testServiceAccount(namespace, name, uid string) *datastore.ServiceAccount {
	return &datastore.ServiceAccount{
		UID:             uid,
		Namespace:       namespace,
		Name:            name,
		Disabled:        false,
		Generation:      1,
		ResourceVersion: "1",
	}
}

func TestServiceAccountJWT_RoundTrip_Allow(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "gitstore-controller-manager", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/gitstore-controller-manager": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, exp, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(10*time.Minute), exp, 2*time.Second)

	principal, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, "serviceaccount:controllers:gitstore-controller-manager", principal.Subject)
	assert.Equal(t, providerName, principal.AuthMethod)
	assert.Empty(t, principal.Roles, "roles must never be embedded — resolved exclusively by rbac-local")
}

func TestServiceAccountJWT_NoBearerToken_Challenge(t *testing.T) {
	p := newTestProvider(t, generateEd25519PEM(t), &stubLookup{})
	_, decision, err := p.Authenticate(context.Background(), bearerRequest(""))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestServiceAccountJWT_WrongIssuer_Challenge(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	p := newTestProvider(t, pemKey, &stubLookup{})

	// A token signed with a DIFFERENT issuer must fall through as "not my token".
	otherPEM := generateEd25519PEM(t)
	otherProvider := newTestProvider(t, otherPEM, &stubLookup{})
	_ = otherProvider
	priv, _ := jwt.ParseEdPrivateKeyFromPEM([]byte(otherPEM))
	claims := accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "some-other-issuer",
			Subject:   "serviceaccount:controllers:x",
			Audience:  jwt.ClaimStrings{"gitstore-api"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	tok.Header["kid"] = "irrelevant"
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(signed))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestServiceAccountJWT_BadSignature_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, _, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token+"tampered"))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_WrongAudience_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, err := p.signWithClaims(accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    p.issuer,
			Subject:   "serviceaccount:controllers:x",
			Audience:  jwt.ClaimStrings{"some-other-audience"},
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
		},
		ServiceAccountUID: sa.UID,
	})
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_Expired_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, err := p.signWithClaims(accessTokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    p.issuer,
			Subject:   "serviceaccount:controllers:x",
			Audience:  jwt.ClaimStrings{"gitstore-api"},
			IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Hour)),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour + time.Minute)),
		},
		ServiceAccountUID: sa.UID,
	})
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_SAUIDMismatch_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-current")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	// Token was signed for a previous incarnation of this namespace/name
	// pair (uid-old) — must be denied even though namespace/name match,
	// guarding against delete+recreate confusion (FR-005).
	staleSA := testServiceAccount("controllers", "x", "uid-old")
	token, _, err := p.IssueAccessToken(staleSA, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_DisabledAccount_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	sa.Disabled = true
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, _, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_DeletedAccount_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	now := time.Now()
	sa.DeletionTimestamp = &now
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, _, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_AccountNotFound_Deny(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	p := newTestProvider(t, pemKey, &stubLookup{}) // empty lookup — no such account

	token, _, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_TTLClampedToMaxTTL(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	p := newTestProvider(t, pemKey, &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}})

	_, exp, err := p.IssueAccessToken(sa, "", 24*time.Hour) // way beyond max_ttl (1h)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(time.Hour), exp, 2*time.Second)
}

func TestServiceAccountJWT_MultiKeyOverlapWindow_BothKeysVerify(t *testing.T) {
	oldPEM := generateEd25519PEM(t)
	newPEM := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}

	// The "new" key is listed first (active signer); the "old" key remains
	// in the bundle for the overlap window, per FR-013.
	oldProvider := newTestProvider(t, oldPEM, lookup)
	overlapProvider := newTestProvider(t, newPEM+"\n"+oldPEM, lookup)

	oldToken, _, err := oldProvider.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	// The overlap-window provider (trusting both keys) must still verify a
	// token signed by the outgoing key.
	_, decision, err := overlapProvider.Authenticate(context.Background(), bearerRequest(oldToken))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)

	// New tokens are signed with the new (first/active) key.
	newToken, _, err := overlapProvider.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)
	_, decision, err = overlapProvider.Authenticate(context.Background(), bearerRequest(newToken))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
}

func TestServiceAccountJWT_OverlapWindowEnded_OldKeyRejected(t *testing.T) {
	oldPEM := generateEd25519PEM(t)
	newPEM := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}

	oldProvider := newTestProvider(t, oldPEM, lookup)
	// Overlap window has ended: the old key's PEM block has been removed
	// from configuration, leaving only the new key trusted.
	newOnlyProvider := newTestProvider(t, newPEM, lookup)

	oldToken, _, err := oldProvider.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := newOnlyProvider.Authenticate(context.Background(), bearerRequest(oldToken))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_RevokeSession_DeniesSubsequentAuth(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "x", "uid-1")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, pemKey, lookup)

	token, exp, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)

	claims := &accessTokenClaims{}
	_, _, _ = jwt.NewParser().ParseUnverified(token, claims)
	require.NoError(t, p.RevokeSession(context.Background(), claims.ID, exp))

	_, decision, err = p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountJWT_RefreshSession_NotSupported(t *testing.T) {
	p := newTestProvider(t, generateEd25519PEM(t), &stubLookup{})
	_, _, err := p.RefreshSession(context.Background(), "irrelevant")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
}

func TestServiceAccountJWT_IssueSession_NotSupported(t *testing.T) {
	p := newTestProvider(t, generateEd25519PEM(t), &stubLookup{})
	_, _, err := p.IssueSession(context.Background(), "irrelevant")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
}

func TestServiceAccountJWT_Capabilities(t *testing.T) {
	p := newTestProvider(t, generateEd25519PEM(t), &stubLookup{})
	caps := p.Capabilities()
	assert.NotZero(t, caps&auth.CapAuthenticate)
	assert.Zero(t, caps&auth.CapIssueSession)
	assert.Zero(t, caps&auth.CapIntrospect)
}

func TestNew_EmptySigningKey_Errors(t *testing.T) {
	_, err := New(config.ServiceAccountConfig{Issuer: "gitstore", Audience: "gitstore-api"}, &stubLookup{}, zap.NewNop())
	require.Error(t, err)
}

func TestNew_InvalidDefaultTTL_Errors(t *testing.T) {
	_, err := New(config.ServiceAccountConfig{SigningKey: generateEd25519PEM(t), DefaultTTL: "not-a-duration"}, &stubLookup{}, zap.NewNop())
	require.Error(t, err)
}

func TestServiceAccountJWT_AuthenticateReturnsPrincipalWithEmptyRoles(t *testing.T) {
	pemKey := generateEd25519PEM(t)
	sa := testServiceAccount("controllers", "manager", "uid-123")
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/manager": sa}}
	p := newTestProvider(t, pemKey, lookup)

	// Issue a token for the service account
	token, _, err := p.IssueAccessToken(sa, "", 0)
	require.NoError(t, err)

	// Authenticate with that token
	principal, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)

	// Verify that Roles is always empty, regardless of any role_bindings entry
	// (FR-011: authorization must be resolved by rbac-local at request time,
	// never via roles embedded in the token or principal)
	assert.Empty(t, principal.Roles, "Principal.Roles must be empty for serviceaccount-jwt principal")
	assert.Equal(t, "serviceaccount:controllers:manager", principal.Subject)
	assert.Equal(t, "serviceaccount-jwt", principal.AuthMethod)
}
