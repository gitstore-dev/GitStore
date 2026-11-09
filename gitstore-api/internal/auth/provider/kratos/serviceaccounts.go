// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

type ServiceAccountSource interface {
	GetServiceAccountByUID(context.Context, string) (*datastore.ServiceAccount, error)
}

func (p *Provider) ApplyServiceAccountEvent(ctx context.Context, source ServiceAccountSource, event datastore.ResourceWatchEvent) error {
	if event.Kind != "ServiceAccount" {
		return nil
	}
	if source == nil || event.Namespace != "" || (event.Type != datastore.ResourceWatchAdded &&
		event.Type != datastore.ResourceWatchModified && event.Type != datastore.ResourceWatchDeleted) ||
		int64(len(event.Payload)) > p.config.MaxResponseBytes {
		return ErrInvalidInput
	}
	var observed struct{ UID string }
	if json.Unmarshal(event.Payload, &observed) != nil || !validIdentifier(observed.UID) {
		return ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	current, err := source.GetServiceAccountByUID(ctx, observed.UID)
	if err != nil && !errors.Is(err, datastore.ErrNotFound) {
		return err
	}
	if err == nil && (current == nil || current.UID != observed.UID || !validIdentifier(current.Name) || current.ResourceVersion == "") {
		return auth.ErrDirectoryUnavailable
	}
	externalID := "gitstore:serviceaccount:" + observed.UID
	var mirror identity
	lookupErr := p.get(ctx, "/admin/identities/by/external/"+url.PathEscape(externalID), nil, &mirror)
	if lookupErr != nil && !errors.Is(lookupErr, auth.ErrProfileNotFound) {
		return lookupErr
	}
	if lookupErr == nil && (mirror.SchemaID != string(auth.ProfileServiceAccount) ||
		mirror.Traits.SourceUID != observed.UID || mirror.ExternalID != externalID || !validIdentifier(mirror.ID)) {
		return ErrInvalidInput
	}
	if errors.Is(err, datastore.ErrNotFound) {
		if errors.Is(lookupErr, auth.ErrProfileNotFound) {
			return nil
		}
		if err := p.request(ctx, http.MethodDelete, "/admin/identities/"+url.PathEscape(mirror.ID), nil, nil, nil); err != nil &&
			!errors.Is(err, auth.ErrProfileNotFound) {
			return err
		}
		p.invalidate()
		return nil
	}
	state := "active"
	if current.Disabled || current.DeletionTimestamp != nil {
		state = "inactive"
	}
	subject := "system:serviceaccount:" + current.Name
	traits := map[string]string{"source_uid": current.UID, "source_name": current.Name}
	changed := false
	if errors.Is(lookupErr, auth.ErrProfileNotFound) {
		changed = true
		err = p.request(ctx, http.MethodPost, "/admin/identities", nil, map[string]any{
			"schema_id": string(auth.ProfileServiceAccount), "external_id": externalID, "state": state, "traits": traits,
			"metadata_admin": map[string]any{
				"groups": []string{}, "creation_actor": current.CreationActor, "update_actor": "system:gitstore",
				"serviceaccount_subject": subject, "source_resource_version": current.ResourceVersion,
			},
		}, nil)
	} else if mirror.State != state || mirror.Traits.SourceName != current.Name ||
		mirror.MetadataAdmin.SourceResourceVersion != current.ResourceVersion || mirror.MetadataAdmin.ServiceAccountSubject != subject {
		changed = true
		err = p.request(ctx, http.MethodPatch, "/admin/identities/"+url.PathEscape(mirror.ID), nil, []map[string]any{
			{"op": "add", "path": "/state", "value": state},
			{"op": "add", "path": "/traits", "value": traits},
			{"op": "add", "path": "/metadata_admin/serviceaccount_subject", "value": subject},
			{"op": "add", "path": "/metadata_admin/source_resource_version", "value": current.ResourceVersion},
			{"op": "add", "path": "/metadata_admin/update_actor", "value": "system:gitstore"},
		}, nil)
	}
	if err != nil {
		return err
	}
	if changed {
		p.invalidate()
	}
	// Native Admin PATCH has no source CAS. A change during I/O must retry
	// against authority; finite retry exhaustion can still leave a stale mirror.
	latest, err := source.GetServiceAccountByUID(ctx, current.UID)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return auth.ErrDirectoryConflict
		}
		return err
	}
	if latest == nil || latest.UID != current.UID || latest.ResourceVersion != current.ResourceVersion {
		return auth.ErrDirectoryConflict
	}
	return nil
}
