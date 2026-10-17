// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/serviceaccountjwt"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
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
		DefaultTTL: 10 * time.Minute,
		MaxTTL:     1 * time.Hour,
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

func TestServiceAccountOperationsRejectNamespace(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	_, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "legacy", "manager", "key"))
	require.ErrorContains(t, err, "cluster-scoped")
	metadata := &model.ObjectMetaInput{Namespace: "legacy", Name: "manager"}
	_, err = h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{Metadata: metadata})
	require.ErrorContains(t, err, "cluster-scoped")
	_, err = h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{Metadata: metadata})
	require.ErrorContains(t, err, "cluster-scoped")
	_, err = h.resolver.IssueServiceAccountToken(ctx, &model.IssueServiceAccountTokenInput{Metadata: metadata, Spec: &model.TokenRequestSpecInput{}})
	require.ErrorContains(t, err, "cluster-scoped")
}

func TestCreateServiceAccountValidatesKeysAndDuplicates(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	input := serviceAccountCreateInput(t, "", "manager", "key-1")

	created, err := h.resolver.CreateServiceAccount(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.Equal(t, []string{"key-1"}, created.ServiceAccount.KeyIDs)
	assert.NotEmpty(t, created.ServiceAccount.Metadata.UID)
	assert.Empty(t, created.ServiceAccount.Metadata.Namespace)
	assert.Equal(t, "manager", created.ServiceAccount.Metadata.Name)

	persisted, err := h.store.GetServiceAccountBySubject(ctx, "manager")
	require.NoError(t, err)
	assert.Equal(t, "admin", persisted.CreationActor)

	_, err = h.resolver.CreateServiceAccount(ctx, input)
	require.ErrorContains(t, err, "already exists")

	_, err = h.resolver.CreateServiceAccount(ctx, &model.CreateServiceAccountInput{
		Metadata:   &model.ObjectMetaInput{Name: "without-key"},
		PublicKeys: nil,
	})
	require.ErrorContains(t, err, "at least one public key")

}

func TestRotateServiceAccountKeyPreservesOverlapAndRejectsEmptyRotation(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "", "manager", "old"))
	require.NoError(t, err)

	overlap, err := h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata: &model.ObjectMetaInput{Name: "manager"},
		Add:      []*model.ServiceAccountPublicKeyInput{serviceAccountPublicKeyInput(t, "new")},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"old", "new"}, overlap.ServiceAccount.KeyIDs)
	assert.Equal(t, created.ServiceAccount.Metadata.UID, overlap.ServiceAccount.Metadata.UID)

	rotated, err := h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata:   &model.ObjectMetaInput{Name: "manager"},
		RemoveKids: []string{"old"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"new"}, rotated.ServiceAccount.KeyIDs)
	assert.Equal(t, created.ServiceAccount.Metadata.UID, rotated.ServiceAccount.Metadata.UID)

	_, err = h.resolver.RotateServiceAccountKey(ctx, &model.RotateServiceAccountKeyInput{
		Metadata:   &model.ObjectMetaInput{Name: "manager"},
		RemoveKids: []string{"new"},
	})
	require.ErrorIs(t, err, datastore.ErrInvalidArgument)
	persisted, err := h.store.GetServiceAccountBySubject(ctx, "manager")
	require.NoError(t, err)
	require.Len(t, persisted.PublicKeys, 1)
	assert.Equal(t, "new", persisted.PublicKeys[0].KeyID)

}

func TestDeleteServiceAccountIsIdempotentAndRevokesAuthentication(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	ctx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "", "manager", "key-1"))
	require.NoError(t, err)
	account, err := h.store.GetServiceAccountBySubject(ctx, "manager")
	require.NoError(t, err)
	token, _, err := h.provider.IssueAccessToken(account, "gitstore-api", time.Minute)
	require.NoError(t, err)

	_, decision, err := h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)

	deleted, err := h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{
		Metadata: &model.ObjectMetaInput{Name: "manager"},
	})
	require.NoError(t, err)
	assert.Equal(t, created.ServiceAccount.Metadata.UID, deleted.ServiceAccount.Metadata.UID)

	_, decision, err = h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeDeny, decision.Outcome)

	deleted, err = h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{
		Metadata: &model.ObjectMetaInput{Name: "manager"},
	})
	require.NoError(t, err)
	assert.Nil(t, deleted.ServiceAccount, "deleting an absent account returns a null serviceAccount")
}

type serviceAccountDeletionFailureStore struct {
	datastore.Datastore
	deletionErr, confirmationErr, subjectErr error
	removeAuthority                          bool
	replacement                              *datastore.ServiceAccount
}

