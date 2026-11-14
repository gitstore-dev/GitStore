// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth

import (
	"context"
	"errors"
	"strings"
	"time"
)

var ErrProfileInactive = errors.New("auth: directory profile is not active")

func directoryGroup(group string) bool {
	return strings.HasPrefix(group, "system:group:") || group == "system:staff" ||
		group == "system:customers" || group == "system:serviceaccounts"
}

// EnrichPrincipal preserves authenticated identity and replaces directory facts.
func EnrichPrincipal(ctx context.Context, directory UserDirProvider, principal *Principal) (*Principal, error) {
	return enrichPrincipal(ctx, directory, principal, false)
}

// EnrichReviewPrincipal requires a complete trusted target profile and groups.
// Authentication may tolerate missing directory support; a review must not
// silently substitute an empty membership set for a failed lookup.
func EnrichReviewPrincipal(ctx context.Context, directory UserDirProvider, principal *Principal) (*Principal, error) {
	return enrichPrincipal(ctx, directory, principal, true)
}

func enrichPrincipal(ctx context.Context, directory UserDirProvider, principal *Principal, requireProfile bool) (*Principal, error) {
	if !ValidTrustedPrincipal(principal) {
		return nil, ErrInvalidToken
	}
	enriched := *principal
	enriched.Username, enriched.ProfileKind = "", ""
	enriched.ProfileExpiresAt = time.Time{}
	enriched.Groups = nil
	for _, group := range principal.Groups {
		if !directoryGroup(group) {
			enriched.Groups = append(enriched.Groups, group)
		}
	}
	if principal.AuthMethod == "none" || directory == nil {
		if requireProfile {
			return nil, ErrDirectoryUnavailable
		}
		return &enriched, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var profile *UserProfile
	var groups []string
	var err error
	groupCapable := true
	if actorDirectory, ok := directory.(ActorDirectory); ok {
		profile, err = actorDirectory.ResolvePrincipal(ctx, principal)
		if profile != nil {
			groups = profile.Groups
		}
	} else {
		profile, err = directory.GetBySubject(ctx, principal.Subject)
		if err == nil && profile != nil {
			groups, err = directory.ListGroups(ctx, profile.Subject)
			if errors.Is(err, ErrNotSupported) {
				if requireProfile {
					return nil, ErrDirectoryUnavailable
				}
				groups, err, groupCapable = nil, nil, false
			}
		}
	}
	if errors.Is(err, ErrProfileNotFound) || errors.Is(err, ErrNotSupported) {
		if requireProfile {
			return nil, ErrDirectoryUnavailable
		}
		return &enriched, nil
	}
	if err != nil {
		return nil, err
	}
	if profile == nil || !ValidExternalUsername(profile.Username) {
		return nil, ErrDirectoryUnavailable
	}
	if !profile.FreshUntil.IsZero() && !profile.FreshUntil.After(time.Now()) {
		return nil, ErrDirectoryUnavailable
	}
	switch profile.Kind {
	case "", ProfileStaff, ProfileCustomer, ProfileServiceAccount:
	default:
		return nil, ErrDirectoryUnavailable
	}
	if profile.Kind != ProfileServiceAccount && (!profile.Active ||
		(profile.Kind != "" && profile.Lifecycle != ActorActive)) {
		return nil, ErrProfileInactive
	}
	if profile.Kind == ProfileServiceAccount && (principal.ServiceAccountUID == "" ||
		profile.ServiceAccountUID != principal.ServiceAccountUID) {
		if requireProfile {
			return nil, ErrDirectoryUnavailable
		}
		return &enriched, nil
	}
	if requireProfile && principal.ServiceAccountUID != "" && profile.Kind != ProfileServiceAccount {
		return nil, ErrDirectoryUnavailable
	}
	enriched.Username, enriched.ProfileKind = profile.Username, profile.Kind
	enriched.ProfileExpiresAt = profile.FreshUntil
	for _, group := range groups {
		if group != "system:authenticated" && group != "system:unauthenticated" &&
			group != "system:staff" && group != "system:customers" && group != "system:serviceaccounts" {
			enriched.Groups = append(enriched.Groups, group)
		}
	}
	if groupCapable {
		switch profile.Kind {
		case ProfileStaff:
			enriched.Groups = append(enriched.Groups, "system:staff")
		case ProfileCustomer:
			enriched.Groups = append(enriched.Groups, "system:customers")
		case ProfileServiceAccount:
			enriched.Groups = append(enriched.Groups, "system:serviceaccounts")
		}
	}
	if !ValidTrustedPrincipal(&enriched) {
		return nil, ErrDirectoryUnavailable
	}
	return &enriched, nil
}
