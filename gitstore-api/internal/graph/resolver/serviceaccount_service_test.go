// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

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
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountjwt"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestNormalizeServiceAccountPublicKey_Ed25519(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	publicKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	key, err := normalizeServiceAccountPublicKey(&model.ServiceAccountPublicKeyInput{
		Kid:          "key-1",
		Algorithm:    "Ed25519",
		PublicKeyPem: string(publicKeyPEM),
	})
	require.NoError(t, err)
	assert.Equal(t, der, key.PublicKey)
	assert.Equal(t, "Ed25519", key.Algorithm)
	assert.Equal(t, "key-1", key.KeyID)
}

func TestNormalizeServiceAccountPublicKey_RejectsInvalidInput(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	validEd25519PEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))

	tests := []struct {
		name  string
		input *model.ServiceAccountPublicKeyInput
	}{
		{
			name: "malformed PEM",
			input: &model.ServiceAccountPublicKeyInput{
				Kid: "key-1", Algorithm: "Ed25519", PublicKeyPem: "not a PEM block",
			},
		},
		{
			name: "wrong algorithm",
			input: &model.ServiceAccountPublicKeyInput{
				Kid: "key-1", Algorithm: "ECDSA-P256",
				PublicKeyPem: validEd25519PEM,
			},
		},
		{name: "nil key", input: nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := normalizeServiceAccountPublicKey(test.input)
			require.Error(t, err)
		})
	}
}

type serviceAccountTestAuthz struct {
	denyAction string
}

func (*serviceAccountTestAuthz) Name() string { return "service-account-test" }

func (a *serviceAccountTestAuthz) Authorize(_ context.Context, _ *auth.Principal, action string, _ auth.ResourceContext) (auth.Decision, error) {
	if action == a.denyAction {
		return auth.Deny(a.Name(), "test deny"), nil
	}
	return auth.Allow(a.Name(), "test allow"), nil
}

type serviceAccountResolverHarness struct {
	resolver *Resolver
	store    datastore.Datastore
	provider *serviceaccountjwt.Provider
	authz    *serviceAccountTestAuthz
}

func newServiceAccountResolverHarness(t *testing.T) *serviceAccountResolverHarness {
	t.Helper()
	store, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	require.NoError(t, err)
	provider, err := serviceaccountjwt.New(config.ServiceAccountConfig{
		Audience:   "gitstore-api",
		SigningKey: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
		DefaultTTL: "10m",
		MaxTTL:     "1h",
	}, store, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(provider.Shutdown)

	authz := &serviceAccountTestAuthz{}
	root, err := NewResolver(ResolverDeps{
		Store:                  store,
		Registry:               auth.NewProviderRegistry(auth.NewChainedAuthN(provider), authz, nil),
		Logger:                 zap.NewNop(),
		ServiceAccountAudience: "gitstore-api",
	})
	require.NoError(t, err)
	return &serviceAccountResolverHarness{resolver: root, store: store, provider: provider, authz: authz}
}

func serviceAccountAdminContext() context.Context {
	return auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:    "admin",
		AuthMethod: "static-admin",
		Roles:      []string{"admin"},
	})
}

func serviceAccountPublicKeyInput(t *testing.T, keyID string) *model.ServiceAccountPublicKeyInput {
	t.Helper()
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(publicKey)
	require.NoError(t, err)
	return &model.ServiceAccountPublicKeyInput{
		Kid:          keyID,
		Algorithm:    "Ed25519",
		PublicKeyPem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}
}

func serviceAccountCreateInput(t *testing.T, namespace, name, keyID string) *model.CreateServiceAccountInput {
	t.Helper()
	return &model.CreateServiceAccountInput{
		APIVersion: "gitstore.dev/v1beta1",
		Kind:       "ServiceAccount",
		Metadata:   &model.ObjectMetaInput{Namespace: namespace, Name: name},
		PublicKeys: []*model.ServiceAccountPublicKeyInput{serviceAccountPublicKeyInput(t, keyID)},
	}
}

