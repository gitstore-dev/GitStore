// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type contractDirectory struct {
	profile   *UserProfile
	err       error
	groupsErr error
	calls     int
	lookup    func(context.Context) (*UserProfile, error)
}

func (*contractDirectory) Name() string { return "contract-directory" }
func (d *contractDirectory) GetBySubject(ctx context.Context, _ string) (*UserProfile, error) {
	d.calls++
	if d.lookup != nil {
		return d.lookup(ctx)
	}
	return d.profile, d.err
}
func (d *contractDirectory) ListGroups(context.Context, string) ([]string, error) {
	return []string{"system:group:merchandiser", "system:staff"}, d.groupsErr
}
func (*contractDirectory) SearchUsers(context.Context, string, int) ([]*UserProfile, error) {
	return nil, ErrNotSupported
}
func (*contractDirectory) UpsertProfile(context.Context, *UserProfile) error { return ErrNotSupported }
func (*contractDirectory) Deactivate(context.Context, string) error          { return ErrNotSupported }

type leaseContractDirectory struct{ *contractDirectory }

func (d leaseContractDirectory) ResolvePrincipal(ctx context.Context, principal *Principal) (*UserProfile, error) {
	return d.GetBySubject(ctx, principal.Subject)
}

func (leaseContractDirectory) ListActors(context.Context, ActorPageRequest) (*ActorPage, error) {
	return nil, ErrNotSupported
}

func TestDirectoryEnrichmentRefreshesAnExpiredLookupWithinTheOriginalDeadline(t *testing.T) {
	for _, actor := range []bool{false, true} {
		t.Run(map[bool]string{false: "profile-and-groups", true: "actor-profile"}[actor], func(t *testing.T) {
			directory := &contractDirectory{}
			var firstDeadline time.Time
			expires := time.Now().Add(time.Minute)
			directory.lookup = func(ctx context.Context) (*UserProfile, error) {
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				profile := &UserProfile{Subject: "directory-uid", Username: "canonical-name",
					Kind: ProfileStaff, Lifecycle: ActorActive, Active: true, FreshUntil: expires,
					Groups: []string{"system:group:current"}}
				if directory.calls == 1 {
					firstDeadline = deadline
					profile.FreshUntil = time.Now().Add(-time.Nanosecond)
					profile.Groups = []string{"system:group:retired"}
				} else {
					require.Equal(t, firstDeadline, deadline, "renewal must not reset the two-second directory budget")
				}
				return profile, nil
			}
			var provider UserDirProvider = directory
			if actor {
				provider = leaseContractDirectory{directory}
			}
			original := &Principal{Subject: "authenticated-name", AuthMethod: "static-users",
				Issuer: "gitstore/static-users", Groups: []string{"system:authenticated", "system:group:retired"}}
			enriched, err := EnrichPrincipal(t.Context(), provider, original)
			require.NoError(t, err)
			require.Equal(t, 2, directory.calls)
			require.Equal(t, original.Subject, enriched.Subject)
			require.Equal(t, original.Issuer, enriched.Issuer)
			require.Equal(t, expires, enriched.ProfileExpiresAt, "renewal must use the new source lease without extending it")
			require.NotContains(t, enriched.Groups, "system:group:retired")
			require.Contains(t, enriched.Groups, "system:staff")
			if actor {
				require.Contains(t, enriched.Groups, "system:group:current")
			}
			require.Empty(t, original.Username)
		})
	}
}

func TestDirectoryEnrichmentRenewalStaysBoundedAndFailsClosed(t *testing.T) {
	for _, scenario := range []string{"still-expired", "canceled", "outage", "refresh-outage", "refresh-inactive"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			directory := &contractDirectory{}
			directory.lookup = func(context.Context) (*UserProfile, error) {
				if scenario == "outage" || scenario == "refresh-outage" && directory.calls == 2 {
					return nil, ErrDirectoryUnavailable
				}
				if scenario == "canceled" {
					cancel()
				}
				profile := &UserProfile{Username: "canonical-name", Kind: ProfileStaff, Active: true,
					Lifecycle: ActorActive, FreshUntil: time.Now().Add(-time.Nanosecond)}
				if scenario == "refresh-inactive" && directory.calls == 2 {
					profile.Active, profile.Lifecycle = false, ActorSuspended
					profile.FreshUntil = time.Now().Add(time.Minute)
				}
				return profile, nil
			}
			enriched, err := EnrichPrincipal(ctx, leaseContractDirectory{directory},
				&Principal{Subject: "alice", AuthMethod: "static-users"})
			require.Nil(t, enriched)
			if scenario == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
			} else if scenario == "refresh-inactive" {
				require.ErrorIs(t, err, ErrProfileInactive)
			} else {
				require.ErrorIs(t, err, ErrDirectoryUnavailable)
			}
			expectedCalls := 1
			if scenario == "still-expired" || strings.HasPrefix(scenario, "refresh-") {
				expectedCalls = 2
			}
			require.Equal(t, expectedCalls, directory.calls)
		})
	}
}

