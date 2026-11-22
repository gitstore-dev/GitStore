// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

type deletedTranslationUID struct {
	UID, NamespaceUID, ResourceVersion, SourceVersion string
	CreationTimestamp                                 time.Time
	RepairWatermark                                   string
}

func (m *memdbDatastore) TranslationStore() datastore.TranslationStore           { return m }
func (m *memdbDatastore) TranslationIndexStore() datastore.TranslationIndexStore { return m }

func (m *memdbDatastore) CreateTranslation(_ context.Context, source *datastore.Translation) error {
	if err := datastore.ValidateTranslationSource(source); err != nil {
		return err
	}
	if source.Generation != 1 || source.ResourceVersion != "1" || len(source.Status) != 0 {
		return datastore.ErrInvalidArgument
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	txn := m.db.Txn(true)
	defer txn.Abort()
	if err := translationParentsActive(txn, source); err != nil {
		return err
	}
	if raw, err := txn.First("deleted_translation_uid", "id", source.UID); err != nil || raw != nil {
		if err != nil {
			return err
		}
		return datastore.ErrAlreadyExists
	}
	if raw, err := txn.First("translation", "id", source.UID); err != nil || raw != nil {
		if err != nil {
			return err
		}
		return datastore.ErrAlreadyExists
	}
	if raw, err := txn.First("translation", "name_namespace", source.Namespace, source.Name); err != nil || raw != nil {
		if err != nil {
			return err
		}
		return datastore.ErrAlreadyExists
	}
	stored := cloneTranslation(source)
	return m.commitTranslation(txn, stored, datastore.ResourceWatchAdded, nil)
}

func translationParentsActive(txn *gomemdb.Txn, source *datastore.Translation) error {
	if err := catalogParentsActive(txn, source.Namespace, source.RepositoryID); err != nil {
		return err
	}
	raw, err := txn.First("namespaces", "name", source.Namespace)
	if err != nil {
		return err
	}
	if raw == nil {
		return datastore.ErrNotFound
	}
	if raw.(*datastore.Namespace).UID != source.NamespaceID {
		return datastore.ErrConflict
	}
	return nil
}

func (m *memdbDatastore) GetTranslation(_ context.Context, uid string) (*datastore.Translation, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneTranslation(raw.(*datastore.Translation)), nil
}

func (m *memdbDatastore) GetTranslationByName(_ context.Context, namespace, name string) (*datastore.Translation, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("translation", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	return cloneTranslation(raw.(*datastore.Translation)), nil
}

func (m *memdbDatastore) UpdateTranslation(_ context.Context, source *datastore.Translation, expected string) error {
	if err := datastore.ValidateTranslationSource(source); err != nil {
		return err
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", source.UID)
	if err != nil || raw == nil {
		return notFoundOrErr(err)
	}
	current := raw.(*datastore.Translation)
	if err := datastore.ValidateCatalogUpdateVersion(current.ResourceVersion, expected, source.ResourceVersion); err != nil {
		return err
	}
	if source.Namespace != current.Namespace || source.NamespaceID != current.NamespaceID || source.Name != current.Name ||
		source.RepositoryID != current.RepositoryID || source.CreationActor != current.CreationActor ||
		!source.CreationTimestamp.Equal(current.CreationTimestamp) {
		return datastore.ErrConflict
	}
	generation := current.Generation
	if !bytes.Equal(source.Spec, current.Spec) || source.Body != current.Body {
		generation++
	}
	if source.Generation != generation {
		return datastore.ErrConflict
	}
	if err := translationParentsActive(txn, source); err != nil {
		return err
	}
	stored := cloneTranslation(source)
	stored.Status = append(json.RawMessage(nil), current.Status...)
	return m.commitTranslation(txn, stored, datastore.ResourceWatchModified, current.Labels)
}

func (m *memdbDatastore) UpdateTranslationStatus(_ context.Context, uid, expected string, status json.RawMessage) (*datastore.Translation, error) {
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", uid)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	current := raw.(*datastore.Translation)
	stored := cloneTranslation(current)
	if err := datastore.ApplyTranslationStatus(stored, expected, status); err != nil {
		return nil, err
	}
	if err := m.commitTranslation(txn, stored, datastore.ResourceWatchModified, current.Labels); err != nil {
		return nil, err
	}
	return cloneTranslation(stored), nil
}

func (m *memdbDatastore) DeleteTranslation(_ context.Context, uid, expected string) error {
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", uid)
	if err != nil || raw == nil {
		return notFoundOrErr(err)
	}
	source := raw.(*datastore.Translation)
	if source.ResourceVersion != expected || len(source.Finalizers) > 0 {
		return datastore.ErrConflict
	}
	return m.commitTranslation(txn, source, datastore.ResourceWatchDeleted, source.Labels)
}

func (m *memdbDatastore) RequestTranslationRevalidation(ctx context.Context, source *datastore.Translation) (bool, error) {
	if err := datastore.ValidateTranslationRevalidationRequest(source); err != nil {
		return false, err
	}
	m.translationMutationMu.Lock()
	defer m.translationMutationMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	txn := m.db.Txn(true)
	defer txn.Abort()
	raw, err := txn.First("translation", "id", source.UID)
	if err != nil {
		return false, err
	}
	if raw == nil {
		return false, datastore.ErrConflict
	}
	current := raw.(*datastore.Translation)
	if current.NamespaceID != source.NamespaceID || !current.CreationTimestamp.Equal(source.CreationTimestamp) ||
		current.ResourceVersion != source.ResourceVersion {
		return false, datastore.ErrConflict
	}
	raw, err = txn.First("translation_source_placements", "id", source.UID)
	if err != nil {
		return false, err
	}
	if raw == nil {
		return false, datastore.ErrRepairRequired
	}
	previous := raw.(*translationSourcePlacements)
	if previous.RevalidationVersion == source.ResourceVersion {
		return false, nil
	}
	next := *previous
	next.RevalidationVersion = source.ResourceVersion
	if err := txn.Insert("translation_source_placements", &next); err != nil {
		return false, err
	}
	payload, err := json.Marshal(current)
	if err != nil {
		return false, err
	}
	txn.Commit()
	m.recordCommittedTranslation(datastore.ResourceWatchModified, current, current.Labels, payload)
	return true, nil
}

// The mutation mutex is held through commit and publication, preserving the
// authoritative order across source, status and deletion writers.
func (m *memdbDatastore) commitTranslation(txn *gomemdb.Txn, source *datastore.Translation, eventType datastore.ResourceWatchEventType, previousLabels map[string]string) error {
	payload, err := json.Marshal(source)
	if err != nil {
		return err
	}
	if err := syncTranslationBinding(txn, source, eventType); err != nil {
		return err
	}
	var binding *datastore.TranslationBinding
	if eventType != datastore.ResourceWatchDeleted {
		raw, err := txn.First("translation_binding", "id", source.UID)
		if err != nil {
			return err
		}
		if raw == nil {
			return datastore.ErrRepairRequired
		}
		binding = raw.(*datastore.TranslationBinding)
	}
	if err := syncTranslationSourceIndexes(txn, source, binding, eventType); err != nil {
		return err
	}
	if eventType == datastore.ResourceWatchDeleted {
		if err := txn.Delete("translation", source); err != nil {
			return err
		}
		if err := deleteOwnerReferenceProjections(txn, "Translation", source.UID); err != nil {
			return err
		}
	} else {
		if err := txn.Insert("translation", source); err != nil {
			return err
		}
		if err := syncOwnerReferenceProjections(txn, source.Namespace, source.RepositoryID, "Translation", source.UID, source.Name, source.ResourceVersion, source.OwnerReferences); err != nil {
			return err
		}
	}
	txn.Commit()
	m.recordCommittedTranslation(eventType, source, previousLabels, payload)
	return nil
}

func (m *memdbDatastore) ListTranslations(ctx context.Context, namespace string, page datastore.PageParams) (*datastore.TranslationPage, error) {
	page, err := page.BoundedTranslationPage()
	if err != nil || namespace == "" {
		return nil, datastore.ErrInvalidArgument
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	raw, err := txn.First("namespaces", "name", namespace)
	if err != nil || raw == nil {
		return nil, notFoundOrErr(err)
	}
	ns := raw.(*datastore.Namespace)
	cursor := page.After
	if page.Last > 0 {
		cursor = page.Before
	}
	indexed, err := datastore.ReadTranslationNamespacePage(ctx, ns.UID, cursor, page.Last > 0, page.Limit(),
		func(_ context.Context, index datastore.TranslationIndexKey) (*datastore.TranslationIndexHead, error) {
			raw, err := txn.First("translation_index_head", "id", translationHeadID(index))
			if err != nil || raw == nil {
				return nil, err
			}
			head := raw.(*translationIndexHeadRow).Head
			return &head, nil
		},
		func(ctx context.Context, index datastore.TranslationIndexKey, position *datastore.PageCursor, backward bool, limit int) ([]datastore.TranslationIndexCell, error) {
			return readTranslationSourceSegment(ctx, txn, index, position, backward, limit)
		})
	if err != nil {
		return nil, err
	}
	return datastore.ResolveTranslationNamespacePage(ctx, indexed, page, func(_ context.Context, entry datastore.TranslationIndexEntry) (*datastore.Translation, error) {
		raw, err := txn.First("translation", "id", entry.TranslationUID)
		if err != nil || raw == nil {
			return nil, err
		}
		source := raw.(*datastore.Translation)
		if source.NamespaceID != ns.UID || source.Namespace != namespace ||
			!source.CreationTimestamp.UTC().Truncate(time.Millisecond).Equal(entry.CreationTimestamp) {
			return nil, datastore.ErrRepairRequired
		}
		return cloneTranslation(source), nil
	})
}

func cloneTranslation(source *datastore.Translation) *datastore.Translation {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Labels = cloneStringMap(source.Labels)
	clone.Annotations = cloneStringMap(source.Annotations)
	clone.OwnerReferences = append(json.RawMessage(nil), source.OwnerReferences...)
	clone.Finalizers = append([]string(nil), source.Finalizers...)
	clone.DeletionTimestamp = cloneTimePointer(source.DeletionTimestamp)
	clone.RawSource = append([]byte(nil), source.RawSource...)
	clone.Spec = append(json.RawMessage(nil), source.Spec...)
	clone.Status = append(json.RawMessage(nil), source.Status...)
	return &clone
}

func (m *memdbDatastore) recordCommittedTranslation(eventType datastore.ResourceWatchEventType, source *datastore.Translation, previousLabels map[string]string, payload json.RawMessage) {
	now := time.Now().UTC()
	m.namespaceWatchMu.Lock()
	defer m.namespaceWatchMu.Unlock()
	m.pruneLocked(now.Add(-m.namespaceWatchRetention))
	m.namespaceWatchSequence++
	m.namespaceWatchEvents = append(m.namespaceWatchEvents, datastore.ResourceWatchEvent{
		Epoch: m.namespaceWatchEpoch, Sequence: m.namespaceWatchSequence,
		Type: eventType, Kind: "Translation", Namespace: source.Namespace, Name: source.Name, Payload: payload,
		SelectorLabels: cloneStringMap(source.Labels), PreviousSelectorLabels: cloneStringMap(previousLabels),
		DeduplicationKey: fmt.Sprintf("memdb:%s:%s:%d", eventType, source.UID, m.namespaceWatchSequence),
		At:               now,
	})
}
