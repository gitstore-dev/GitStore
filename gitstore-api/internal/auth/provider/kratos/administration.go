// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

func profileGroups(profile *auth.UserProfile) ([]string, error) {
	var groups []string
	for _, group := range profile.Groups {
		if group == "system:staff" && profile.Kind == auth.ProfileStaff ||
			group == "system:customers" && profile.Kind == auth.ProfileCustomer {
			continue
		}
		group = strings.TrimPrefix(group, "system:group:")
		if strings.HasPrefix(group, "system:") {
			return nil, ErrInvalidInput
		}
		groups = append(groups, group)
	}
	if _, err := ordinaryGroups(groups); err != nil {
		return nil, ErrInvalidInput
	}
	return groups, nil
}

func (p *Provider) UpsertProfile(ctx context.Context, profile *auth.UserProfile) error {
	initiator := auth.PrincipalFromContext(ctx)
	if !auth.ValidTrustedPrincipal(initiator) || profile == nil || !validIdentifier(profile.Username) ||
		(profile.Kind != auth.ProfileStaff && profile.Kind != auth.ProfileCustomer) {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	groups, err := profileGroups(profile)
	if err != nil {
		return err
	}
	actor := mutationActor(initiator)
	state := "active"
	if profile.Lifecycle == auth.ActorInactive || profile.Lifecycle == auth.ActorSuspended {
		state = "inactive"
	}
	var result identity
	if profile.Subject == "" {
		if profile.Metadata != nil && profile.Metadata.Annotations[datastore.OwnerAnnotationKey] != "" {
			return ErrInvalidInput
		}
		record := identity{SchemaID: string(profile.Kind)}
		metadata, err := revisedMetadata(record, nil, actor, true)
		if err != nil {
			return err
		}
		metadata["groups"], metadata["lifecycle_state"] = groups, profile.Lifecycle
		metadata["contact_consent"], metadata["email_deliverable"] = profile.ContactConsent, profile.EmailDeliverable
		metadata["setup_complete"] = false
		if profile.Metadata != nil {
			gitstore := metadata["gitstore"].(map[string]any)
			gitstore["labels"], gitstore["annotations"] = profile.Metadata.Labels, profile.Metadata.Annotations
		}
		traits := map[string]any{"username": profile.Username, "email": profile.Email}
		if profile.DisplayName != "" {
			traits["full_name"] = profile.DisplayName
		}
		err = p.request(ctx, http.MethodPost, "/admin/identities", nil, map[string]any{
			"schema_id": profile.Kind, "state": state,
			"traits":         traits,
			"metadata_admin": metadata,
		}, &result)
		if err != nil {
			return err
		}
		createdID := result.ID
		if !validIdentifier(createdID) {
			return auth.ErrDirectoryUnavailable
		}
		if err := p.request(ctx, http.MethodPatch, "/admin/identities/"+url.PathEscape(createdID), nil,
			[]map[string]any{{"op": "add", "path": "/credentials/password", "value": map[string]any{
				"identifiers": []string{profile.Username, profile.Email}, "config": map[string]any{},
			}}}, &result); err != nil {
			_ = p.request(ctx, http.MethodDelete, "/admin/identities/"+url.PathEscape(createdID), nil, nil, nil)
			p.invalidate()
			return err
		}
	} else {
		current, previous, err := p.fetchIdentity(ctx, profile.Subject)
		if err != nil {
			return err
		}
		if current.SchemaID != string(profile.Kind) || current.Traits.Username != profile.Username {
			return ErrInvalidInput
		}
		resolved, err := p.profile(current)
		if err != nil {
			return err
		}
		lifecycle := profile.Lifecycle
		if lifecycle == resolved.Lifecycle {
			lifecycle = current.MetadataAdmin.Lifecycle
		}
		if profile.Metadata != nil && profile.Metadata.ResourceVersion != "" && profile.Metadata.ResourceVersion != "-1" &&
			current.MetadataAdmin.Metadata != nil && current.MetadataAdmin.Metadata.ResourceVersion != "-1" &&
			profile.Metadata.ResourceVersion != current.MetadataAdmin.Metadata.ResourceVersion {
			return auth.ErrDirectoryConflict
		}
		metadataChanged := false
		if profile.Metadata != nil {
			stored := current.MetadataAdmin.Metadata
			if stored == nil {
				stored = &auth.DirectoryMetadata{}
			}
			if stored.Annotations[datastore.OwnerAnnotationKey] != profile.Metadata.Annotations[datastore.OwnerAnnotationKey] {
				return ErrInvalidInput
			}
			metadataChanged = !reflect.DeepEqual(stored.Labels, profile.Metadata.Labels) ||
				!reflect.DeepEqual(stored.Annotations, profile.Metadata.Annotations)
		}
		consentChanged := current.MetadataAdmin.ContactConsent.Transactional != profile.ContactConsent.Transactional ||
			current.MetadataAdmin.ContactConsent.Marketing != profile.ContactConsent.Marketing
		profile.ContactConsent.ChangedAt = current.MetadataAdmin.ContactConsent.ChangedAt
		if consentChanged {
			profile.ContactConsent.ChangedAt = time.Now().UTC()
		}
		authorChanged := current.Traits.FullName != profile.DisplayName || current.Traits.Email != profile.Email ||
			current.State != state || current.MetadataAdmin.Lifecycle != lifecycle ||
			!slices.Equal(current.MetadataAdmin.Groups, groups) || consentChanged || metadataChanged
		metadata, err := revisedMetadata(current, previous, actor, authorChanged)
		if err != nil {
			return err
		}
		gitstore := metadata["gitstore"].(map[string]any)
		if profile.Metadata != nil {
			gitstore["labels"], gitstore["annotations"] = profile.Metadata.Labels, profile.Metadata.Annotations
		}
		patch := []map[string]any{
			{"op": "add", "path": "/traits/email", "value": profile.Email},
			{"op": "add", "path": "/state", "value": state},
			{"op": "add", "path": "/metadata_admin", "value": metadata},
		}
		if profile.DisplayName != "" {
			patch = append(patch, map[string]any{"op": "add", "path": "/traits/full_name", "value": profile.DisplayName})
		} else if current.Traits.FullName != "" {
			patch = append(patch, map[string]any{"op": "remove", "path": "/traits/full_name"})
		}
		for path, value := range map[string]any{
			"gitstore": gitstore, "update_actor": actor, "groups": groups,
			"lifecycle_state": lifecycle, "contact_consent": profile.ContactConsent,
			"email_deliverable": profile.EmailDeliverable,
			"deliverable_email": resolved.DeliverableEmail,
		} {
			patch = append(patch, map[string]any{"op": "add", "path": "/metadata_admin/" + path, "value": value})
		}
		if err := p.request(ctx, http.MethodPatch, "/admin/identities/"+url.PathEscape(profile.Subject), nil, patch, &result); err != nil {
			return err
		}
	}
	converted, err := p.profile(result)
	if err != nil {
		return err
	}
	*profile = *converted
	p.invalidate()
	return nil
}

func (p *Provider) ResolveContact(ctx context.Context, subject string) (*auth.DirectoryContact, error) {
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	profile, err := p.humanProfile(ctx, subject)
	if err != nil {
		return nil, err
	}
	return &auth.DirectoryContact{Subject: profile.Subject, PrimaryEmail: profile.Email,
		DeliverableEmail: profile.DeliverableEmail, EmailVerified: profile.EmailVerified,
		EmailDeliverable: profile.EmailDeliverable, ContactConsent: profile.ContactConsent}, nil
}

func (p *Provider) Deactivate(ctx context.Context, subject string) error {
	_, err := p.SetHumanLifecycle(ctx, subject, auth.ActorInactive)
	return err
}

func (p *Provider) SetInvitationSender(sender func(context.Context, *auth.UserProfile, string) error) {
	p.invitationMu.Lock()
	defer p.invitationMu.Unlock()
	p.sendInvitation = sender
}

func (p *Provider) invitationSender() func(context.Context, *auth.UserProfile, string) error {
	p.invitationMu.RLock()
	defer p.invitationMu.RUnlock()
	return p.sendInvitation
}

func (p *Provider) deliverInvitation(ctx context.Context, profile *auth.UserProfile, sender func(context.Context, *auth.UserProfile, string) error) error {
	var result struct {
		Link string `json:"recovery_link"`
	}
	if err := p.request(ctx, http.MethodPost, "/admin/recovery/link", nil,
		map[string]any{"identity_id": profile.Subject, "expires_in": "1h"}, &result); err != nil {
		return err
	}
	link, err := url.Parse(result.Link)
	if err != nil || (link.Scheme != "http" && link.Scheme != "https") || link.Hostname() == "" || link.User != nil {
		return auth.ErrDirectoryUnavailable
	}
	if sender(ctx, profile, result.Link) != nil {
		return auth.ErrDirectoryUnavailable
	}
	return nil
}

func (p *Provider) CreateStaff(ctx context.Context, profile *auth.UserProfile) error {
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) || profile == nil ||
		profile.Kind != auth.ProfileStaff || profile.Subject != "" {
		return ErrInvalidInput
	}
	sender := p.invitationSender()
	if sender == nil {
		return auth.ErrNotSupported
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	profile.Lifecycle, profile.Active = auth.ActorInvited, false
	if err := p.UpsertProfile(ctx, profile); err != nil {
		return err
	}
	return p.deliverInvitation(ctx, profile, sender)
}

func (p *Provider) humanProfile(ctx context.Context, subject string) (*auth.UserProfile, error) {
	record, _, err := p.fetchIdentity(ctx, subject)
	if err != nil {
		return nil, err
	}
	if record.SchemaID != string(auth.ProfileStaff) && record.SchemaID != string(auth.ProfileCustomer) {
		return nil, ErrInvalidInput
	}
	return p.profile(record)
}

func (p *Provider) ResendInvitation(ctx context.Context, subject string) (*auth.UserProfile, error) {
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) {
		return nil, ErrInvalidInput
	}
	sender := p.invitationSender()
	if sender == nil {
		return nil, auth.ErrNotSupported
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	profile, err := p.humanProfile(ctx, subject)
	if err != nil {
		return nil, err
	}
	if profile.Kind != auth.ProfileStaff || (profile.Lifecycle != auth.ActorInvited && profile.Lifecycle != auth.ActorPendingVerification) {
		return nil, ErrInvalidInput
	}
	if err := p.UpsertProfile(ctx, profile); err != nil {
		return nil, err
	}
	return profile, p.deliverInvitation(ctx, profile, sender)
}

func (p *Provider) SetHumanLifecycle(ctx context.Context, subject string, state auth.ActorLifecycleState) (*auth.UserProfile, error) {
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) ||
		(state != auth.ActorActive && state != auth.ActorInactive && state != auth.ActorSuspended) {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	profile, err := p.humanProfile(ctx, subject)
	if err != nil {
		return nil, err
	}
	profile.Lifecycle = state
	if err := p.UpsertProfile(ctx, profile); err != nil {
		return nil, err
	}
	if state != auth.ActorActive {
		if err := p.request(ctx, http.MethodDelete, "/admin/identities/"+url.PathEscape(subject)+"/sessions", nil, nil, nil); err != nil && !errors.Is(err, auth.ErrProfileNotFound) {
			return profile, err
		}
	}
	return profile, nil
}

func (p *Provider) DeleteHuman(ctx context.Context, subject string) (*auth.UserProfile, error) {
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	profile, err := p.humanProfile(ctx, subject)
	if err != nil {
		return nil, err
	}
	if err := p.request(ctx, http.MethodDelete, "/admin/identities/"+url.PathEscape(subject), nil, nil, nil); err != nil {
		return nil, err
	}
	p.invalidate()
	return profile, nil
}

func (p *Provider) SetActorGroup(ctx context.Context, kind auth.ProfileKind, uid, group string, assign bool) (*auth.UserProfile, error) {
	if !auth.ValidTrustedPrincipal(auth.PrincipalFromContext(ctx)) || !validIdentifier(uid) ||
		group == "" || strings.TrimSpace(group) != group || strings.HasPrefix(group, "system:") {
		return nil, ErrInvalidInput
	}
	if _, err := ordinaryGroups([]string{group}); err != nil {
		return nil, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	subject := uid
	if kind == auth.ProfileServiceAccount {
		var mirror identity
		if err := p.get(ctx, "/admin/identities/by/external/"+url.PathEscape("gitstore:serviceaccount:"+uid), nil, &mirror); err != nil {
			return nil, err
		}
		subject = mirror.ID
	}
	record, previous, err := p.fetchIdentity(ctx, subject)
	if err != nil {
		return nil, err
	}
	profile, err := p.profile(record)
	if err != nil {
		return nil, err
	}
	if profile.Kind != kind || (kind == auth.ProfileServiceAccount && profile.ServiceAccountUID != uid) {
		return nil, ErrInvalidInput
	}
	if kind == auth.ProfileCustomer {
		if !assign || !slices.Contains(p.config.CustomerGroups, group) {
			return nil, ErrInvalidInput
		}
		return profile, nil
	}
	groups := append([]string{}, record.MetadataAdmin.Groups...)
	if assign && !slices.Contains(groups, group) {
		groups = append(groups, group)
	}
	if !assign {
		groups = slices.DeleteFunc(groups, func(value string) bool { return value == group })
	}
	metadata, err := revisedMetadata(record, previous, mutationActor(auth.PrincipalFromContext(ctx)), !slices.Equal(groups, record.MetadataAdmin.Groups))
	if err != nil {
		return nil, err
	}
	metadata["groups"] = groups
	var result identity
	if err := p.request(ctx, http.MethodPatch, "/admin/identities/"+url.PathEscape(subject), nil, []map[string]any{
		{"op": "add", "path": "/metadata_admin/groups", "value": groups},
		{"op": "add", "path": "/metadata_admin/gitstore", "value": metadata["gitstore"]},
		{"op": "add", "path": "/metadata_admin/update_actor", "value": metadata["update_actor"]},
	}, &result); err != nil {
		return nil, err
	}
	p.invalidate()
	return p.profile(result)
}
