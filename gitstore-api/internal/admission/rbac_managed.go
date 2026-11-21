// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package admission

import (
	"context"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
)

func managedBinding(resource *rbac.Resource) bool {
	return resource != nil && resource.Kind == rbac.RoleBinding && resource.Metadata.Name == rbac.ManagedBindingName
}

func (v RBACValidator) RequireController(ctx context.Context, principal *auth.Principal) error {
	if v.ManagedStore == nil || v.ControllerSubject == "" || !auth.ValidTrustedPrincipal(principal) ||
		principal.Subject != v.ControllerSubject || principal.ServiceAccountUID == "" ||
		(principal.AuthMethod != "serviceaccount-jwt" && principal.AuthMethod != "serviceaccount-assertion") {
		return NewError(CodeForbidden, "RBAC_CONTROLLER_REQUIRED", "the enrolled RBAC controller is required")
	}
	name, ok := strings.CutPrefix(principal.Subject, "system:serviceaccount:")
	if !ok {
		return rbac.ErrUnavailable
	}
	account, err := v.ManagedStore.GetServiceAccountByUID(ctx, principal.ServiceAccountUID)
	if err != nil || account == nil || account.Name != name || account.UID != principal.ServiceAccountUID || account.Disabled || account.DeletionTimestamp != nil {
		return rbac.ErrUnavailable
	}
	return nil
}

// ManagedProjectionProtection checks durable template facts, not historical
// actor permissions. It is also used after a committed write loses its callback.
func (v RBACValidator) ManagedProjectionProtection(ctx context.Context, namespace, uid string) (rbac.Protection, error) {
	protection := rbac.Protection{Builtins: v.Builtins}
	if v.ManagedStore == nil {
		return protection, rbac.ErrUnavailable
	}
	parent, err := v.ManagedStore.GetNamespaceByName(ctx, namespace)
	if err != nil || parent == nil || parent.UID != uid || parent.Tier != datastore.NamespaceTierUser {
		return protection, rbac.ErrUnavailable
	}
	template := &rbac.ManagedBindingTemplate{Namespace: parent.Name, NamespaceUID: parent.UID, Subject: parent.EffectiveOwnerSub()}
	if _, err := template.Resource(); err != nil {
		return protection, err
	}
	protection.Managed = template
	return protection, nil
}

func (v RBACValidator) managedAuthorProtection(ctx context.Context, principal *auth.Principal, origin rbac.SnapshotOrigin) (rbac.Protection, error) {
	protection, err := v.ManagedProjectionProtection(ctx, origin.Namespace, origin.NamespaceUID)
	if err != nil {
		return protection, err
	}
	if err := v.RequireController(ctx, principal); err != nil {
		return protection, err
	}
	parent, err := v.ManagedStore.GetNamespaceByName(ctx, origin.Namespace)
	if err != nil || parent == nil || parent.ResourceVersion != origin.NamespaceVersion ||
		parent.DeletionTimestamp != nil || datastore.HasDeletionIntent(parent.Status) || origin.Root || !origin.Active {
		return protection, rbac.ErrUnavailable
	}
	registry := v.Capture.Registry
	if registry == nil || registry.UserDir() == nil {
		return protection, rbac.ErrUnavailable
	}
	directory := registry.UserDir()
	var profile *auth.UserProfile
	if qualified, ok := directory.(auth.ActorDirectory); ok {
		owner := &auth.Principal{Subject: protection.Managed.Subject,
			AuthMethod: parent.Annotations[datastore.OwnerAuthMethodAnnotationKey],
			Issuer:     parent.Annotations[datastore.OwnerIssuerAnnotationKey]}
		if !auth.ValidTrustedPrincipal(owner) || owner.AuthMethod == "none" || owner.Issuer == "" {
			return protection, rbac.ErrUnavailable
		}
		profile, err = qualified.ResolvePrincipal(ctx, owner)
	} else {
		profile, err = directory.GetBySubject(ctx, protection.Managed.Subject)
	}
	if err != nil {
		return protection, rbac.ErrUnavailable
	}
	if profile == nil || profile.Subject == "" || !auth.ValidExternalUsername(profile.Username) ||
		(!profile.FreshUntil.IsZero() && !profile.FreshUntil.After(time.Now())) ||
		profile.Kind != auth.ProfileStaff || !profile.Active || profile.Lifecycle != auth.ActorActive {
		return protection, rbac.ErrInvalid
	}
	return protection, nil
}
