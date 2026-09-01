// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"fmt"
	"time"

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
		pubKeys[i] = datastore.ServiceAccountPublicKey{
			KeyID:     pk.Kid,
			Algorithm: pk.Algorithm,
			PublicKey: []byte(pk.PublicKeyPem),
		}
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
		CreationActor:     "serviceaccount", // Principal.Subject from context
		UpdateTimestamp:   now,
		UpdateActor:       "serviceaccount",
		PublicKeys:        pubKeys,
	}

	err = r.store.CreateServiceAccount(ctx, sa)
	if err != nil {
		return nil, err
	}

	keyIDs := make([]string, len(pubKeys))
	for i, pk := range pubKeys {
		keyIDs[i] = pk.KeyID
	}

	return &model.CreateServiceAccountPayload{
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Metadata: &model.ServiceAccountObjectMeta{
			Namespace:         sa.Namespace,
			Name:              sa.Name,
			UID:               sa.UID,
			CreationTimestamp: sa.CreationTimestamp,
		},
		KeyIDs:   keyIDs,
		Disabled: sa.Disabled,
	}, nil
}

// RotateServiceAccountKey updates the public keys for a service account.
func (r *Resolver) RotateServiceAccountKey(ctx context.Context, input *model.RotateServiceAccountKeyInput) (*model.CreateServiceAccountPayload, error) {
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
		addKeys[i] = datastore.ServiceAccountPublicKey{
			KeyID:     pk.Kid,
			Algorithm: pk.Algorithm,
			PublicKey: []byte(pk.PublicKeyPem),
		}
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

	keyIDs := make([]string, len(updated.PublicKeys))
	for i, pk := range updated.PublicKeys {
		keyIDs[i] = pk.KeyID
	}

	return &model.CreateServiceAccountPayload{
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Metadata: &model.ServiceAccountObjectMeta{
			Namespace:         updated.Namespace,
			Name:              updated.Name,
			UID:               updated.UID,
			CreationTimestamp: updated.CreationTimestamp,
		},
		KeyIDs:   keyIDs,
		Disabled: updated.Disabled,
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
		// Idempotent: already deleted
		return &model.DeleteServiceAccountPayload{
			APIVersion: "v1",
			Kind:       "ServiceAccount",
			Metadata: &model.ServiceAccountObjectMeta{
				Namespace: namespace,
				Name:      name,
			},
		}, nil
	}
	if err != nil {
		return nil, err
	}

	// Delete the service account
	err = r.store.DeleteServiceAccount(ctx, sa.UID)
	if err != nil {
		return nil, err
	}

	return &model.DeleteServiceAccountPayload{
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Metadata: &model.ServiceAccountObjectMeta{
			Namespace:         sa.Namespace,
			Name:              sa.Name,
			UID:               sa.UID,
			CreationTimestamp: sa.CreationTimestamp,
		},
	}, nil
}

// IssueServiceAccountToken issues a short-lived access token for a service account.
func (r *Resolver) IssueServiceAccountToken(ctx context.Context, input *model.IssueServiceAccountTokenInput) (*model.IssueServiceAccountTokenPayload, error) {
	if input == nil || input.Metadata == nil || input.Spec == nil {
		return nil, gqlerror.Errorf("metadata and spec are required")
	}

	namespace := input.Metadata.Namespace
	name := input.Metadata.Name

	// The caller must already be authenticated as this service account (field-level gate ensures this)
	// Get the service account to pass to provider
	sa, err := r.store.GetServiceAccountBySubject(ctx, namespace, name)
	if err != nil {
		return nil, fmt.Errorf("could not find service account: %w", err)
	}

	// Determine audience: use input or configured default
	audience := r.serviceAccountAudience
	if input.Spec.Audience != nil && *input.Spec.Audience != "" {
		// Validate that requested audience matches configured value
		if *input.Spec.Audience != r.serviceAccountAudience {
			return nil, gqlerror.Errorf("requested audience %q does not match configured audience %q", *input.Spec.Audience, r.serviceAccountAudience)
		}
		audience = *input.Spec.Audience
	}

	// Determine TTL: use input or provider default
	ttlSeconds := 600 // default 10 minutes
	if input.Spec.TTLSeconds != nil {
		ttlSeconds = int(*input.Spec.TTLSeconds)
	}

	// Issue token via the provider chain
	// The IssueServiceAccountToken method is defined on ChainedAuthN
	ttl := time.Duration(ttlSeconds) * time.Second
	token, expiresAt, err := r.registry.AuthN().IssueServiceAccountToken(sa, audience, ttl)
	if err != nil {
		return nil, err
	}

	return &model.IssueServiceAccountTokenPayload{
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Metadata: &model.ServiceAccountObjectMeta{
			Namespace:         sa.Namespace,
			Name:              sa.Name,
			UID:               sa.UID,
			CreationTimestamp: sa.CreationTimestamp,
		},
		Status: &model.TokenRequestStatus{
			Token:     token,
			ExpiresAt: expiresAt,
		},
	}, nil
}
