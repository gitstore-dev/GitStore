// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/google/uuid"
)

type translationIdentityRow struct {
	NamespaceUID, UID, ExpectedNameToken gocql.UUID
	Namespace, Name                      string
	CreatedAt                            time.Time
	Retired                              bool
}

func translationCreationToken(uid, purpose string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("Translation/"+uid+"/"+purpose)).String()
}

func translationNameKey(name string) string {
	digest := sha256.Sum256([]byte(name))
	return hex.EncodeToString(digest[:])
}

func (s *scyllaDatastore) readTranslationIdentity(ctx context.Context, uid string) (*translationIdentityRow, error) {
	parsed, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, datastore.ErrInvalidArgument
	}
	row := &translationIdentityRow{UID: parsed}
	err = s.session.Session.Query(
		"SELECT namespace_uid,namespace,name,creation_timestamp,retired,expected_name_token FROM translations_by_uid WHERE uid=?", parsed,
	).WithContext(ctx).Consistency(gocql.Serial).Scan(
		&row.NamespaceUID, &row.Namespace, &row.Name, &row.CreatedAt, &row.Retired, &row.ExpectedNameToken)
	if errors.Is(err, gocql.ErrNotFound) {
		return nil, datastore.ErrNotFound
	}
	return row, err
}

func (s *scyllaDatastore) translationNameOwner(ctx context.Context, namespace gocql.UUID, name string) (gocql.UUID, gocql.UUID, string, error) {
	var uid, token gocql.UUID
	var phase string
	err := s.session.Session.Query(
		"SELECT uid,reservation_token,phase FROM translations_by_name WHERE namespace_uid=? AND name=?", namespace, translationNameKey(name),
	).WithContext(ctx).Consistency(gocql.Serial).Scan(&uid, &token, &phase)
	if errors.Is(err, gocql.ErrNotFound) {
		return uid, token, "", nil
	}
	if err == nil && (token == (gocql.UUID{}) || (phase != "Reserved" && phase != "Vacant")) {
		err = datastore.ErrRepairRequired
	}
	return uid, token, phase, err
}

func (s *scyllaDatastore) prepareTranslationIdentity(ctx context.Context, source *datastore.Translation) (*translationIdentityRow, error) {
	existing, err := s.readTranslationIdentity(ctx, source.UID)
	if errors.Is(err, datastore.ErrNotFound) {
		namespace := mustParseUUID(source.NamespaceID)
		owner, expected, phase, nameErr := s.translationNameOwner(ctx, namespace, source.Name)
		if nameErr != nil {
			return nil, nameErr
		}
		if phase == "Reserved" {
			if owner.String() == source.UID {
				return nil, datastore.ErrRepairRequired
			}
			return nil, datastore.ErrAlreadyExists
		}
		var expectedValue any
		if phase == "Vacant" {
			expectedValue = expected
		}
		_, err = s.translationIndexCAS(ctx,
			"INSERT INTO translations_by_uid (uid,namespace_uid,namespace,name,creation_timestamp,reservation_token,retired,expected_name_token) VALUES (?,?,?,?,?,?,false,?) IF NOT EXISTS",
			mustParseUUID(source.UID), namespace, source.Namespace, source.Name, source.CreationTimestamp,
			mustParseUUID(translationCreationToken(source.UID, "namespace")), expectedValue)
		if err != nil {
			return nil, err
		}
		existing, err = s.readTranslationIdentity(ctx, source.UID)
	}
	if err != nil {
		return nil, err
	}
	if existing.Retired || existing.NamespaceUID.String() != source.NamespaceID ||
		existing.Namespace != source.Namespace || existing.Name != source.Name ||
		!existing.CreatedAt.Equal(source.CreationTimestamp) {
		return nil, datastore.ErrAlreadyExists
	}
	return existing, nil
}

