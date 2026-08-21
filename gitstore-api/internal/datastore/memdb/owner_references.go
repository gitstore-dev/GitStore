// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package memdb

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	gomemdb "github.com/hashicorp/go-memdb"
)

type ownerReferenceProjection struct {
	ID              string
	Namespace       string
	RepositoryID    string
	OwnerUID        string
	BlockKey        string
	DependentUID    string
	DependentKind   string
	Name            string
	ResourceVersion string
}

func projectionID(namespace, repositoryID, ownerUID, dependentKind, dependentUID string) string {
	return strings.Join([]string{namespace, repositoryID, ownerUID, dependentKind, dependentUID}, "\x00")
}

func blockKey(block bool) string {
	if block {
		return "1"
	}
	return "0"
}

func syncOwnerReferenceProjections(txn *gomemdb.Txn, namespace, repositoryID, kind, uid, name, resourceVersion string, raw json.RawMessage) error {
	if err := deleteOwnerReferenceProjections(txn, kind, uid); err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	var refs []catalog.OwnerReference
	if err := json.Unmarshal(raw, &refs); err != nil {
		return fmt.Errorf("memdb: decode owner references for %s %s: %w", kind, uid, err)
	}
	for _, ref := range refs {
		if ref.UID == "" {
			continue
		}
		projection := &ownerReferenceProjection{
			ID:              projectionID(namespace, repositoryID, ref.UID, kind, uid),
			Namespace:       namespace,
			RepositoryID:    repositoryID,
			OwnerUID:        ref.UID,
			BlockKey:        blockKey(ref.BlockOwnerDeletion),
			DependentUID:    uid,
			DependentKind:   kind,
			Name:            name,
			ResourceVersion: resourceVersion,
		}
		if err := txn.Insert("owner_reference", projection); err != nil {
			return fmt.Errorf("memdb: write owner reference projection: %w", err)
		}
	}
	return nil
}

func deleteOwnerReferenceProjections(txn *gomemdb.Txn, kind, uid string) error {
	it, err := txn.Get("owner_reference", "dependent", kind, uid)
	if err != nil {
		return fmt.Errorf("memdb: find owner reference projections: %w", err)
	}
	var projections []*ownerReferenceProjection
	for raw := it.Next(); raw != nil; raw = it.Next() {
		projections = append(projections, raw.(*ownerReferenceProjection))
	}
	for _, projection := range projections {
		if err := txn.Delete("owner_reference", projection); err != nil {
			return fmt.Errorf("memdb: delete owner reference projection: %w", err)
		}
	}
	return nil
}

func (m *memdbDatastore) HasBlockingOwnerDependents(_ context.Context, scope datastore.OwnerReferenceScope, ownerUID string) (bool, error) {
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("owner_reference", "owner_block", scope.Namespace, scope.RepositoryID, ownerUID, "1")
	if err != nil {
		return false, fmt.Errorf("memdb: check blocking owner dependents: %w", err)
	}
	return it.Next() != nil, nil
}

func (m *memdbDatastore) ListNonBlockingProductOwnerDependents(_ context.Context, scope datastore.OwnerReferenceScope, ownerUID, after string, limit int) (datastore.OwnerDependentPage, error) {
	if limit <= 0 {
		limit = datastore.DefaultPageSize
	}
	if limit > datastore.MaxOwnerDependentPageSize {
		limit = datastore.MaxOwnerDependentPageSize
	}
	txn := m.db.Txn(false)
	defer txn.Abort()
	it, err := txn.Get("owner_reference", "owner_product", scope.Namespace, scope.RepositoryID, ownerUID, "Product", "0")
	if err != nil {
		return datastore.OwnerDependentPage{}, fmt.Errorf("memdb: list non-blocking product dependents: %w", err)
	}
	var dependencies []datastore.OwnerDependent
	for raw := it.Next(); raw != nil; raw = it.Next() {
		projection := raw.(*ownerReferenceProjection)
		if projection.DependentUID <= after {
			continue
		}
		dependencies = append(dependencies, datastore.OwnerDependent{
			DependentUID:    projection.DependentUID,
			DependentKind:   projection.DependentKind,
			Name:            projection.Name,
			ResourceVersion: projection.ResourceVersion,
		})
	}
	sort.Slice(dependencies, func(i, j int) bool {
		return dependencies[i].DependentUID < dependencies[j].DependentUID
	})
	page := datastore.OwnerDependentPage{Items: dependencies}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCursor = page.Items[len(page.Items)-1].DependentUID
	}
	return page, nil
}

func (m *memdbDatastore) MarkCategoryTaxonomyDeletion(_ context.Context, namespace, name, expectedResourceVersion string, at time.Time) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(true)
	raw, err := txn.First("category_taxonomy", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		txn.Abort()
		return nil, fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, namespace, name)
	}
	category := cloneCategoryTaxonomy(raw.(*datastore.CategoryTaxonomy))
	if category.DeletionTimestamp != nil {
		txn.Abort()
		return category, nil
	}
	if category.ResourceVersion != expectedResourceVersion {
		txn.Abort()
		return nil, datastore.ErrConflict
	}
	category.DeletionTimestamp = &at
	if !containsFinalizer(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer) {
		category.Finalizers = append(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer)
	}
	if err := datastore.MarkCategoryTaxonomyTerminating(category, at); err != nil {
		txn.Abort()
		return nil, err
	}
	category.UpdateTimestamp = at
	category.UpdateActor = "deletion"
	datastore.AdvanceCategoryTaxonomySystemVersion(category)
	if err := txn.Insert("category_taxonomy", category); err != nil {
		txn.Abort()
		return nil, fmt.Errorf("memdb: mark category taxonomy deletion: %w", err)
	}
	if err := syncOwnerReferenceProjections(txn, category.Namespace, category.RepositoryID, "CategoryTaxonomy", category.UID, category.Name, category.ResourceVersion, category.OwnerReferences); err != nil {
		txn.Abort()
		return nil, err
	}
	txn.Commit()
	return cloneCategoryTaxonomy(category), nil
}

func (m *memdbDatastore) CompleteCategoryTaxonomyDeletion(_ context.Context, namespace, name, expectedResourceVersion string) (*datastore.CategoryTaxonomy, error) {
	txn := m.db.Txn(true)
	raw, err := txn.First("category_taxonomy", "name_namespace", namespace, name)
	if err != nil || raw == nil {
		txn.Abort()
		return nil, fmt.Errorf("%w: category_taxonomy %s/%s", datastore.ErrNotFound, namespace, name)
	}
	category := raw.(*datastore.CategoryTaxonomy)
	if category.ResourceVersion != expectedResourceVersion {
		txn.Abort()
		return nil, datastore.ErrConflict
	}
	if category.DeletionTimestamp == nil || !containsFinalizer(category.Finalizers, datastore.CategoryTaxonomyForegroundDeletionFinalizer) {
		txn.Abort()
		return nil, fmt.Errorf("%w: category taxonomy is not terminating", datastore.ErrInvalidArgument)
	}
	if err := deleteOwnerReferenceProjections(txn, "CategoryTaxonomy", category.UID); err != nil {
		txn.Abort()
		return nil, err
	}
	if err := txn.Delete("category_taxonomy", category); err != nil {
		txn.Abort()
		return nil, fmt.Errorf("memdb: complete category taxonomy deletion: %w", err)
	}
	txn.Commit()
	return cloneCategoryTaxonomy(category), nil
}

func containsFinalizer(finalizers []string, target string) bool {
	for _, finalizer := range finalizers {
		if finalizer == target {
			return true
		}
	}
	return false
}
