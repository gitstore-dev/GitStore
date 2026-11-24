// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"math/big"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

type translationOwnerKey struct {
	namespace, repository, owner, dependent string
	block                                   bool
}

func (s *scyllaDatastore) syncTranslationOwners(ctx context.Context, coordination string, previous, current *datastore.Translation) error {
	changes := map[translationOwnerKey]bool{}
	for side, source := range []*datastore.Translation{previous, current} {
		if source == nil {
			continue
		}
		refs, err := decodeOwnerReferences(source.OwnerReferences)
		if err != nil {
			return datastore.ErrRepairRequired
		}
		for _, ref := range refs {
			if ref.UID == "" {
				continue
			}
			repository := ref.RepositoryID
			if repository == "" {
				repository = source.RepositoryID
			}
			changes[translationOwnerKey{source.Namespace, repository, ref.UID, source.UID, ref.BlockOwnerDeletion}] = side == 0
		}
	}
	source := current
	if source == nil {
		source = previous
	}
	for owner, retired := range changes {
		key := translationCellLocation{
			table:   ownerReferenceDependentsTable,
			columns: []string{"namespace", "repository_id", "owner_uid", "block_owner_deletion", "dependent_kind", "dependent_uid"},
			values:  []any{owner.namespace, owner.repository, owner.owner, owner.block, "", owner.dependent},
		}
		values := []any{owner.namespace, owner.repository, owner.owner, owner.block, "Translation", owner.dependent}
		statement := "DELETE FROM " + key.table + " WHERE " + key.predicate()
		if !retired {
			statement = "INSERT INTO " + key.table + " (" + strings.Join(key.columns, ",") + ",name,resource_version) VALUES (?,?,?,?,?,?,?,?)"
			values = append(values, source.Name, source.ResourceVersion)
		}
		if err := s.writeTranslationSourceProjection(ctx, key, coordination, retired, statement, values); err != nil {
			return err
		}
	}
	return nil
}

// Empty-kind markers sort before real dependents and share their partition.
// One conditional batch advances the retained fence and changes the live edge.
func (s *scyllaDatastore) writeTranslationSourceProjection(ctx context.Context, key translationCellLocation, coordination string, retired bool, statement string, values []any) error {
	if _, err := datastore.AdvanceTranslationIndexVersion(coordination); err != nil {
		return err
	}

	for range 4 {
		var version string
		var wasRetired *bool
		err := s.session.Session.Query(
			"SELECT translation_coordination_version,translation_projection_retired FROM "+key.table+" WHERE "+key.predicate(), key.values...,
		).WithContext(ctx).Consistency(gocql.Serial).Scan(&version, &wasRetired)
		if err != nil && !errors.Is(err, gocql.ErrNotFound) {
			return err
		}
		var marker string
		var args []any
		if errors.Is(err, gocql.ErrNotFound) {
			columns := append(append([]string(nil), key.columns...), "translation_coordination_version", "translation_projection_retired")
			marker = "INSERT INTO " + key.table + " (" + strings.Join(columns, ",") + ") VALUES (" +
				strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ") IF NOT EXISTS"
			args = append(append([]any(nil), key.values...), coordination, retired)
		} else {
			if _, err := datastore.AdvanceTranslationIndexVersion(version); err != nil || wasRetired == nil {
				return datastore.ErrRepairRequired
			}
			if version != coordination {
				before, _ := new(big.Int).SetString(version, 10)
				desired, _ := new(big.Int).SetString(coordination, 10)
				if before.Cmp(desired) > 0 {
					return nil
				}
				if before.Cmp(desired) == 0 {
					return datastore.ErrRepairRequired
				}
			} else if *wasRetired != retired {
				return datastore.ErrRepairRequired
			}
			marker = "UPDATE " + key.table + " SET translation_coordination_version=?,translation_projection_retired=? WHERE " +
				key.predicate() + " IF translation_coordination_version=? AND translation_projection_retired=?"
			args = append([]any{coordination, retired}, key.values...)
			args = append(args, version, *wasRetired)
		}
		args = append(args, values...)
		applied, err := s.translationIndexCAS(ctx, "BEGIN BATCH "+marker+"; "+statement+"; APPLY BATCH", args...)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return datastore.ErrConflict
}

func (s *scyllaDatastore) syncTranslationSourceProjections(ctx context.Context, coordination string, previous, current *datastore.Translation) error {
	if err := s.syncTranslationOwners(ctx, coordination, previous, current); err != nil {
		return err
	}
	source := current
	if source == nil {
		source = previous
	}
	if source == nil || source.RepositoryID == "" {
		return nil
	}
	repository, err := gocql.ParseUUID(source.RepositoryID)
	if err != nil {
		return datastore.ErrRepairRequired
	}
	uid, err := gocql.ParseUUID(source.UID)
	if err != nil {
		return datastore.ErrRepairRequired
	}
	shard := categoryProductShard(source.UID)
	key := translationCellLocation{
		table: repositoryResourcesTable, columns: []string{"repository_id", "shard", "kind", "uid"},
		values: []any{repository, shard, "", uid},
	}
	values := []any{repository, shard, "Translation", uid}
	statement := "DELETE FROM " + key.table + " WHERE " + key.predicate()
	if current != nil {
		statement = "INSERT INTO " + key.table + " (repository_id,shard,kind,uid,namespace,name,creation_timestamp) VALUES (?,?,?,?,?,?,?)"
		values = append(values, source.Namespace, source.Name, source.CreationTimestamp)
	}
	return s.writeTranslationSourceProjection(ctx, key, coordination, current == nil, statement, values)
}
