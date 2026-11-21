// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	apiruntime "github.com/gitstore-dev/gitstore/api/internal/runtime"
	"github.com/stretchr/testify/require"
)

type watchGuardProvider struct {
	mode atomic.Int32
	seen chan struct{}
}

func (*watchGuardProvider) Name() string { return "opa" }
func (p *watchGuardProvider) Authorize(_ context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	select {
	case p.seen <- struct{}{}:
	default:
	}
	if p.mode.Load() == 2 {
		return auth.Deny("opa", "unavailable"), errors.New("private source detail")
	}
	if p.mode.Load() == 1 || principal.ServiceAccountUID == "" && !slices.Contains(principal.Groups, "system:group:readers") ||
		action != "product.watch" || resource.Attrs["namespace"] != "shop" {
		return auth.Deny("opa", "denied"), nil
	}
	return auth.Allow("opa", "allowed"), nil
}

type watchGuardDirectory struct {
	auth.UserDirProvider
	revoked   atomic.Bool
	lifecycle atomic.Int32
}

func (d *watchGuardDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	if d.lifecycle.Load() == 1 {
		return nil, auth.ErrProfileNotFound
	}
	return &auth.UserProfile{Subject: "alice", Username: "alice", Kind: auth.ProfileStaff,
		Active: d.lifecycle.Load() != 2, Lifecycle: auth.ActorActive}, nil
}
func (d *watchGuardDirectory) ListGroups(context.Context, string) ([]string, error) {
	if d.revoked.Load() {
		return nil, nil
	}
	return []string{"system:group:readers"}, nil
}

func TestWatchGuardClosesIdleAndBackpressuredStreamsAfterRevocation(t *testing.T) {
	for _, mode := range []string{"policy", "unavailable", "membership", "deleted-profile", "inactive-profile", "expired-token", "backpressure", "saturation"} {
		t.Run(mode, func(t *testing.T) {
			provider := &watchGuardProvider{seen: make(chan struct{}, 8)}
			directory := &watchGuardDirectory{}
			r := &Resolver{registry: auth.NewProviderRegistry(nil, provider, directory),
				clock: apiruntime.SystemClock{}, watchRechecks: make(chan struct{}, 1)}
			principal := &auth.Principal{
				Subject: "alice", AuthMethod: "static-users", Groups: []string{"system:authenticated"},
			}
			if mode == "expired-token" {
				principal.ExpiresAt = time.Now().Add(500 * time.Millisecond)
			}
			ctx, stop := context.WithCancel(auth.ContextWithPrincipal(t.Context(), principal))
			defer stop()
			source := make(chan int, 2)
			upstreamStopped := make(chan struct{})
			namespace := "shop"
			out := guardWatch(ctx, r, "Product", &namespace, source, func() { close(upstreamStopped) })
			source <- 1
			select {
			case value := <-out:
				require.Equal(t, 1, value)
			case <-time.After(time.Second):
				t.Fatal("authorized watch did not deliver")
			}
			select {
			case <-provider.seen:
			default:
			}
			switch mode {
			case "policy":
				provider.mode.Store(1)
			case "unavailable":
				provider.mode.Store(2)
			case "membership":
				directory.revoked.Store(true)
			case "deleted-profile":
				directory.lifecycle.Store(1)
			case "inactive-profile":
				directory.lifecycle.Store(2)
			case "backpressure":
				source <- 2
				select {
				case <-provider.seen:
				case <-time.After(time.Second):
					t.Fatal("guard did not reach blocked delivery")
				}
				provider.mode.Store(1)
			case "saturation":
				r.watchRechecks <- struct{}{}
			}
			select {
			case <-upstreamStopped:
			case <-time.After(3 * time.Second):
				t.Fatal("watch was not closed within the reauthorization bound")
			}
			_, ok := <-out
			require.False(t, ok, "revoked buffered events must not escape the guard")
		})
	}
}

type watchGuardAccountStore struct {
	datastore.Datastore
	mode atomic.Int32
}

func (s *watchGuardAccountStore) GetServiceAccountBySubject(context.Context, string) (*datastore.ServiceAccount, error) {
	account := &datastore.ServiceAccount{UID: "00000000-0000-4000-8000-000000000123"}
	switch s.mode.Load() {
	case 1:
		account.Disabled = true
	case 2:
		now := time.Now()
		account.DeletionTimestamp = &now
	case 3:
		account.UID = "00000000-0000-4000-8000-000000000456"
	case 4:
		return nil, errors.New("private account lookup error")
	}
	return account, nil
}

func TestWatchGuardRefreshesServiceAccountLifecycleWithoutDirectorySupport(t *testing.T) {
	for _, mode := range []int32{1, 2, 3, 4} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			store := &watchGuardAccountStore{}
			provider := &watchGuardProvider{seen: make(chan struct{}, 8)}
			r := &Resolver{store: store, registry: auth.NewProviderRegistry(nil, provider, nil),
				clock: apiruntime.SystemClock{}, watchRechecks: make(chan struct{}, 1)}
			ctx, stop := context.WithCancel(auth.ContextWithPrincipal(t.Context(), &auth.Principal{
				Subject: datastore.ServiceAccountSubject("controller"), AuthMethod: "serviceaccount-jwt",
				ServiceAccountUID: "00000000-0000-4000-8000-000000000123",
			}))
			defer stop()
			source := make(chan int, 1)
			namespace := "shop"
			upstreamStopped := make(chan struct{})
			out := guardWatch(ctx, r, "Product", &namespace, source, func() { close(upstreamStopped) })
			source <- 1
			select {
			case value := <-out:
				require.Equal(t, 1, value)
			case <-time.After(time.Second):
				t.Fatal("live ServiceAccount watch did not deliver")
			}
			store.mode.Store(mode)
			select {
			case <-upstreamStopped:
			case <-time.After(3 * time.Second):
				t.Fatal("watch outlived its account lifecycle or UID")
			}
			_, ok := <-out
			require.False(t, ok)
		})
	}
}