func (s serviceAccountDeletionFailureStore) DeleteServiceAccount(ctx context.Context, uid string) error {
	if s.removeAuthority {
		if err := s.Datastore.DeleteServiceAccount(ctx, uid); err != nil {
			return err
		}
		if s.replacement != nil {
			if err := s.Datastore.CreateServiceAccount(ctx, s.replacement); err != nil {
				return err
			}
		}
	}
	return s.deletionErr
}

func (s serviceAccountDeletionFailureStore) GetServiceAccountByUID(ctx context.Context, uid string) (*datastore.ServiceAccount, error) {
	if s.confirmationErr != nil {
		return nil, s.confirmationErr
	}
	return s.Datastore.GetServiceAccountByUID(ctx, uid)
}

func (s serviceAccountDeletionFailureStore) GetServiceAccountBySubject(ctx context.Context, name string) (*datastore.ServiceAccount, error) {
	if s.subjectErr != nil {
		return nil, s.subjectErr
	}
	return s.Datastore.GetServiceAccountBySubject(ctx, name)
}

func TestDeleteServiceAccountLookupFailurePreservesAbsenceAndCauses(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{"absent", datastore.ErrNotFound},
		{"wrapped absence", fmt.Errorf("lookup ServiceAccount: %w", datastore.ErrNotFound)},
		{"storage unavailable", errors.New("private subject lookup detail")},
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceAccountResolverHarness(t)
			h.resolver.store = serviceAccountDeletionFailureStore{Datastore: h.store, subjectErr: test.err}
			h.resolver.connectionRegistry = wsregistry.New()
			connection, cancel := context.WithCancel(t.Context())
			defer cancel()
			h.resolver.connectionRegistry.Register(t.Context(), "unrelated-uid", cancel)
			result, err := h.resolver.DeleteServiceAccount(serviceAccountAdminContext(), &model.DeleteServiceAccountInput{
				Metadata: &model.ObjectMetaInput{Name: "manager"},
			})
			require.NoError(t, connection.Err(), "subject lookup failure must not authorize UID revocation")
			if errors.Is(test.err, datastore.ErrNotFound) {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Nil(t, result.ServiceAccount)
			} else {
				require.Nil(t, result)
				require.ErrorIs(t, err, test.err)
				encoded, marshalErr := json.Marshal(err)
				require.NoError(t, marshalErr)
				require.JSONEq(t, `{"message":"delete ServiceAccount"}`, string(encoded))
			}
		})
	}
}

func TestDeleteServiceAccountPartialCleanupPreservesFailureAndUIDRevocation(t *testing.T) {
	for _, test := range []struct {
		name            string
		removed         bool
		confirmationErr error
		deletionCause   error
	}{
		{"authority removed", true, nil, nil},
		{"authority retained", false, nil, nil},
		{"authority conflict", false, nil, datastore.ErrConflict},
		{"confirmation unavailable", true, errors.New("private confirmation detail"), nil},
		{"confirmation canceled", true, context.Canceled, nil},
		{"wrapped absence confirmation", true, fmt.Errorf("confirm UID: %w", datastore.ErrNotFound), nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newServiceAccountResolverHarness(t)
			ctx := serviceAccountAdminContext()
			_, err := h.resolver.CreateServiceAccount(ctx, serviceAccountCreateInput(t, "", "manager", "key-1"))
			require.NoError(t, err)
			account, err := h.store.GetServiceAccountBySubject(ctx, "manager")
			require.NoError(t, err)
			replacement := *account
			replacement.UID = "01960000-0000-7000-8000-000000000199"
			replacement.CreationTimestamp = account.CreationTimestamp.Add(time.Second)
			oldToken, _, err := h.provider.IssueAccessToken(account, "gitstore-api", time.Minute)
			require.NoError(t, err)
			deletionErr := errors.New("private listing cleanup detail")
			if test.deletionCause != nil {
				deletionErr = test.deletionCause
			}
			failed := serviceAccountDeletionFailureStore{
				Datastore: h.store, deletionErr: deletionErr, confirmationErr: test.confirmationErr,
				removeAuthority: test.removed, replacement: &replacement,
			}
			h.resolver.store = failed
			h.resolver.connectionRegistry = wsregistry.New()
			oldConnection, cancelOld := context.WithCancel(t.Context())
			defer cancelOld()
			newConnection, cancelNew := context.WithCancel(t.Context())
			defer cancelNew()
			h.resolver.connectionRegistry.Register(ctx, account.UID, cancelOld)
			h.resolver.connectionRegistry.Register(ctx, replacement.UID, cancelNew)
			result, err := h.resolver.DeleteServiceAccount(ctx, &model.DeleteServiceAccountInput{
				Metadata: &model.ObjectMetaInput{Name: account.Name},
			})
			require.Nil(t, result, "partial cleanup must not fabricate a successful deletion payload")
			require.ErrorIs(t, err, deletionErr)
			if test.confirmationErr != nil && !errors.Is(test.confirmationErr, datastore.ErrNotFound) {
				require.ErrorIs(t, err, test.confirmationErr)
			}
			require.NotErrorIs(t, err, datastore.ErrNotFound, "absence confirmation must not turn partial cleanup into a not-found response")
			encoded, marshalErr := json.Marshal(err)
			require.NoError(t, marshalErr)
			require.JSONEq(t, `{"message":"delete ServiceAccount"}`, string(encoded))
			require.NotContains(t, err.Error(), "private")
			require.NotContains(t, string(encoded), "private")
			if test.removed && (test.confirmationErr == nil || errors.Is(test.confirmationErr, datastore.ErrNotFound)) {
				require.ErrorIs(t, oldConnection.Err(), context.Canceled)
			} else {
				require.NoError(t, oldConnection.Err(), "failed or unconfirmed deletion must not revoke a live UID")
			}
			require.NoError(t, newConnection.Err(), "old-UID cleanup must never revoke the name's replacement UID")
			_, decision, authErr := h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
				Header: http.Header{"Authorization": []string{"Bearer " + oldToken}},
			})
			require.NoError(t, authErr)
			if test.removed {
				require.Equal(t, auth.OutcomeDeny, decision.Outcome, "the old credential must fail even if deletion confirmation is unavailable")
				got, lookupErr := h.store.GetServiceAccountBySubject(ctx, account.Name)
				require.NoError(t, lookupErr)
				require.Equal(t, replacement.UID, got.UID)
				newToken, _, issueErr := h.provider.IssueAccessToken(got, "gitstore-api", time.Minute)
				require.NoError(t, issueErr)
				principal, newDecision, newAuthErr := h.resolver.registry.AuthN().Authenticate(ctx, auth.AuthRequest{
					Header: http.Header{"Authorization": []string{"Bearer " + newToken}},
				})
				require.NoError(t, newAuthErr)
				require.Equal(t, auth.OutcomeAllow, newDecision.Outcome)
				require.Equal(t, replacement.UID, principal.ServiceAccountUID)
			} else {
				require.Equal(t, auth.OutcomeAllow, decision.Outcome, "a failed pre-delete operation must preserve the live credential")
			}
		})
	}
}

