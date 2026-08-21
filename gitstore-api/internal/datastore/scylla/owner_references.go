// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
)

const ownerReferenceDependentsTable = "owner_reference_dependents"

type ownerReferenceDependentRow struct {
	Namespace          string `db:"namespace"`
	RepositoryID       string `db:"repository_id"`
	OwnerUID           string `db:"owner_uid"`
	BlockOwnerDeletion bool   `db:"block_owner_deletion"`
	DependentKind      string `db:"dependent_kind"`
	DependentUID       string `db:"dependent_uid"`
	Name               string `db:"name"`
	ResourceVersion    string `db:"resource_version"`
}

func decodeOwnerReferences(raw json.RawMessage) ([]catalog.OwnerReference, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var references []catalog.OwnerReference
	if err := json.Unmarshal(raw, &references); err != nil {
		return nil, fmt.Errorf("scylla: decode owner references: %w", err)
	}
	return references, nil
}

func (s *scyllaDatastore) syncOwnerReferenceDependents(
	ctx context.Context,
	namespace, repositoryID, dependentKind, dependentUID, name, resourceVersion string,
	previous, current json.RawMessage,
) error {
	previousRefs, err := decodeOwnerReferences(previous)
	if err != nil {
		return err
	}
	currentRefs, err := decodeOwnerReferences(current)
	if err != nil {
		return err
	}
	for _, ref := range previousRefs {
		if ref.UID == "" {
			continue
		}
		ownerRepositoryID := ref.RepositoryID
		if ownerRepositoryID == "" {
			ownerRepositoryID = repositoryID
		}
		if err := s.deleteOwnerReferenceDependent(ctx, namespace, ownerRepositoryID, ref.UID, ref.BlockOwnerDeletion, dependentKind, dependentUID); err != nil {
			return err
		}
	}
	for _, ref := range currentRefs {
		if ref.UID == "" {
			continue
		}
		ownerRepositoryID := ref.RepositoryID
		if ownerRepositoryID == "" {
			ownerRepositoryID = repositoryID
		}
		if err := s.upsertOwnerReferenceDependent(ctx, ownerReferenceDependentRow{
			Namespace: namespace, RepositoryID: ownerRepositoryID, OwnerUID: ref.UID,
			BlockOwnerDeletion: ref.BlockOwnerDeletion, DependentKind: dependentKind,
			DependentUID: dependentUID, Name: name, ResourceVersion: resourceVersion,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *scyllaDatastore) upsertOwnerReferenceDependent(ctx context.Context, row ownerReferenceDependentRow) error {
	const stmt = `INSERT INTO owner_reference_dependents
		(namespace, repository_id, owner_uid, block_owner_deletion, dependent_kind, dependent_uid, name, resource_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		row.Namespace, row.RepositoryID, row.OwnerUID, row.BlockOwnerDeletion,
		row.DependentKind, row.DependentUID, row.Name, row.ResourceVersion,
	).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: upsert owner reference dependent: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) deleteOwnerReferenceDependent(
	ctx context.Context,
	namespace, repositoryID, ownerUID string,
	blockOwnerDeletion bool,
	dependentKind, dependentUID string,
) error {
	const stmt = `DELETE FROM owner_reference_dependents
		WHERE namespace=? AND repository_id=? AND owner_uid=? AND block_owner_deletion=?
		AND dependent_kind=? AND dependent_uid=?`
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		namespace, repositoryID, ownerUID, blockOwnerDeletion, dependentKind, dependentUID,
	).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete owner reference dependent: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) HasBlockingOwnerDependents(ctx context.Context, scope datastore.OwnerReferenceScope, ownerUID string) (bool, error) {
	const stmt = `SELECT dependent_uid FROM owner_reference_dependents
		WHERE namespace=? AND repository_id=? AND owner_uid=? AND block_owner_deletion=? LIMIT 1`
	var row struct {
		DependentUID string `db:"dependent_uid"`
	}
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		scope.Namespace, scope.RepositoryID, ownerUID, true,
	).GetRelease(&row); err != nil {
		if err == gocql.ErrNotFound {
			return false, nil
		}
		return false, fmt.Errorf("scylla: check blocking owner dependents: %w", err)
	}
	return true, nil
}

func (s *scyllaDatastore) ListNonBlockingProductOwnerDependents(
	ctx context.Context,
	scope datastore.OwnerReferenceScope,
	ownerUID, after string,
	limit int,
) (datastore.OwnerDependentPage, error) {
	if limit <= 0 {
		limit = datastore.DefaultPageSize
	}
	if limit > datastore.MaxOwnerDependentPageSize {
		limit = datastore.MaxOwnerDependentPageSize
	}
	const base = `SELECT dependent_uid, dependent_kind, name, resource_version
		FROM owner_reference_dependents
		WHERE namespace=? AND repository_id=? AND owner_uid=? AND block_owner_deletion=?
		AND dependent_kind=?`
	args := []any{scope.Namespace, scope.RepositoryID, ownerUID, false, "Product"}
	stmt := base
	if after != "" {
		stmt += " AND dependent_uid > ?"
		args = append(args, after)
	}
	stmt += " LIMIT ?"
	args = append(args, limit+1)
	var rows []ownerReferenceDependentRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
		return datastore.OwnerDependentPage{}, fmt.Errorf("scylla: list non-blocking Product owner dependents: %w", err)
	}
	page := datastore.OwnerDependentPage{Items: make([]datastore.OwnerDependent, 0, len(rows))}
	for _, row := range rows {
		page.Items = append(page.Items, datastore.OwnerDependent{
			DependentUID: row.DependentUID, DependentKind: row.DependentKind,
			Name: row.Name, ResourceVersion: row.ResourceVersion,
		})
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].DependentUID
	}
	return page, nil
}

func (s *scyllaDatastore) MarkCategoryTaxonomyDeletion(
	ctx context.Context,
	namespace, name, expectedResourceVersion string,
	at time.Time,
) (*datastore.CategoryTaxonomy, error) {
	category, err := s.GetCategoryTaxonomyByName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if category.DeletionTimestamp != nil {
		return category, nil
	}
	if category.ResourceVersion != expectedResourceVersion {
		return nil, datastore.ErrConflict
	}
	category.DeletionTimestamp = &at
	if !containsFinalizer(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer) {
		category.Finalizers = append(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer)
	}
	if err := datastore.MarkCategoryTaxonomyTerminating(category, at); err != nil {
		return nil, err
	}
	category.UpdateTimestamp = at
	category.UpdateActor = "deletion"
	datastore.AdvanceCategoryTaxonomySystemVersion(category)
	uid := mustParseUUID(category.UID)
	const stmt = `UPDATE category_taxonomy
		SET resource_version=?, deletion_timestamp=?, finalizers=?, status=?, update_timestamp=?, update_actor=?
		WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?`
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		category.ResourceVersion, category.DeletionTimestamp, category.Finalizers, string(category.Status), category.UpdateTimestamp, category.UpdateActor,
		category.Namespace, category.CreationTimestamp, uid, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return nil, fmt.Errorf("scylla: mark category taxonomy deletion: %w", err)
	}
	if !applied {
		return nil, datastore.ErrConflict
	}
	return category, nil
}

func (s *scyllaDatastore) CompleteCategoryTaxonomyDeletion(
	ctx context.Context,
	namespace, name, expectedResourceVersion string,
) (*datastore.CategoryTaxonomy, error) {
	category, err := s.GetCategoryTaxonomyByName(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	if category.ResourceVersion != expectedResourceVersion {
		return nil, datastore.ErrConflict
	}
	if category.DeletionTimestamp == nil || !containsFinalizer(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer) {
		return nil, fmt.Errorf("%w: category taxonomy is not terminating", datastore.ErrInvalidArgument)
	}
	// deleteCategoryTaxonomyWithResourceVersion executes reverse projections
	// before the authoritative CAS and compensates them on a pre-commit
	// failure, so retrying converges a partially failed projection.
	if err := s.deleteCategoryTaxonomyWithResourceVersion(ctx, category, expectedResourceVersion); err != nil {
		return nil, err
	}
	return category, nil
}

func containsFinalizer(finalizers []string, finalizer string) bool {
	for _, current := range finalizers {
		if current == finalizer {
			return true
		}
	}
	return false
}
