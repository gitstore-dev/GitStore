// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

func (p *Provider) fetchIdentity(ctx context.Context, id string) (identity, map[string]any, error) {
	var raw map[string]any
	var record identity
	if !validIdentifier(id) {
		return record, nil, ErrInvalidInput
	}
	if err := p.get(ctx, "/admin/identities/"+url.PathEscape(id), nil, &raw); err != nil {
		return record, nil, err
	}
	body, err := json.Marshal(raw)
	if err != nil || json.Unmarshal(body, &record) != nil || record.ID != id {
		return record, nil, auth.ErrDirectoryUnavailable
	}
	metadata, _ := raw["metadata_admin"].(map[string]any)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	return record, maps.Clone(metadata), nil
}

func revisedMetadata(record identity, previous map[string]any, actor string, authorChanged bool) (map[string]any, error) {
	result := maps.Clone(previous)
	if result == nil {
		result = make(map[string]any)
	}
	metadata := &auth.DirectoryMetadata{Generation: 1, ResourceVersion: "1"}
	if record.SchemaID != string(auth.ProfileCustomer) && record.MetadataAdmin.Metadata != nil &&
		record.MetadataAdmin.Metadata.Generation > 0 {
		copy := *record.MetadataAdmin.Metadata
		metadata = &copy
		if err := metadata.Advance(authorChanged); err != nil {
			return nil, ErrInvalidInput
		}
	}
	if record.SchemaID == string(auth.ProfileCustomer) {
		metadata.Generation, metadata.ResourceVersion = -1, "-1"
	}
	// Preserve provider metadata that this operation does not own.
	gitstore, _ := previous["gitstore"].(map[string]any)
	gitstore = maps.Clone(gitstore)
	if gitstore == nil {
		gitstore = make(map[string]any)
	}
	gitstore["generation"], gitstore["resource_version"] = metadata.Generation, metadata.ResourceVersion
	result["gitstore"] = gitstore
	if record.ID == "" && (result["creation_actor"] == nil || result["creation_actor"] == "") {
		result["creation_actor"] = actor
	}
	result["update_actor"] = actor
	return result, nil
}

func mutationActor(principal *auth.Principal) string {
	if principal.Username != "" && !strings.HasPrefix(principal.Subject, "system:") {
		return principal.Username
	}
	return principal.Subject
}