func TestIssueServiceAccountTokenRequiresMatchingAssertionIdentityAndClampsTTL(t *testing.T) {
	h := newServiceAccountResolverHarness(t)
	adminCtx := serviceAccountAdminContext()
	created, err := h.resolver.CreateServiceAccount(adminCtx, serviceAccountCreateInput(t, "", "manager", "key-1"))
	require.NoError(t, err)
	ttl := int32(7200)
	input := &model.IssueServiceAccountTokenInput{
		Metadata: &model.ObjectMetaInput{Name: "manager"},
		Spec:     &model.TokenRequestSpecInput{Audiences: []string{"gitstore-api"}, ExpirationSeconds: &ttl},
	}
	assertionCtx := auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		Subject:           datastore.ServiceAccountSubject("manager"),
		AuthMethod:        "serviceaccount-assertion",
		ServiceAccountUID: created.ServiceAccount.Metadata.UID,
	})
	before := time.Now()
	issued, err := h.resolver.IssueServiceAccountToken(assertionCtx, input)
	require.NoError(t, err)
	assert.NotEmpty(t, issued.TokenRequest.Status.Token)
	assert.WithinDuration(t, before.Add(time.Hour), issued.TokenRequest.Status.ExpirationTimestamp, 2*time.Second)
	// spec.expirationSeconds echoes the effective (clamped) lifetime, not the 7200s request.
	require.NotNil(t, issued.TokenRequest.Spec.ExpirationSeconds)
	assert.InDelta(t, 3600, *issued.TokenRequest.Spec.ExpirationSeconds, 2)

	principal, decision, err := h.resolver.registry.AuthN().Authenticate(assertionCtx, auth.AuthRequest{
		Header: http.Header{"Authorization": []string{"Bearer " + issued.TokenRequest.Status.Token}},
	})
	require.NoError(t, err)
	assert.Equal(t, auth.OutcomeAllow, decision.Outcome)
	assert.Equal(t, created.ServiceAccount.Metadata.UID, principal.ServiceAccountUID)

	for _, principal := range []*auth.Principal{
		{Subject: datastore.ServiceAccountSubject("other"), AuthMethod: "serviceaccount-assertion", ServiceAccountUID: created.ServiceAccount.Metadata.UID},
		{Subject: datastore.ServiceAccountSubject("manager"), AuthMethod: "serviceaccount-assertion", ServiceAccountUID: "other-uid"},
		{Subject: "admin", AuthMethod: "static-admin"},
	} {
		_, err := h.resolver.IssueServiceAccountToken(auth.ContextWithPrincipal(context.Background(), principal), input)
		require.Error(t, err)
	}
}