func (s *scyllaDatastore) CreateTranslation(ctx context.Context, candidate *datastore.Translation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := datastore.ValidateTranslationSource(candidate); err != nil {
		return err
	}
	if candidate.ResourceVersion != "1" || candidate.Generation != 1 || len(candidate.Status) != 0 {
		return datastore.ErrInvalidArgument
	}
	if _, err := datastore.TranslationIndexShard(candidate.UID); err != nil {
		return err
	}
	return s.withNamespaceCatalogFence(ctx, candidate.Namespace, false, func() error {
		if err := s.catalogParentsActive(ctx, candidate.Namespace, candidate.RepositoryID); err != nil {
			return err
		}
		namespace, err := s.GetNamespaceByName(ctx, candidate.Namespace)
		if err != nil {
			return err
		}
		if namespace.UID != candidate.NamespaceID {
			return datastore.ErrConflict
		}
		source := *candidate
		source.CreationTimestamp = source.CreationTimestamp.UTC().Truncate(time.Millisecond)
		binding, err := datastore.PrepareTranslationBinding(&source, nil)
		if err != nil {
			return err
		}
		binding.CurrentEpoch = translationCreationToken(source.UID, "reference")
		binding.Epochs[0].Epoch = binding.CurrentEpoch
		identity, err := s.prepareTranslationIdentity(ctx, &source)
		if err != nil {
			return err
		}
		current, err := s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
		if err == nil {
			if current.Phase == "Creating" {
				if err := s.completeTranslationCreation(ctx, current, identity); err != nil {
					return err
				}
			}
			return datastore.ErrAlreadyExists
		}
		if !errors.Is(err, datastore.ErrNotFound) {
			return err
		}
		anchor, err := s.reserveTranslationCreationIndex(ctx, &source, binding, false)
		if err != nil {
			return err
		}
		if err := s.CommitTranslationIndex(ctx, anchor); err != nil {
			return err
		}
		row := toTranslationSourceRow(&source)
		row.Phase, row.CoordinationVersion, row.ReferenceEpoch = "Creating", "1", binding.CurrentEpoch
		raw, err := json.Marshal(binding)
		if err != nil {
			return err
		}
		row.BindingSnapshot = string(raw)
		applied, err := s.insertTranslationSourceRow(ctx, row)
		if err != nil {
			return err
		}
		if !applied {
			row, err = s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
			if err != nil {
				return err
			}
		}
		if err := s.completeTranslationCreation(ctx, row, identity); err != nil {
			return err
		}
		if !applied {
			return datastore.ErrAlreadyExists
		}
		return nil
	})
}

func (s *scyllaDatastore) reserveTranslationCreationIndex(ctx context.Context, source *datastore.Translation, binding *datastore.TranslationBinding, named bool) (datastore.TranslationIndexReservation, error) {
	shard, err := datastore.TranslationIndexShard(source.UID)
	if err != nil {
		return datastore.TranslationIndexReservation{}, err
	}
	index := datastore.TranslationIndexKey{Kind: datastore.TranslationNamespaceIndex, NamespaceUID: source.NamespaceID, Shard: shard}
	purpose := "namespace"
	if named {
		index.Kind, index.Target, purpose = datastore.TranslationTargetNameIndex, binding.Epochs[0].Target, "target-name"
	}
	entry := datastore.TranslationIndexEntry{TranslationUID: source.UID, SourceVersion: source.ResourceVersion,
		ReferenceEpoch: binding.CurrentEpoch, CreationTimestamp: source.CreationTimestamp}
	return s.ReserveTranslationIndex(ctx, index, entry, translationCreationToken(source.UID, purpose),
		max(1024, datastore.TranslationIndexPayloadBytes(index, entry)))
}