func TestDirectoryEnrichmentDistinguishesAbsenceCapabilityAndOutage(t *testing.T) {
	for _, test := range []struct {
		name      string
		lookupErr error
		groupsErr error
		wantErr   error
		wantKind  ProfileKind
		wantGroup bool
	}{
		{"linked", nil, nil, nil, ProfileStaff, true},
		{"unlinked", ErrProfileNotFound, nil, nil, "", false},
		{"no directory capability", ErrNotSupported, nil, nil, "", false},
		{"no group capability", nil, ErrNotSupported, nil, ProfileStaff, false},
		{"lookup outage", ErrDirectoryUnavailable, nil, ErrDirectoryUnavailable, "", false},
		{"group outage", nil, ErrDirectoryUnavailable, ErrDirectoryUnavailable, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := &contractDirectory{
				profile: &UserProfile{Subject: "bob", Username: "canonical-bob", Kind: ProfileStaff, Lifecycle: ActorActive, Active: true},
				err:     test.lookupErr, groupsErr: test.groupsErr,
			}
			original := &Principal{
				Subject: "bob", AuthMethod: "oidc-jwt", Groups: []string{"system:authenticated", "system:customers"},
			}
			principal, err := EnrichPrincipal(context.Background(), directory, original)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
				require.Nil(t, principal)
				return
			}
			require.NoError(t, err)
			require.Equal(t, test.wantKind, principal.ProfileKind)
			require.Contains(t, principal.Groups, "system:authenticated")
			require.NotContains(t, principal.Groups, "system:customers")
			require.Equal(t, test.wantGroup, slices.Contains(principal.Groups, "system:staff"))
			require.Equal(t, []string{"system:authenticated", "system:customers"}, original.Groups)
		})
	}
}

func TestAnonymousDirectoryBypassAndInactiveHumanAdmission(t *testing.T) {
	directory := &contractDirectory{err: ErrDirectoryUnavailable}
	principal, err := EnrichPrincipal(context.Background(), directory, Anonymous())
	require.NoError(t, err)
	require.Equal(t, "anon", principal.Subject)
	require.Zero(t, directory.calls)
	directory.err = nil
	directory.profile = &UserProfile{Username: "bob", Kind: ProfileStaff, Lifecycle: ActorSuspended}
	_, err = EnrichPrincipal(context.Background(), directory, &Principal{Subject: "bob", AuthMethod: "oidc-jwt"})
	require.ErrorIs(t, err, ErrProfileInactive)
}

func TestHumanDirectoryEnrichmentPreservesWinningAuthentication(t *testing.T) {
	for _, method := range []string{"oidc-jwt", "static-users"} {
		t.Run(method, func(t *testing.T) {
			directory := &contractDirectory{profile: &UserProfile{
				Subject: "directory-uid", Username: "directory-name",
				Kind: ProfileStaff, Lifecycle: ActorActive, Active: true,
			}}
			original := &Principal{Subject: "authenticated-name", AuthMethod: method,
				Issuer: "selected-issuer", Claims: map[string]any{"sub": "raw-token-subject"}}
			enriched, err := EnrichPrincipal(t.Context(), directory, original)
			require.NoError(t, err)
			require.Equal(t, original.Subject, enriched.Subject)
			require.Equal(t, original.AuthMethod, enriched.AuthMethod)
			require.Equal(t, original.Issuer, enriched.Issuer)
			require.Equal(t, original.Claims, enriched.Claims)
			require.Equal(t, "directory-name", enriched.Username)
			require.Equal(t, ProfileStaff, enriched.ProfileKind)
			require.Contains(t, enriched.Groups, "system:staff")
			require.Empty(t, original.Username)
		})
	}
}

