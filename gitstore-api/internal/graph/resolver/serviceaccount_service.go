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
	principal, err := r.authorizeServiceAccountAction(ctx, "serviceaccount.create", "")
	if err != nil {
		return nil, err
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
	if _, err := r.authorizeServiceAccountAction(ctx, "serviceaccount.key.rotate", ""); err != nil {
		return nil, err
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
	if _, err := r.authorizeServiceAccountAction(ctx, "serviceaccount.delete", ""); err != nil {
		return nil, err
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
	if r.connectionRegistry != nil {
		r.connectionRegistry.CancelAll(sa.UID)
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
	if _, err := r.authorizeServiceAccountAction(ctx, "serviceaccount.token.issue", expectedSubject); err != nil {
		return nil, err
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

func (r *Resolver) authorizeServiceAccountAction(ctx context.Context, action, name string) (*auth.Principal, error) {
	principal := auth.PrincipalFromContext(ctx)
	if principal == nil || principal.Subject == "" || principal.AuthMethod == "" || principal.AuthMethod == "none" {
		return nil, gqlerror.Errorf("authenticated principal is required")
	}
	if r.registry == nil || r.registry.AuthZ() == nil {
		return nil, gqlerror.Errorf("authorization service unavailable")
	}
	decision, err := r.registry.AuthZ().Authorize(ctx, principal, action, auth.ResourceContext{
		Kind: "serviceAccount",
		Name: name,
	})
	if err != nil {
		return nil, gqlerror.Errorf("authorization error")
	}
	if decision.Outcome != auth.OutcomeAllow {
		return nil, gqlerror.Errorf("permission denied: %s", decision.Reason)
	}
	return principal, nil
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
