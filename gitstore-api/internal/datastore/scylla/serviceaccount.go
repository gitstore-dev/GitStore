// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

// serviceAccountRow mirrors the columns of service_accounts_by_namespace.
// ServiceAccount is not a git-backed catalog resource (unlike File/Product),
// so it does not carry api_version/kind/labels/annotations/owner_references/
// finalizers/repository_id/spec/body/status — see models.go.
type serviceAccountRow struct {
	Namespace         string     `db:"namespace"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
	UID               gocql.UUID `db:"uid"`
	Name              string     `db:"name"`
	Generation        int64      `db:"generation"`
	ResourceVersion   string     `db:"resource_version"`
	CreationActor     string     `db:"creation_actor"`
	UpdateTimestamp   time.Time  `db:"update_timestamp"`
	UpdateActor       string     `db:"update_actor"`
	DeletionTimestamp *time.Time `db:"deletion_timestamp"`
	Disabled          bool       `db:"disabled"`
	PublicKeys        string     `db:"public_keys"` // JSON-encoded []datastore.ServiceAccountPublicKey
}

type serviceAccountIndexRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

func toServiceAccountRow(sa *datastore.ServiceAccount) (*serviceAccountRow, error) {
	keys, err := json.Marshal(sa.PublicKeys)
	if err != nil {
		return nil, fmt.Errorf("scylla: marshal service account public keys: %w", err)
	}
	return &serviceAccountRow{
		Namespace:         sa.Namespace,
		CreationTimestamp: sa.CreationTimestamp,
		UID:               mustParseUUID(sa.UID),
		Name:              sa.Name,
		Generation:        sa.Generation,
		ResourceVersion:   sa.ResourceVersion,
		CreationActor:     sa.CreationActor,
		UpdateTimestamp:   sa.UpdateTimestamp,
		UpdateActor:       sa.UpdateActor,
		DeletionTimestamp: sa.DeletionTimestamp,
		Disabled:          sa.Disabled,
		PublicKeys:        string(keys),
	}, nil
}

func fromServiceAccountRow(r *serviceAccountRow) (*datastore.ServiceAccount, error) {
	var keys []datastore.ServiceAccountPublicKey
	if len(r.PublicKeys) > 0 {
		if err := json.Unmarshal([]byte(r.PublicKeys), &keys); err != nil {
			return nil, fmt.Errorf("scylla: unmarshal service account public keys: %w", err)
		}
	}
	return &datastore.ServiceAccount{
		UID:               r.UID.String(),
		Namespace:         r.Namespace,
		Name:              r.Name,
		Disabled:          r.Disabled,
		Generation:        r.Generation,
		ResourceVersion:   r.ResourceVersion,
		CreationTimestamp: r.CreationTimestamp,
		CreationActor:     r.CreationActor,
		UpdateTimestamp:   r.UpdateTimestamp,
		UpdateActor:       r.UpdateActor,
		PublicKeys:        keys,
		DeletionTimestamp: r.DeletionTimestamp,
	}, nil
}

func (s *scyllaDatastore) CreateServiceAccount(ctx context.Context, sa *datastore.ServiceAccount) error {
	if sa == nil || sa.UID == "" || sa.Namespace == "" || sa.Name == "" {
		return fmt.Errorf("%w: service account uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(sa.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid service account uid", datastore.ErrInvalidArgument)
	}
	if sa.CreationTimestamp.IsZero() {
		sa.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	applied, err := s.insertAuthoritative(ctx, s.serviceAccountByNamespaceTable, row)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrAlreadyExists
	}
	if err := s.reserveName(ctx, "ServiceAccount", "service_accounts_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp); err != nil {
		_ = s.deleteServiceAccountAuthoritative(ctx, row, row.ResourceVersion)
		return err
	}
	if err := s.reserveUID(ctx, "ServiceAccount", "service_accounts_by_uid", row.Namespace, uid, row.CreationTimestamp); err != nil {
		_ = s.releaseName(ctx, "service_accounts_by_name", row.Namespace, row.Name, uid)
		_ = s.deleteServiceAccountAuthoritative(ctx, row, row.ResourceVersion)
		return err
	}
	return nil
}

func (s *scyllaDatastore) GetServiceAccountByUID(ctx context.Context, uid string) (*datastore.ServiceAccount, error) {
	parsed, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, datastore.ErrNotFound
	}
	stmt, names := s.serviceAccountByUIDTable.Get()
	var idx serviceAccountIndexRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"uid": parsed}).GetRelease(&idx); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return s.getServiceAccountByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
}

func (s *scyllaDatastore) GetServiceAccountBySubject(ctx context.Context, namespace, name string) (*datastore.ServiceAccount, error) {
	stmt, names := s.serviceAccountByNameTable.Get()
	var idx serviceAccountIndexRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace": namespace, "name": name}).GetRelease(&idx); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return s.getServiceAccountByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
}

func (s *scyllaDatastore) getServiceAccountByKey(ctx context.Context, namespace string, created time.Time, uid gocql.UUID) (*datastore.ServiceAccount, error) {
	stmt := "SELECT namespace,creation_timestamp,uid,name,generation,resource_version,creation_actor,update_timestamp,update_actor,deletion_timestamp,disabled,public_keys " +
		"FROM service_accounts_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?"
	var row serviceAccountRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(namespace, created, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return fromServiceAccountRow(&row)
}

func (s *scyllaDatastore) ListServiceAccounts(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.ServiceAccount], error) {
	// No dedicated "list all namespaces" partition scan exists for
	// ServiceAccount (query-first, per 002_secondary_indexes.cql's rule);
	// the expected namespace count is small (one convention string per
	// controller class today), so a full-table scan via ALLOW FILTERING is
	// avoided by instead scanning service_accounts_by_uid, whose partition
	// key is uid — every row lives in its own partition, so this is a
	// full-table read regardless of predicate. Acceptable for an
	// admin-only, low-cardinality listing operation; revisit if
	// ServiceAccount count ever approaches catalog scale.
	var idxRows []serviceAccountIndexRow
	if err := s.session.Query("SELECT uid,namespace,creation_timestamp FROM service_accounts_by_uid", nil).WithContext(ctx).SelectRelease(&idxRows); err != nil {
		return nil, err
	}
	items := make([]*datastore.ServiceAccount, 0, len(idxRows))
	for _, idx := range idxRows {
		sa, err := s.getServiceAccountByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
		if err != nil {
			if errors.Is(err, datastore.ErrNotFound) {
				continue
			}
			return nil, err
		}
		items = append(items, sa)
	}
	return buildPageResult(items, page.Limit(), page), nil
}

func (s *scyllaDatastore) UpdateServiceAccountKeys(ctx context.Context, uid string, add []datastore.ServiceAccountPublicKey, removeKeyIDs []string, expectedResourceVersion string) (*datastore.ServiceAccount, error) {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return nil, err
	}
	if err := datastore.ApplyServiceAccountKeyUpdate(sa, add, removeKeyIDs, expectedResourceVersion); err != nil {
		return nil, err
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return nil, err
	}
	if err := s.updateServiceAccountKeysAuthoritative(ctx, row, expectedResourceVersion); err != nil {
		return nil, err
	}
	return sa, nil
}

func (s *scyllaDatastore) SetServiceAccountDisabled(ctx context.Context, uid string, disabled bool) error {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return err
	}
	expected := sa.ResourceVersion
	sa.Disabled = disabled
	datastore.AdvanceServiceAccountSystemVersion(sa)
	sa.UpdateTimestamp = time.Now().UTC()
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	return s.updateServiceAccountDisabledAuthoritative(ctx, row, expected)
}

func (s *scyllaDatastore) DeleteServiceAccount(ctx context.Context, uid string) error {
	sa, err := s.GetServiceAccountByUID(ctx, uid)
	if err != nil {
		return err
	}
	row, err := toServiceAccountRow(sa)
	if err != nil {
		return err
	}
	if err := s.deleteServiceAccountAuthoritative(ctx, row, sa.ResourceVersion); err != nil {
		return err
	}
	parsed := mustParseUUID(sa.UID)
	_ = s.releaseName(ctx, "service_accounts_by_name", sa.Namespace, sa.Name, parsed)
	_ = s.releaseUID(ctx, "service_accounts_by_uid", sa.Namespace, parsed, sa.CreationTimestamp)
	return nil
}

func (s *scyllaDatastore) updateServiceAccountKeysAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	const stmt = "UPDATE service_accounts_by_namespace SET generation=?,resource_version=?,update_timestamp=?,update_actor=?,public_keys=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		row.Generation, row.ResourceVersion, row.UpdateTimestamp, row.UpdateActor, row.PublicKeys,
		row.Namespace, row.CreationTimestamp, row.UID, expected,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) updateServiceAccountDisabledAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	const stmt = "UPDATE service_accounts_by_namespace SET disabled=?,resource_version=?,update_timestamp=? " +
		"WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(
		row.Disabled, row.ResourceVersion, row.UpdateTimestamp,
		row.Namespace, row.CreationTimestamp, row.UID, expected,
	).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) deleteServiceAccountAuthoritative(ctx context.Context, row *serviceAccountRow, expected string) error {
	return s.deleteAuthoritative(ctx,
		"DELETE FROM service_accounts_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?",
		row.Namespace, row.CreationTimestamp, row.UID, expected,
	)
}
