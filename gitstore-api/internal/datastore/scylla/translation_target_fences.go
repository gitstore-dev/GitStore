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

func (s *scyllaDatastore) GetTranslationTargetFence(ctx context.Context, target datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := datastore.TranslationTargetFenceID(target)
	if err != nil {
		return nil, err
	}
	fence := &datastore.TranslationTargetFence{Target: target}
	var name, evidence, intent string
	err = s.session.Session.Query(
		"SELECT target_name,fence_token,phase,evidence,write_intent,target_creation_timestamp FROM translation_target_fences_by_name WHERE namespace_uid=? AND api_version=? AND kind=? AND name=?",
		mustParseUUID(target.NamespaceUID), target.APIVersion, target.Kind, key,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&name, &fence.Token, &fence.Phase, &evidence, &intent, &fence.CreationTimestamp)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if name != target.Name || len(evidence) > 2<<20 || json.Unmarshal([]byte(evidence), &fence.Evidence) != nil ||
		len(intent) > datastore.MaxTranslationTargetIntentBytes ||
		(intent != "" && json.Unmarshal([]byte(intent), &fence.Intent) != nil) || datastore.ValidateTranslationTargetFence(fence) != nil {
		return nil, datastore.ErrRepairRequired
	}
	return fence, nil
}

func (s *scyllaDatastore) BeginTranslationTargetWrite(ctx context.Context, target datastore.TranslationTargetKey, intent datastore.TranslationTargetWriteIntent, expected string) (*datastore.TranslationTargetFence, error) {
	for range 4 {
		current, err := s.GetTranslationTargetFence(ctx, target)
		if err != nil && !errors.Is(err, datastore.ErrNotFound) {
			return nil, err
		}
		next, err := datastore.PlanTranslationTargetWrite(current, target, intent, expected)
		if err != nil {
			return nil, err
		}
		if current != nil && current.Token == next.Token {
			return next, nil
		}
		next, err = s.prepareTranslationTargetPending(ctx, next, expected)
		if err != nil {
			return nil, err
		}
		if err := s.mutations.injector.Inject("reserve-target-head", failureBefore); err != nil {
			return nil, err
		}
		key, _ := datastore.TranslationTargetFenceID(target)
		rawIntent, err := json.Marshal(next.Intent)
		if err != nil {
			return nil, err
		}
		var applied bool
		if current == nil {
			applied, err = s.translationIndexCAS(ctx,
				"INSERT INTO translation_target_fences_by_name (namespace_uid,api_version,kind,name,target_name,fence_token,phase,evidence,write_intent) VALUES (?,?,?,?,?,?,'UPDATING','null',?) IF NOT EXISTS",
				mustParseUUID(target.NamespaceUID), target.APIVersion, target.Kind, key, target.Name, next.Token, string(rawIntent))
		} else {
			applied, err = s.translationIndexCAS(ctx,
				"UPDATE translation_target_fences_by_name SET fence_token=?,phase='UPDATING',write_intent=? WHERE namespace_uid=? AND api_version=? AND kind=? AND name=? IF phase='STABLE' AND fence_token=?",
				next.Token, string(rawIntent), mustParseUUID(target.NamespaceUID), target.APIVersion, target.Kind, key, current.Token)
		}
		if err != nil {
			return nil, err
		}
		if applied {
			return next, nil
		}
	}
	return nil, datastore.ErrConflict
}

func (s *scyllaDatastore) CompleteTranslationTargetWrite(ctx context.Context, target datastore.TranslationTargetKey, token string, evidence *catalog.TranslationTargetEvidence) error {
	current, err := s.GetTranslationTargetFence(ctx, target)
	if err != nil {
		return err
	}
	next, err := datastore.PlanTranslationTargetCompletion(current, token, evidence)
	if err != nil || current.Phase == datastore.TranslationTargetStable {
		return err
	}
	key, _ := datastore.TranslationTargetFenceID(target)
	raw, err := json.Marshal(next.Evidence)
	if err != nil {
		return err
	}
	var createdAt *time.Time
	if !next.CreationTimestamp.IsZero() {
		createdAt = &next.CreationTimestamp
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translation_target_fences_by_name SET phase='STABLE',evidence=?,write_intent=null,target_creation_timestamp=? WHERE namespace_uid=? AND api_version=? AND kind=? AND name=? IF phase='UPDATING' AND fence_token=?",
		string(raw), createdAt, mustParseUUID(target.NamespaceUID), target.APIVersion, target.Kind, key, token)
	if err != nil {
		return err
	}
	if applied {
		return s.closeTranslationTargetPending(ctx, current, current.Intent.Deleting)
	}
	latest, err := s.GetTranslationTargetFence(ctx, target)
	if err != nil {
		return err
	}
	_, err = datastore.PlanTranslationTargetCompletion(latest, token, evidence)
	return err
}

func (s *scyllaDatastore) abortTranslationTargetWrite(ctx context.Context, current *datastore.TranslationTargetFence, evidence *catalog.TranslationTargetEvidence) error {
	next, err := datastore.PlanTranslationTargetAbort(current, current.Token, evidence)
	if err != nil {
		return err
	}
	if err := s.mutations.injector.Inject("publish-target-abort", failureBefore); err != nil {
		return err
	}
	key, _ := datastore.TranslationTargetFenceID(current.Target)
	raw, err := json.Marshal(next.Evidence)
	if err != nil {
		return err
	}
	var createdAt *time.Time
	if !next.CreationTimestamp.IsZero() {
		createdAt = &next.CreationTimestamp
	}
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translation_target_fences_by_name SET phase='STABLE',evidence=?,write_intent=null,target_creation_timestamp=? WHERE namespace_uid=? AND api_version=? AND kind=? AND name=? IF phase='UPDATING' AND fence_token=?",
		string(raw), createdAt, mustParseUUID(current.Target.NamespaceUID), current.Target.APIVersion,
		current.Target.Kind, key, current.Token)
	if err != nil {
		return err
	}
	if applied {
		return s.closeTranslationTargetPending(ctx, current, current.Intent.PreviousUID == "" && evidence == nil)
	}
	latest, err := s.GetTranslationTargetFence(ctx, current.Target)
	if err != nil {
		return err
	}
	_, err = datastore.PlanTranslationTargetAbort(latest, current.Token, evidence)
	return err
}
