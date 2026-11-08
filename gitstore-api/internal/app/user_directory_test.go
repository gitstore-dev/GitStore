// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"testing"
	"time"

	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/wsregistry"
	"github.com/stretchr/testify/require"
)

type directoryWebSocketAuth struct{ auth.AuthNProvider }

func (*directoryWebSocketAuth) Name() string                  { return "oidc-jwt" }
func (*directoryWebSocketAuth) Capabilities() auth.Capability { return auth.CapAuthenticate }
func (*directoryWebSocketAuth) Authenticate(context.Context, auth.AuthRequest) (*auth.Principal, auth.Decision, error) {
	return &auth.Principal{Subject: "remote-human", AuthMethod: "oidc-jwt", ExpiresAt: time.Now().Add(time.Hour)},
		auth.Allow("oidc-jwt", "trusted"), nil
}

type webSocketDirectory struct {
	auth.UserDirProvider
	profile  *auth.UserProfile
	err      error
	lease    time.Duration
	onLookup func()
}

func (*webSocketDirectory) Name() string { return "test-directory" }

func (d *webSocketDirectory) GetBySubject(context.Context, string) (*auth.UserProfile, error) {
	if d.onLookup != nil {
		d.onLookup()
	}
	return d.profile, d.err
}

func TestWebSocketAdmissionRejectsConcurrentDirectoryInvalidation(t *testing.T) {
	connections := wsregistry.New()
	directory := &webSocketDirectory{lease: time.Minute, onLookup: connections.Invalidate,
		profile: &auth.UserProfile{Subject: "directory-id", Username: "alice",
			Kind: auth.ProfileStaff, Active: true, Lifecycle: auth.ActorActive}}
	registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}), nil, directory)
	_, _, err := webSocketInitFunc(registry, connections, nil)(t.Context(),
		transport.InitPayload{"Authorization": "Bearer trusted"})
	require.Error(t, err)
	require.Zero(t, connections.Count())
}
func (d *webSocketDirectory) ListGroups(context.Context, string) ([]string, error) {
	return d.profile.Groups, nil
}
func (d *webSocketDirectory) ProfileLeaseDuration() time.Duration { return d.lease }

func TestWebSocketDirectoryAdmissionAndLeaseExpiry(t *testing.T) {
	for _, scenario := range []string{"active", "cached-active", "inactive", "unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			directory := &webSocketDirectory{lease: 50 * time.Millisecond, profile: &auth.UserProfile{
				Subject: "directory-id", Username: "alice", Kind: auth.ProfileStaff, Active: true,
				Lifecycle: auth.ActorActive, Groups: []string{"system:group:merchandiser"},
			}}
			if scenario == "inactive" {
				directory.profile.Active = false
			}
			if scenario == "unavailable" {
				directory.err = auth.ErrDirectoryUnavailable
			}
			if scenario == "cached-active" {
				directory.lease = time.Hour
				directory.profile.FreshUntil = time.Now().Add(50 * time.Millisecond)
			}
			connections := wsregistry.New()
			registry := auth.NewProviderRegistry(auth.NewChainedAuthN(&directoryWebSocketAuth{}), nil, directory)
			ctx, _, err := webSocketInitFunc(registry, connections, nil)(t.Context(),
				transport.InitPayload{"Authorization": "Bearer trusted"})
			if scenario == "inactive" || scenario == "unavailable" {
				require.Error(t, err)
				require.Zero(t, connections.Count())
				return
			}
			require.NoError(t, err)
			t.Cleanup(func() { cancelWebSocketLifetime(ctx); connections.Unregister(ctx) })
			principal := auth.PrincipalFromContext(ctx)
			require.Equal(t, "alice", principal.Username)
			require.Contains(t, principal.Groups, "system:staff")
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.Less(t, time.Until(deadline), 5*time.Minute)
			if scenario == "cached-active" {
				require.False(t, deadline.After(directory.profile.FreshUntil), "a lease must not extend cached freshness")
			}
			require.Eventually(t, func() bool { return ctx.Err() != nil }, time.Second, 10*time.Millisecond)
		})
	}
}
