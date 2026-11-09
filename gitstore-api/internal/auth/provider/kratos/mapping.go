// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"errors"
	"net/url"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

func (p *Provider) ResolvePrincipal(ctx context.Context, principal *auth.Principal) (*auth.UserProfile, error) {
	if principal == nil || principal.AuthMethod == "none" {
		return nil, auth.ErrProfileNotFound
	}
	if !auth.ValidTrustedPrincipal(principal) || !validIdentifier(principal.Subject) {
		return nil, ErrInvalidInput
	}
	for _, mapping := range p.config.Mappings {
		if mapping.Provider != principal.AuthMethod || mapping.Issuer != principal.Issuer {
			continue
		}
		key := mapping.Provider + "\x00" + mapping.Issuer + "\x00" + principal.Subject
		var profile *auth.UserProfile
		var err error
		switch mapping.Mode {
		case "uid":
			profile, err = p.lookup(ctx, key, "/admin/identities/"+url.PathEscape(principal.Subject), nil, "")
		case "external_id":
			profile, err = p.lookup(ctx, key, "/admin/identities/by/external/"+url.PathEscape(mapping.Prefix+principal.Subject), nil, "")
		case "serviceaccount_uid":
			if principal.ServiceAccountUID == "" {
				err = auth.ErrProfileNotFound
				break
			}
			profile, err = p.lookup(ctx, key+"\x00"+principal.ServiceAccountUID,
				"/admin/identities/by/external/"+url.PathEscape("gitstore:serviceaccount:"+principal.ServiceAccountUID), nil, "")
			if err == nil && (profile.Kind != auth.ProfileServiceAccount || profile.ServiceAccountUID != principal.ServiceAccountUID) {
				profile, err = nil, auth.ErrProfileNotFound
			}
		case "username":
			if !auth.ValidExternalUsername(principal.Subject) {
				err = auth.ErrProfileNotFound
				break
			}
			profile, err = p.lookup(ctx, key, "/admin/identities",
				url.Values{"credentials_identifier": {principal.Subject}, "page_size": {"2"}}, principal.Subject)
		default:
			return nil, ErrInvalidInput
		}
		if errors.Is(err, auth.ErrProfileNotFound) && principal.ServiceAccountUID == "" {
			return nil, auth.ErrProfileInactive
		}
		return profile, err
	}
	return nil, auth.ErrProfileNotFound
}