func (s *scyllaDatastore) completeTranslationCreation(ctx context.Context, row *translationSourceRow, identity *translationIdentityRow) error {
	if row.Phase == "Stable" {
		return nil
	}
	if row.Phase != "Creating" || row.CoordinationVersion != "1" {
		return datastore.ErrRepairRequired
	}
	binding, err := translationRowBinding(row)
	if err != nil {
		return err
	}
	claim := mustParseUUID(translationCreationToken(row.UID.String(), "claim"))
	applied := false
	if identity.ExpectedNameToken == (gocql.UUID{}) {
		applied, err = s.translationIndexCAS(ctx,
			"INSERT INTO translations_by_name (namespace_uid,name,uid,creation_timestamp,reservation_token,phase) VALUES (?,?,?,?,?,'Reserved') IF NOT EXISTS",
			row.NamespaceUID, translationNameKey(row.Name), row.UID, row.CreationTimestamp, claim)
	} else {
		applied, err = s.translationIndexCAS(ctx,
			"UPDATE translations_by_name SET uid=?,creation_timestamp=?,reservation_token=?,phase='Reserved' WHERE namespace_uid=? AND name=? IF phase='Vacant' AND reservation_token=?",
			row.UID, row.CreationTimestamp, claim, row.NamespaceUID, translationNameKey(row.Name), identity.ExpectedNameToken)
	}
	if err != nil {
		return err
	}
	if !applied {
		owner, token, phase, err := s.translationNameOwner(ctx, row.NamespaceUID, row.Name)
		if err != nil {
			return err
		}
		if owner != row.UID || token != claim || phase != "Reserved" {
			return s.retireUnclaimedTranslation(ctx, row)
		}
	}
	source := row.source()
	anchor, err := s.reserveTranslationCreationIndex(ctx, source, binding, false)
	if err != nil {
		return err
	}
	named, err := s.reserveTranslationCreationIndex(ctx, source, binding, true)
	if err != nil {
		return err
	}
	if err := s.CommitTranslationIndex(ctx, named); err != nil {
		return err
	}
	placements, err := json.Marshal([]datastore.TranslationIndexReservation{anchor, named})
	if err != nil {
		return err
	}
	if err := s.syncTranslationSourceProjections(ctx, row.CoordinationVersion, nil, source); err != nil {
		return err
	}
	applied, err = s.translationIndexCAS(ctx,
		"UPDATE translations_by_key SET phase='Stable',coordination_version='2',maintenance_intent=? WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Creating' AND coordination_version='1'",
		string(placements), row.NamespaceUID, row.CreationTimestamp, row.UID)
	if err != nil {
		return err
	}
	if !applied {
		current, err := s.readTranslationSourceRow(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
		if err != nil {
			return err
		}
		if current.Phase != "Stable" {
			return datastore.ErrConflict
		}
	}
	return nil
}

func (s *scyllaDatastore) retireUnclaimedTranslation(ctx context.Context, row *translationSourceRow) error {
	applied, err := s.translationIndexCAS(ctx,
		"UPDATE translations_by_key SET phase='Retired',coordination_version='2' WHERE namespace_uid=? AND creation_timestamp=? AND uid=? IF phase='Creating' AND coordination_version='1'",
		row.NamespaceUID, row.CreationTimestamp, row.UID)
	if err != nil {
		return err
	}
	if !applied {
		row, err = s.readTranslationSourceRow(ctx, row.NamespaceUID, row.CreationTimestamp, row.UID)
		if err != nil {
			return err
		}
		if row.Phase != "Retired" {
			return datastore.ErrConflict
		}
	} else {
		pending := *row
		pending.Phase, pending.CoordinationVersion = "Retired", "2"
		row = &pending
	}
	err = s.finishRetiredTranslation(ctx, row)
	if err != nil {
		return err
	}
	return datastore.ErrAlreadyExists
}

func (s *scyllaDatastore) RecoverTranslationCreation(ctx context.Context, uid string) error {
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return err
	}
	if identity.Retired {
		return datastore.ErrNotFound
	}
	row, err := s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
	if err != nil {
		return err
	}
	return s.withNamespaceCatalogFence(ctx, row.Namespace, false, func() error {
		if err := s.catalogParentsActive(ctx, row.Namespace, uuidString(row.RepositoryID)); err != nil {
			return err
		}
		return s.completeTranslationCreation(ctx, row, identity)
	})
}

func (s *scyllaDatastore) fenceUnpublishedTranslation(ctx context.Context, identity *translationIdentityRow) (*translationSourceRow, error) {
	row := &translationSourceRow{
		productRow:   productRow{UID: identity.UID, CreationTimestamp: identity.CreatedAt, ResourceVersion: "1"},
		NamespaceUID: identity.NamespaceUID, Phase: "Retired", CoordinationVersion: "1",
	}
	applied, err := s.insertTranslationSourceRow(ctx, row)
	if err != nil {
		return nil, err
	}
	if applied {
		return row, nil
	}
	// A provisional source won the same physical-key CAS. Resume that durable
	// source instead of treating its earlier absent read as permission to erase.
	return s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
}

func (s *scyllaDatastore) GetTranslation(ctx context.Context, uid string) (*datastore.Translation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	identity, err := s.readTranslationIdentity(ctx, uid)
	if err != nil {
		return nil, err
	}
	if identity.Retired {
		return nil, datastore.ErrNotFound
	}
	row, err := s.readTranslationSourceRow(ctx, identity.NamespaceUID, identity.CreatedAt, identity.UID)
	if err != nil {
		return nil, err
	}
	if row.Phase == "Retired" {
		return nil, datastore.ErrNotFound
	}
	if row.Phase != "Stable" {
		return nil, datastore.ErrRepairRequired
	}
	source := row.source()
	if source.Name != identity.Name || source.Namespace != identity.Namespace ||
		datastore.ValidateTranslationSource(source) != nil {
		return nil, datastore.ErrRepairRequired
	}
	return source, nil
}

func (s *scyllaDatastore) GetTranslationByName(ctx context.Context, namespace, name string) (*datastore.Translation, error) {
	ns, err := s.GetNamespaceByName(ctx, namespace)
	if err != nil {
		return nil, err
	}
	uid, _, phase, err := s.translationNameOwner(ctx, mustParseUUID(ns.UID), name)
	if err != nil {
		return nil, err
	}
	if phase != "Reserved" {
		return nil, datastore.ErrNotFound
	}
	source, err := s.GetTranslation(ctx, uid.String())
	if err != nil {
		return nil, err
	}
	if source.NamespaceID != ns.UID || source.Namespace != namespace || source.Name != name {
		return nil, datastore.ErrRepairRequired
	}
	return source, nil
}
