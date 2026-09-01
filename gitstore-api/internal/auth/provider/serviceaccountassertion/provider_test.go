// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package serviceaccountassertion

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
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

// generateEd25519Keypair returns an Ed25519 private key plus the DER-encoded
// (PKIX) public key bytes matching datastore.ServiceAccountPublicKey's
// "raw public key bytes (PEM-decoded at load, stored decoded)" contract.
func generateEd25519Keypair(t *testing.T) (ed25519.PrivateKey, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return priv, der
}

func generateECDSAP256Keypair(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	require.NoError(t, err)
	return priv, der
}

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

func newTestProvider(t *testing.T, lookup ServiceAccountLookup) *Provider {
	t.Helper()
	p, err := New(config.ServiceAccountConfig{
		AssertionAudience: "gitstore-api/serviceaccount-token",
		ClockSkew:         "2m",
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

// signAssertion signs claims with priv using the EdDSA method, sets the
// typ/kid protected headers, and returns the compact JWT.
func signAssertion(t *testing.T, priv ed25519.PrivateKey, kid string, typ string, claims assertionClaims) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, claims)
	if typ != "" {
		tok.Header["typ"] = typ
	}
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

// validAssertionClaims returns claims satisfying every check in
// data-model.md §3 for the given subject/uid, valid for the caller to
// mutate specific fields for negative-path tests.
func validAssertionClaims(subject, uid string) assertionClaims {
	now := time.Now()
	return assertionClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    subject,
			Subject:   subject,
			Audience:  jwt.ClaimStrings{"gitstore-api/serviceaccount-token"},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)),
			ID:        "jti-" + subject,
		},
		ServiceAccountUID: uid,
	}
}

func testServiceAccount(namespace, name, uid, kid string, pubDER []byte) *datastore.ServiceAccount {
	return &datastore.ServiceAccount{
		UID:       uid,
		Namespace: namespace,
		Name:      name,
		Disabled:  false,
		PublicKeys: []datastore.ServiceAccountPublicKey{
			{KeyID: kid, Algorithm: "Ed25519", PublicKey: pubDER, EnrolledAt: time.Now()},
		},
	}
}

func TestServiceAccountAssertion_RoundTrip_Allow(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "gitstore-controller-manager")
	sa := testServiceAccount("controllers", "gitstore-controller-manager", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/gitstore-controller-manager": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	principal, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	require.NotNil(t, principal)
	assert.Equal(t, subject, principal.Subject)
	assert.Equal(t, providerName, principal.AuthMethod)
	assert.Empty(t, principal.Roles)
}

func TestServiceAccountAssertion_NoBearerToken_Challenge(t *testing.T) {
	p := newTestProvider(t, &stubLookup{})
	_, decision, err := p.Authenticate(context.Background(), bearerRequest(""))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestServiceAccountAssertion_WrongTyp_Challenge(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", "some-other-typ", validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestServiceAccountAssertion_MissingTyp_Challenge(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", "", validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeChallenge, decision.Outcome)
}

