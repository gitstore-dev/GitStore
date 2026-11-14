// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func guardWatch[T any](ctx context.Context, r *Resolver, kind string, namespace *string, source <-chan T, cancel context.CancelFunc) <-chan T {
	if r.registry == nil || r.registry.AuthZ() == nil || r.registry.AuthZ().Name() != "opa" {
		return source
	}
	out := make(chan T)
	go func() {
		defer cancel()
		defer close(out)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		principal := auth.PrincipalFromContext(ctx)
		if principal == nil {
			principal = auth.Anonymous()
		}
		original := principal
		var refreshAt time.Time
		resourceKind := strings.ToLower(kind[:1]) + kind[1:]
		resource := auth.ResourceContext{Kind: resourceKind}
		if namespace != nil && *namespace != "" {
			resource.Attrs = map[string]any{"namespace": *namespace}
		}
		check := func() error {
			select {
			case r.watchRechecks <- struct{}{}:
				defer func() { <-r.watchRechecks }()
			default:
				auth.ObserveRBACUnavailable(ctx, "evaluation", namespace == nil || *namespace == "")
				return rbacUnavailableError()
			}
			checkCtx, stop := context.WithTimeout(ctx, time.Second)
			defer stop()
			now := r.clock.Now()
			if !principal.ExpiresAt.IsZero() && !now.Before(principal.ExpiresAt) {
				return admission.NewError(admission.CodeForbidden, "IDENTITY_EXPIRED", "identity expired").ToGQLError()
			}
			if !now.Before(refreshAt) || !principal.ProfileExpiresAt.IsZero() && !now.Before(principal.ProfileExpiresAt) {
				if original.ServiceAccountUID != "" {
					name, ok := datastore.ParseServiceAccountSubject(original.Subject)
					if !ok || r.store == nil {
						return rbacUnavailableError()
					}
					account, err := r.store.GetServiceAccountBySubject(checkCtx, name)
					if err != nil || account == nil || account.Disabled || account.DeletionTimestamp != nil ||
						account.UID != original.ServiceAccountUID {
						return rbacUnavailableError()
					}
				}
				enriched, err := auth.EnrichPrincipal(checkCtx, r.registry.UserDir(), original)
				if err != nil || enriched == nil ||
					(principal.ProfileKind != "" || principal.Username != "") && enriched.ProfileKind == "" && enriched.Username == "" {
					return rbacUnavailableError()
				}
				principal, refreshAt = enriched, now.Add(time.Second)
			}
			provider := r.registry.AuthZ()
			if provider == nil || provider.Name() != "opa" {
				return rbacUnavailableError()
			}
			decision, err := provider.Authorize(checkCtx, principal, resourceKind+".watch", resource)
			if err != nil {
				return rbacUnavailableError()
			}
			if decision.Outcome != auth.OutcomeAllow {
				return admission.NewError(admission.CodeForbidden, "WATCH_PERMISSION_REVOKED", "watch permission revoked").ToGQLError()
			}
			return nil
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := check(); err != nil {
					addNamespaceWatchSubscriptionError(ctx, err)
					return
				}
			case value, ok := <-source:
				if !ok {
					return
				}
				if err := check(); err != nil {
					addNamespaceWatchSubscriptionError(ctx, err)
					return
				}
			deliver:
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if err := check(); err != nil {
							addNamespaceWatchSubscriptionError(ctx, err)
							return
						}
					case out <- value:
						break deliver
					}
				}
			}
		}
	}()
	return out
}
