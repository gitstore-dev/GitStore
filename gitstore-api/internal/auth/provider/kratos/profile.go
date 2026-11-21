// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type identity struct {
	ID         string    `json:"id"`
	ExternalID string    `json:"external_id"`
	SchemaID   string    `json:"schema_id"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	Traits     struct {
		Username   string `json:"username"`
		Email      string `json:"email"`
		FullName   string `json:"full_name"`
		SourceUID  string `json:"source_uid"`
		SourceName string `json:"source_name"`
	} `json:"traits"`
	MetadataAdmin struct {
		CreationActor         string                   `json:"creation_actor"`
		UpdateActor           string                   `json:"update_actor"`
		ServiceAccountSubject string                   `json:"serviceaccount_subject"`
		SourceResourceVersion string                   `json:"source_resource_version"`
		Metadata              *auth.DirectoryMetadata  `json:"gitstore"`
		Groups                []string                 `json:"groups"`
		Lifecycle             auth.ActorLifecycleState `json:"lifecycle_state"`
		ContactConsent        auth.ContactConsent      `json:"contact_consent"`
		EmailDeliverable      *bool                    `json:"email_deliverable"`
		DeliverableEmail      string                   `json:"deliverable_email"`
		SetupComplete         *bool                    `json:"setup_complete"`
	} `json:"metadata_admin"`
	VerifiableAddresses []struct {
		Value    string `json:"value"`
		Verified bool   `json:"verified"`
	} `json:"verifiable_addresses"`
}

func ordinaryGroups(groups []string) ([]string, error) {
	result := make([]string, 0, len(groups))
	seen := make(map[string]bool, len(groups))
	if len(groups) > 128 {
		return nil, auth.ErrDirectoryUnavailable
	}
	for _, group := range groups {
		if strings.HasPrefix(group, "system:") {
			continue
		}
		if group == "" || len(group) > 256 || strings.TrimSpace(group) != group ||
			strings.IndexFunc(group, unicode.IsControl) >= 0 {
			return nil, auth.ErrDirectoryUnavailable
		}
		name := "system:group:" + group
		if !seen[name] {
			result = append(result, name)
			seen[name] = true
		}
	}
	return result, nil
}

func (p *Provider) profile(identity identity) (*auth.UserProfile, error) {
	kind := auth.ProfileKind(identity.SchemaID)
	if kind != auth.ProfileStaff && kind != auth.ProfileCustomer && kind != auth.ProfileServiceAccount {
		return nil, auth.ErrProfileNotFound
	}
	if !validIdentifier(identity.ID) {
		return nil, auth.ErrDirectoryUnavailable
	}
	username := identity.Traits.Username
	sourceUID := ""
	if kind == auth.ProfileServiceAccount {
		username = identity.Traits.SourceName
		sourceUID = identity.Traits.SourceUID
		if !validIdentifier(identity.Traits.SourceUID) || !validIdentifier(username) {
			return nil, auth.ErrDirectoryUnavailable
		}
	} else if !auth.ValidExternalUsername(username) {
		return nil, auth.ErrDirectoryUnavailable
	}
	metadata := identity.MetadataAdmin.Metadata
	if metadata == nil {
		metadata = &auth.DirectoryMetadata{Generation: -1, ResourceVersion: "-1"}
	} else {
		copy := *metadata
		metadata = &copy
	}
	metadata.UID, metadata.Name = identity.ID, username
	if !identity.CreatedAt.IsZero() {
		metadata.CreationTimestamp = identity.CreatedAt
	}
	metadata.CreationActor, metadata.UpdateActor = identity.MetadataAdmin.CreationActor, identity.MetadataAdmin.UpdateActor
	metadata.OwnerSub = metadata.CreationActor
	if owner := metadata.Annotations[datastore.OwnerAnnotationKey]; owner != "" {
		metadata.OwnerSub = owner
	}
	if metadata.OwnerSub == "" {
		metadata.OwnerSub = username
	}
	if kind == auth.ProfileCustomer {
		metadata.Generation, metadata.ResourceVersion = -1, "-1"
	} else if metadata.Generation == -1 {
		if metadata.ResourceVersion != "-1" {
			return nil, auth.ErrDirectoryUnavailable
		}
	} else {
		version, err := strconv.ParseUint(metadata.ResourceVersion, 10, 64)
		if err != nil || version == 0 || metadata.Generation < 1 || metadata.Generation > math.MaxInt32 {
			return nil, auth.ErrDirectoryUnavailable
		}
	}
	verified := false
	for _, address := range identity.VerifiableAddresses {
		if address.Value == identity.Traits.Email && address.Verified {
			verified = true
		}
	}
	deliverableEmail := identity.MetadataAdmin.DeliverableEmail
	if verified {
		deliverableEmail = identity.Traits.Email
	}
	deliverable := false
	if observation := identity.MetadataAdmin.EmailDeliverable; observation != nil &&
		(identity.MetadataAdmin.DeliverableEmail == deliverableEmail || identity.MetadataAdmin.DeliverableEmail == "") {
		deliverable = deliverableEmail != "" && *observation
	} else if verified && identity.MetadataAdmin.DeliverableEmail != "" && identity.MetadataAdmin.DeliverableEmail != deliverableEmail {
		deliverable = true
	}
	lifecycle := identity.MetadataAdmin.Lifecycle
	switch lifecycle {
	case "", auth.ActorActive, auth.ActorInactive, auth.ActorSuspended, auth.ActorInvited, auth.ActorPendingVerification:
	default:
		return nil, auth.ErrDirectoryUnavailable
	}
	if lifecycle == "" {
		lifecycle = auth.ActorActive
		if kind != auth.ProfileServiceAccount && !verified {
			lifecycle = auth.ActorPendingVerification
		}
	}
	if identity.State != "active" && identity.State != "inactive" {
		return nil, auth.ErrDirectoryUnavailable
	}
	if kind != auth.ProfileServiceAccount && identity.MetadataAdmin.SetupComplete != nil &&
		lifecycle != auth.ActorInactive && lifecycle != auth.ActorSuspended {
		if !*identity.MetadataAdmin.SetupComplete {
			lifecycle = auth.ActorPendingVerification
			if kind == auth.ProfileStaff {
				lifecycle = auth.ActorInvited
			}
		} else if *identity.MetadataAdmin.SetupComplete && (lifecycle == auth.ActorInvited || lifecycle == auth.ActorPendingVerification) {
			lifecycle = auth.ActorPendingVerification
			if verified {
				lifecycle = auth.ActorActive
			}
		}
	}
	if kind != auth.ProfileServiceAccount && lifecycle == auth.ActorActive && !verified {
		lifecycle = auth.ActorPendingVerification
	}
	if identity.State == "inactive" && lifecycle != auth.ActorSuspended {
		lifecycle = auth.ActorInactive
	}
	groups, err := ordinaryGroups(identity.MetadataAdmin.Groups)
	if err != nil {
		return nil, err
	}
	switch kind {
	case auth.ProfileStaff:
		groups = append(groups, "system:staff")
	case auth.ProfileCustomer:
		groups, err = ordinaryGroups(p.config.CustomerGroups)
		if err != nil {
			return nil, err
		}
		groups = append(groups, "system:customers")
	case auth.ProfileServiceAccount:
		groups = append(groups, "system:serviceaccounts")
	}
	return &auth.UserProfile{
		Subject: identity.ID, Username: username, DisplayName: identity.Traits.FullName,
		Email: identity.Traits.Email, Groups: groups, Kind: kind, Lifecycle: lifecycle,
		Active: lifecycle == auth.ActorActive && identity.State == "active", Metadata: metadata,
		Identities: []string{identity.ID}, EmailVerified: verified,
		EmailDeliverable: deliverable, DeliverableEmail: deliverableEmail, ContactConsent: identity.MetadataAdmin.ContactConsent,
		ServiceAccountUID: sourceUID,
	}, nil
}