func TestServiceAccountAssertion_AccountNotFound_Deny(t *testing.T) {
	priv, _ := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "ghost")
	p := newTestProvider(t, &stubLookup{})

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_DisabledAccount_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	sa.Disabled = true
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_DeletedAccount_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	now := time.Now()
	sa.DeletionTimestamp = &now
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_UIDMismatch_UntrustedClaim_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-current", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	// The untrusted peek's sa_uid doesn't match the current record — must
	// be denied before signature verification even runs.
	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-stale"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_UnknownKID_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "no-such-key", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_BadSignature_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	otherPriv, _ := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	// Signed with a DIFFERENT private key than the enrolled public key.
	token := signAssertion(t, otherPriv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))
	_ = priv

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_WrongAudience_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.Audience = jwt.ClaimStrings{"some-other-audience"}
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_IssuerNotSubject_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.Issuer = "not-the-subject"
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_SubjectNotIssuer_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.Issuer = subject
	claims.Subject = "serviceaccount:controllers:someone-else"
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_Expired_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour + 30*time.Second))
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_LifetimeExceeds60s_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.IssuedAt = jwt.NewNumericDate(time.Now())
	// exp-iat = 5 minutes, well beyond the 60s bound, but still "not
	// expired" from the library's own leeway-based exp check — this
	// exercises this provider's manual lifetime check specifically.
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(5 * time.Minute))
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_MissingJTI_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	claims.ID = ""
	token := signAssertion(t, priv, "key-1", assertionTyp, claims)

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_ReplayedJTI_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := testServiceAccount("controllers", "x", "uid-1", "key-1", pubDER)
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)

	// Second use of the exact same assertion (same jti) must be denied —
	// this is the authoritative single-use replay control.
	_, decision, err = p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_ECDSAP256_RoundTrip_Allow(t *testing.T) {
	priv, pubDER := generateECDSAP256Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := &datastore.ServiceAccount{
		UID:       "uid-1",
		Namespace: "controllers",
		Name:      "x",
		PublicKeys: []datastore.ServiceAccountPublicKey{
			{KeyID: "key-1", Algorithm: "ECDSA-P256", PublicKey: pubDER, EnrolledAt: time.Now()},
		},
	}
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	claims := validAssertionClaims(subject, "uid-1")
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	tok.Header["typ"] = assertionTyp
	tok.Header["kid"] = "key-1"
	token, err := tok.SignedString(priv)
	require.NoError(t, err)

	principal, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	require.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, subject, principal.Subject)
}

func TestServiceAccountAssertion_AlgorithmMismatchWithEnrollment_Deny(t *testing.T) {
	priv, pubDER := generateEd25519Keypair(t)
	subject := datastore.ServiceAccountSubject("controllers", "x")
	sa := &datastore.ServiceAccount{
		UID:       "uid-1",
		Namespace: "controllers",
		Name:      "x",
		PublicKeys: []datastore.ServiceAccountPublicKey{
			// Mislabeled: the key bytes are Ed25519 but Algorithm claims ECDSA-P256.
			{KeyID: "key-1", Algorithm: "ECDSA-P256", PublicKey: pubDER, EnrolledAt: time.Now()},
		},
	}
	lookup := &stubLookup{accounts: map[string]*datastore.ServiceAccount{"controllers/x": sa}}
	p := newTestProvider(t, lookup)

	token := signAssertion(t, priv, "key-1", assertionTyp, validAssertionClaims(subject, "uid-1"))

	_, decision, err := p.Authenticate(context.Background(), bearerRequest(token))
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)
}

func TestServiceAccountAssertion_RevokeSession_NotSupported(t *testing.T) {
	p := newTestProvider(t, &stubLookup{})
	err := p.RevokeSession(context.Background(), "jti", time.Now())
	assert.ErrorIs(t, err, auth.ErrNotSupported)
}

func TestServiceAccountAssertion_RefreshSession_NotSupported(t *testing.T) {
	p := newTestProvider(t, &stubLookup{})
	_, _, err := p.RefreshSession(context.Background(), "irrelevant")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
}

func TestServiceAccountAssertion_IssueSession_NotSupported(t *testing.T) {
	p := newTestProvider(t, &stubLookup{})
	_, _, err := p.IssueSession(context.Background(), "irrelevant")
	assert.ErrorIs(t, err, auth.ErrNotSupported)
}

func TestServiceAccountAssertion_Capabilities(t *testing.T) {
	p := newTestProvider(t, &stubLookup{})
	caps := p.Capabilities()
	assert.NotZero(t, caps&auth.CapAuthenticate)
	assert.Zero(t, caps&auth.CapIssueSession)
	assert.Zero(t, caps&auth.CapIntrospect)
}

func TestNew_InvalidClockSkew_Errors(t *testing.T) {
	_, err := New(config.ServiceAccountConfig{ClockSkew: "not-a-duration"}, &stubLookup{}, zap.NewNop())
	require.Error(t, err)
}