func TestWorkloadEnrichmentRequiresCurrentUIDAndPreservesAuthority(t *testing.T) {
	for _, scenario := range []string{"linked", "inactive mirror", "old UID", "missing mirror"} {
		t.Run(scenario, func(t *testing.T) {
			directory := &contractDirectory{profile: &UserProfile{
				Subject: "native-mirror-id", Username: "ci-bot", Kind: ProfileServiceAccount,
				ServiceAccountUID: "current-uid", Active: true, Lifecycle: ActorActive,
			}}
			switch scenario {
			case "inactive mirror":
				directory.profile.Active, directory.profile.Lifecycle = false, ActorInactive
			case "old UID":
				directory.profile.ServiceAccountUID = "old-uid"
			case "missing mirror":
				directory.err = ErrProfileNotFound
			}
			original := &Principal{
				Subject: "system:serviceaccount:ci-bot", ServiceAccountUID: "current-uid", AuthMethod: "serviceaccount-jwt",
				Issuer: "gitstore", TokenID: "validated-token", Groups: []string{"system:authenticated", "system:group:old"},
			}
			principal, err := EnrichPrincipal(t.Context(), directory, original)
			require.NoError(t, err)
			require.Equal(t, original.Subject, principal.Subject)
			require.Equal(t, original.ServiceAccountUID, principal.ServiceAccountUID)
			require.Equal(t, original.AuthMethod, principal.AuthMethod)
			require.Equal(t, original.Issuer, principal.Issuer)
			require.Equal(t, original.TokenID, principal.TokenID)
			require.NotContains(t, principal.Groups, "system:group:old")
			require.NotContains(t, principal.Groups, "system:staff")
			linked := scenario == "linked" || scenario == "inactive mirror"
			require.Equal(t, linked, slices.Contains(principal.Groups, "system:group:merchandiser"))
			require.Equal(t, linked, slices.Contains(principal.Groups, "system:serviceaccounts"))
			if !linked {
				require.Empty(t, principal.ProfileKind)
				require.Empty(t, principal.Username)
			}
		})
	}
}

func TestDirectoryMetadataOwnerAndChangeCounters(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	metadata, err := NewDirectoryMetadata(
		&Principal{Subject: "alice", AuthMethod: "oidc-jwt"},
		"directory-uid", "carol", now,
	)
	require.NoError(t, err)
	require.Equal(t, "alice", metadata.OwnerSub)
	require.EqualValues(t, 1, metadata.Generation)
	require.Equal(t, "1", metadata.ResourceVersion)
	require.Equal(t, now, metadata.CreationTimestamp)

	require.NoError(t, metadata.Advance(false))
	require.EqualValues(t, 1, metadata.Generation)
	require.Equal(t, "2", metadata.ResourceVersion)
	require.NoError(t, metadata.Advance(true))
	require.EqualValues(t, 2, metadata.Generation)
	require.Equal(t, "3", metadata.ResourceVersion)
	require.Equal(t, "alice", metadata.OwnerSub)
}

func TestDirectoryMetadataRequiresTrustedInitiator(t *testing.T) {
	_, err := NewDirectoryMetadata(nil, "directory-uid", "carol", time.Now())
	require.Error(t, err)
	anonymousMetadata, err := NewDirectoryMetadata(Anonymous(), "directory-uid", "carol", time.Now())
	require.NoError(t, err)
	require.Equal(t, "anon", anonymousMetadata.OwnerSub)
	metadata, err := NewDirectoryMetadata(&Principal{
		Subject:           "system:serviceaccount:installer",
		ServiceAccountUID: "installer-uid",
		AuthMethod:        "serviceaccount-jwt",
	}, "directory-uid", "carol", time.Now())
	require.NoError(t, err)
	require.Equal(t, "system:serviceaccount:installer", metadata.OwnerSub)
}

func TestDirectoryMetadataCounterOverflowDoesNotPartiallyAdvance(t *testing.T) {
	for _, test := range []struct {
		generation int64
		version    string
	}{
		{math.MaxInt32, "4"},
		{2, "18446744073709551615"},
	} {
		metadata := DirectoryMetadata{Generation: test.generation, ResourceVersion: test.version}
		require.Error(t, metadata.Advance(true))
		require.Equal(t, test.generation, metadata.Generation)
		require.Equal(t, test.version, metadata.ResourceVersion)
	}
}

func TestForwardActorRequestRejectsBackwardPagination(t *testing.T) {
	first, last, before := 10, 10, "cursor"
	require.NoError(t, (ActorPageRequest{First: &first}).ValidateForward())
	for _, request := range []ActorPageRequest{
		{Last: &last},
		{Before: &before},
		{First: &first, Before: &before},
	} {
		require.True(t, errors.Is(request.ValidateForward(), ErrNotSupported))
	}

	for _, size := range []int{0, -1, 501} {
		require.Error(t, (ActorPageRequest{First: &size}).ValidateForward())
	}
}

func TestAuthenticationDoesNotSupplyDirectoryProfileFacts(t *testing.T) {
	principal, ok := normalizeAuthenticatedPrincipal("oidc-jwt", &Principal{
		Subject: "bob", AuthMethod: "oidc-jwt",
		Username: "admin", ProfileKind: ProfileStaff,
	})
	require.True(t, ok)
	require.Empty(t, principal.Username)
	require.Empty(t, principal.ProfileKind)
}
