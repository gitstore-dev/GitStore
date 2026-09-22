// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// CreateServiceAccount creates a new service account with the given metadata and public keys.
func (r *Resolver) CreateServiceAccount(ctx context.Context, input *model.CreateServiceAccountInput) (*model.CreateServiceAccountPayload, error) {
	if input == nil || input.Metadata == nil {
		return nil, gqlerror.Errorf("metadata is required")
	}
	if len(input.PublicKeys) == 0 {
		return nil, gqlerror.Errorf("at least one public key is required")
	}
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.Subject == "" {
		return nil, gqlerror.Errorf("authenticated principal is required")
	}

	namespace := input.Metadata.Namespace
	name := input.Metadata.Name

	// Check if already exists
	existing, err := r.store.GetServiceAccountBySubject(ctx, namespace, name)
	if err == nil && existing != nil {
		return nil, gqlerror.Errorf("service account %s:%s already exists", namespace, name)
	}
	if err != nil && err != datastore.ErrNotFound {
		return nil, err
	}

	// Convert public keys
	pubKeys := make([]datastore.ServiceAccountPublicKey, len(input.PublicKeys))
	for i, pk := range input.PublicKeys {
		key, err := normalizeServiceAccountPublicKey(pk)
		if err != nil {
			return nil, gqlerror.Errorf("invalid public key: %s", err)
		}
		pubKeys[i] = key
	}

	// Create the service account
	now := time.Now().UTC()
	sa := &datastore.ServiceAccount{
		UID:               uuid.New().String(),
		Namespace:         namespace,
		Name:              name,
		Generation:        1,
		ResourceVersion:   "1",
		CreationTimestamp: now,
		CreationActor:     principal.Subject,
		UpdateTimestamp:   now,
		UpdateActor:       principal.Subject,
		PublicKeys:        pubKeys,
	}

	err = r.store.CreateServiceAccount(ctx, sa)
	if err != nil {
		return nil, err
	}

	return &model.CreateServiceAccountPayload{
		ServiceAccount: serviceAccountToModel(sa),
	}, nil
}

// serviceAccountObjectMeta projects a datastore ServiceAccount onto the shared
// ObjectMeta envelope. A ServiceAccount does not carry the full catalog
// envelope, so the fields it lacks are populated with safe defaults: nil
// labels/annotations, no revision, and — importantly — empty (non-nil) slices
// for the non-null OwnerReferences/Finalizers list fields, without which
// gqlgen marshaling would fail at runtime. The UID is returned raw (a
// ServiceAccount is not a Relay Node).
func serviceAccountObjectMeta(sa *datastore.ServiceAccount) *model.ObjectMeta {
	return &model.ObjectMeta{
		Name:              sa.Name,
		Namespace:         sa.Namespace,
		UID:               sa.UID,
		ResourceVersion:   sa.ResourceVersion,
		Generation:        int32(sa.Generation),
		CreationTimestamp: sa.CreationTimestamp,
		OwnerReferences:   []*model.OwnerReference{},
		Finalizers:        []string{},
	}
}

// serviceAccountToModel builds the GraphQL ServiceAccount object returned by
// the create/rotate/delete payloads.
func serviceAccountToModel(sa *datastore.ServiceAccount) *model.ServiceAccount {
	keyIDs := make([]string, len(sa.PublicKeys))
	for i, pk := range sa.PublicKeys {
		keyIDs[i] = pk.KeyID
	}
	status := model.ActorStatusActive
	if sa.Disabled {
		status = model.ActorStatusInactive
	}
	return &model.ServiceAccount{
		APIVersion: "authentication.gitstore.dev/v1beta1",
		Kind:       "ServiceAccount",
		Metadata:   serviceAccountObjectMeta(sa),
		KeyIDs:     keyIDs,
		Status:     status,
	}
}

// RotateServiceAccountKey updates the public keys for a service account.
func (r *Resolver) RotateServiceAccountKey(ctx context.Context, input *model.RotateServiceAccountKeyInput) (*model.RotateServiceAccountKeyPayload, error) {
	if input == nil || input.Metadata == nil {
		return nil, gqlerror.Errorf("metadata is required")
	}
	namespace := input.Metadata.Namespace
	name := input.Metadata.Name

	// Get existing service account
	sa, err := r.store.GetServiceAccountBySubject(ctx, namespace, name)
	if err != nil {
		return nil, err
	}

	// Convert new keys for addition
	addKeys := make([]datastore.ServiceAccountPublicKey, len(input.Add))
	for i, pk := range input.Add {
		key, err := normalizeServiceAccountPublicKey(pk)
		if err != nil {
			return nil, gqlerror.Errorf("invalid public key: %s", err)
		}
		addKeys[i] = key
	}

	// Update service account keys
	updated, err := r.store.UpdateServiceAccountKeys(ctx, sa.UID, addKeys, input.RemoveKids, sa.ResourceVersion)
	if err != nil {
		return nil, err
	}

	// Check that we don't end up with empty keys
	if len(updated.PublicKeys) == 0 {
		return nil, gqlerror.Errorf("rotation would result in empty public key set")
	}

	return &model.RotateServiceAccountKeyPayload{
		ServiceAccount: serviceAccountToModel(updated),
	}, nil
}

