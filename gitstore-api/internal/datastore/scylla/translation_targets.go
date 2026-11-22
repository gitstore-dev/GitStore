// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

func (s *scyllaDatastore) ReadTranslationTargetNamespaceMetadata(ctx context.Context, name string) (*datastore.TranslationTargetNamespaceMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if name == "" || len(name) > 253 {
		return nil, datastore.ErrInvalidArgument
	}
	var uid gocql.UUID
	err := s.session.Session.Query("SELECT uid FROM namespaces_by_name WHERE name=?", name).
		WithContext(ctx).Consistency(gocql.Serial).Scan(&uid)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if uid == (gocql.UUID{}) {
		return nil, datastore.ErrRepairRequired
	}
	result := &datastore.TranslationTargetNamespaceMetadata{UID: uid.String()}
	var owner datastore.Namespace
	var deletion *time.Time
	var committed *bool
	err = s.session.Session.Query(
		"SELECT name,resource_version,creation_actor,annotations,deletion_timestamp,watch_committed FROM namespaces_by_uid WHERE uid=?", uid,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&result.Name, &result.ResourceVersion, &owner.CreationActor, &owner.Annotations, &deletion, &committed)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrRepairRequired
	}
	if err != nil {
		return nil, err
	}
	if result.Name != name || committed == nil || !*committed {
		return nil, datastore.ErrRepairRequired
	}
	result.OwnerSub, result.Active = owner.EffectiveOwnerSub(), deletion == nil
	return result, nil
}

func (s *scyllaDatastore) ReadTargetEvidence(ctx context.Context, target datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
	return datastore.ReadTranslationTargetEvidence(ctx, target, s.GetTranslationTargetFence, s.ReadTranslationTargetMetadata)
}

func (s *scyllaDatastore) ReadTranslationTargetMetadata(ctx context.Context, fence *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if datastore.ValidateTranslationTargetFence(fence) != nil || fence.Phase != datastore.TranslationTargetStable ||
		fence.Evidence == nil || fence.CreationTimestamp.IsZero() {
		return nil, datastore.ErrInvalidArgument
	}
	evidence := catalog.TranslationTargetEvidence{Namespace: fence.Evidence.Namespace, UID: fence.Evidence.UID}
	var identity *string
	var retired *bool
	var deletion *time.Time
	owner := datastore.Product{}
	err := s.session.Session.Query(
		"SELECT name,api_version,kind,generation,resource_version,revision,translation_source_identity,translation_retired,deletion_timestamp,creation_actor,annotations FROM "+
			authoritativeTable(fence.Target.Kind)+" WHERE namespace=? AND creation_timestamp=? AND uid=?",
		evidence.Namespace, fence.CreationTimestamp, mustParseUUID(evidence.UID),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&evidence.Name, &evidence.APIVersion, &evidence.Kind, &evidence.Generation, &evidence.ResourceVersion,
		&evidence.Revision, &identity, &retired, &deletion, &owner.CreationActor, &owner.Annotations,
	)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var source translationTargetSourceRecord
	if identity == nil || len(*identity) > 512 || json.Unmarshal([]byte(*identity), &source) != nil || source.Invalidated {
		return nil, datastore.ErrRepairRequired
	}
	evidence.ContentDigest = source.ContentDigest
	return &datastore.TranslationTargetMetadata{
		Evidence: evidence, SourceIdentity: source.TranslationTargetSourceIdentity,
		CreationTimestamp: fence.CreationTimestamp, Active: deletion == nil && retired == nil,
		OwnerSub: owner.EffectiveOwnerSub(),
	}, nil
}

var _ datastore.TranslationTargetMetadataStore = (*scyllaDatastore)(nil)
var _ datastore.TranslationTargetFenceStore = (*scyllaDatastore)(nil)