func TestCreateServiceAccountValidatesKeysAndDuplicates(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	input := serviceAccountCreateInput(t, "controllers", "manager", "key-1")

	created, err := h.resolver.CreateServiceAccount(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, []string{"key-1"}, created.KeyIDs)
	assert.NotEmpty(t, created.Metadata.UID)
	assert.Equal(t, "controllers", created.Metadata.Namespace)
	assert.Equal(t, "manager", created.Metadata.Name)

	persisted, err := h.store.GetServiceAccountBySubject(ctx, "controllers", "manager")
	require.NoError(t, err)
	assert.Equal(t, "admin", persisted.CreationActor)

	_, err = h.resolver.CreateServiceAccount(ctx, input)
	require.ErrorContains(t, err, "already exists")

	_, err = h.resolver.CreateServiceAccount(ctx, &model.CreateServiceAccountInput{
		Metadata:   &model.ObjectMetaInput{Namespace: "controllers", Name: "without-key"},
		PublicKeys: nil,
	})
	require.ErrorContains(t, err, "at least one public key")

}

func TestRotateServiceAccountKeyPreservesOverlapAndRejectsEmptyRotation(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "controllers", "manager", "old"))
	require.NoError(t, err)

	overlap, err := h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata: &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
		Add:      []*model.ServiceAccountPublicKeyInput{serviceAccountPublicKeyInput(t, "new")},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"old", "new"}, overlap.KeyIDs)
	assert.Equal(t, created.Metadata.UID, overlap.Metadata.UID)

	rotated, err := h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata:   &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
		RemoveKids: []string{"old"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, rotated.KeyIDs)
	assert.Equal(t, created.Metadata.UID, rotated.Metadata.UID)

	_, err = h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata:   &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
		RemoveKids: []string{"new"},
	})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	persisted, err := h.store.GetServiceAccountBySubject(ctx, "controllers", "manager")
	require.NoError(t, err)
	require.Len(t, persisted.PublicKeys, 1)
	assert.Equal(t, "new", persisted.PublicKeys[0].KeyID)

}

func TestDeleteServiceAccountIsIdempotentAndRevokesAuthentication(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "controllers", "manager", "key-1"))
	require.NoError(t, err)
	account, err := h.store.GetServiceAccountBySubject(ctx, "controllers", "manager")
	require.NoError(t, err)
	token, _, err := h.provider.IssueAccessToken(account, "gitstore-api", time.Minute)
	require.NoError(t, err)

	_, decision, err := h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)

	deleted, err := h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{
		Metadata: &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
	})
	require.NoError(t, err)
	assert.Equal(t, created.Metadata.UID, deleted.Metadata.UID)

	_, decision, err = h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)

	deleted, err = h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{
		Metadata: &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
	})
	require.NoError(t, err)
	assert.Empty(t, deleted.Metadata.UID)

}

func TestIssueServiceAccountTokenRequiresMatchingAssertionIdentityAndClampsTTL(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	adminCtx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(adminCtx, serviceAccountCreateInput(t, "controllers", "manager", "key-1"))
	require.NoError(t, err)
	ttl := int32(7200)
	input := &model.IssueServiceAccountTokenInput{
		Metadata: &model.ObjectMetaInput{Namespace: "controllers", Name: "manager"},
		Spec:     &model.TokenRequestSpec{Audience: stringPtr("gitstore-api"), TTLSeconds: &ttl},
	}
	assertionCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:           datastore.ServiceAccountSubject("controllers", "manager"),
		AuthMethod:        "serviceaccount-assertion",
		ServiceAccountUID: created.Metadata.UID,
	})
	before := time.Now()
	issued, err := h.resolver.IssueServiceAccountToken(assertionCtx, input)
	require.NoError(t, err)
	assert.NotEmpty(t, issued.Status.Token)
	assert.WithinDuration(t, before.Add(time.Hour), issued.Status.ExpiresAt, 2*time.Second)

	principal, decision, err := h.resolver.registry.AuthN().Authenticate(assertionCtx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + issued.Status.Token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, created.Metadata.UID, principal.ServiceAccountUID)

	for _, principal := range []*auth.Principal{
		{Subject: datastore.ServiceAccountSubject("controllers", "other"), AuthMethod: "serviceaccount-assertion", ServiceAccountUID: created.Metadata.UID},
		{Subject: datastore.ServiceAccountSubject("controllers", "manager"), AuthMethod: "serviceaccount-assertion", ServiceAccountUID: "other-uid"},
		{Subject: "admin", AuthMethod: "static-admin"},
	} {
		_, err := h.resolver.IssueServiceAccountToken(auth.ContextWithPrincipal(context.Background(), principal), input)
		require.Error(t, err)
	}
}

func stringPtr(value string) *string {
	return &value
}