// DeleteServiceAccount deletes a service account. Deletion of non-existent accounts is a no-op.
func (r *Resolver) DeleteServiceAccount(ctx context.Context, input *model.DeleteServiceAccountInput) (*model.DeleteServiceAccountPayload, error) {
	if input == nil || input.Metadata == nil {
		return nil, gqlerror.Errorf("metadata is required")
	}
	namespace := input.Metadata.Namespace
	name := input.Metadata.Name

	sa, err := r.store.GetServiceAccountBySubject(ctx, namespace, name)
	if err == datastore.ErrNotFound {
		// Idempotent: the account is already absent. The payload field is
		// nullable, so return null rather than fabricating a zero-valued
		// ServiceAccount that would misreport status ACTIVE.
		return &model.DeleteServiceAccountPayload{ServiceAccount: nil}, nil
	}
	if err != nil {
		return nil, err
	}

	// Delete the service account
	err = r.store.DeleteServiceAccount(ctx, sa.UID)
	if err != nil {
		return nil, err
	}
	if r.connectionRegistry != nil {
		r.connectionRegistry.CancelAll(sa.UID)
	}

	return &model.DeleteServiceAccountPayload{
		ServiceAccount: serviceAccountToModel(sa),
	}, nil
}

// SetServiceAccountDisabled applies the ServiceAccount disabled state and
// synchronously revokes all of its process-local WebSocket connections.
func (r *Resolver) SetServiceAccountDisabled(ctx context.Context, uid string, disabled bool) error {
	if err := r.store.SetServiceAccountDisabled(ctx, uid, disabled); err != nil {
		return err
	}
	if r.connectionRegistry == nil {
		return nil
	}
	if disabled {
		r.connectionRegistry.CancelAll(uid)
	}
	return nil
}

// IssueServiceAccountToken issues a short-lived access token for a service account.
func (r *Resolver) IssueServiceAccountToken(ctx context.Context, input *model.IssueServiceAccountTokenInput) (*model.IssueServiceAccountTokenPayload, error) {
	if input == nil || input.Metadata == nil || input.Spec == nil {
		return nil, gqlerror.Errorf("metadata and spec are required")
	}

	namespace := input.Metadata.Namespace
	name := input.Metadata.Name
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.AuthMethod != "serviceaccount-assertion" {
		return nil, gqlerror.Errorf("service account assertion authentication is required")
	}
	expectedSubject := datastore.ServiceAccountSubject(namespace, name)
	if principal.Subject != expectedSubject {
		return nil, gqlerror.Errorf("service account can only issue tokens for itself")
	}

	sa, err := r.store.GetServiceAccountBySubject(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("could not find service account: %w", err)
	}
	if principal.ServiceAccountUID != sa.UID {
		return nil, gqlerror.Errorf("service account identity does not match the current service account")
	}
	// Determine audience: use input or configured default. Every requested
	// audience must match the single configured value.
	audience := r.serviceAccountAudience
	for _, requested := range input.Spec.Audiences {
		if requested == "" {
			continue
		}
		if requested != r.serviceAccountAudience {
			return nil, gqlerror.Errorf("requested audience %q does not match configured audience %q", requested, r.serviceAccountAudience)
		}
		audience = requested
	}

	// Determine TTL: use input or provider default
	ttlSeconds := 600 // default 10 minutes
	if input.Spec.ExpirationSeconds != nil {
		ttlSeconds = int(*input.Spec.ExpirationSeconds)
	}

	// Issue token via the provider chain
	// The IssueServiceAccountToken method is defined on ChainedAuthN
	ttl := time.Duration(ttlSeconds) * time.Second
	token, expiresAt, err := r.registry.AuthN().IssueServiceAccountToken(sa, audience, ttl)
	if err != nil {
		return nil, err
	}

	// Echo the effective lifetime the provider actually issued (after any
	// max-TTL clamping or default substitution), not the raw request, so
	// spec.expirationSeconds agrees with status.expirationTimestamp.
	effectiveTTL := int32(time.Until(expiresAt).Round(time.Second) / time.Second)
	return &model.IssueServiceAccountTokenPayload{
		TokenRequest: &model.TokenRequest{
			APIVersion: "authentication.gitstore.dev/v1beta1",
			Kind:       "TokenRequest",
			Metadata:   serviceAccountObjectMeta(sa),
			Spec: &model.TokenRequestSpec{
				Audiences:         []string{audience},
				ExpirationSeconds: &effectiveTTL,
			},
			Status: &model.TokenRequestStatus{
				Token:               token,
				ExpirationTimestamp: expiresAt,
			},
		},
	}, nil
}

func normalizeServiceAccountPublicKey(input *model.ServiceAccountPublicKeyInput) (datastore.ServiceAccountPublicKey, error) {
	if input == nil {
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("key is required")
	}
	if strings.TrimSpace(input.Kid) == "" {
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("kid is required")
	}

	block, rest := pem.Decode([]byte(input.PublicKeyPem))
	if block == nil {
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("publicKeyPEM is not PEM encoded")
	}
	if len(strings.TrimSpace(string(rest))) != 0 {
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("publicKeyPEM must contain exactly one PEM block")
	}
	publicKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("parse public key: %w", err)
	}

	switch key := publicKey.(type) {
	case ed25519.PublicKey:
		if input.Algorithm != "Ed25519" {
			return datastore.ServiceAccountPublicKey{}, fmt.Errorf("Ed25519 key requires algorithm Ed25519")
		}
	case *ecdsa.PublicKey:
		if input.Algorithm != "ECDSA-P256" {
			return datastore.ServiceAccountPublicKey{}, fmt.Errorf("ECDSA P-256 key requires algorithm ECDSA-P256")
		}
		if key.Curve.Params().Name != "P-256" {
			return datastore.ServiceAccountPublicKey{}, fmt.Errorf("unsupported ECDSA curve %q", key.Curve.Params().Name)
		}
	default:
		return datastore.ServiceAccountPublicKey{}, fmt.Errorf("unsupported public key type %T", publicKey)
	}

	return datastore.ServiceAccountPublicKey{
		KeyID:     input.Kid,
		Algorithm: input.Algorithm,
		PublicKey: block.Bytes,
	}, nil
}
