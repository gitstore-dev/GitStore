// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

type namespaceMappingRow struct {
	NamespaceID gocql.UUID `db:"namespace_id"`
	Name        string     `db:"name"`
	RepoID      gocql.UUID `db:"repo_id"`
}

func toMappingRow(m *datastore.NamespaceMapping) *namespaceMappingRow {
	return &namespaceMappingRow{
		NamespaceID: mustParseUUID(m.NamespaceID),
		Name:        m.Name,
		RepoID:      mustParseUUID(m.RepoID),
	}
}

func fromMappingRow(r *namespaceMappingRow) *datastore.NamespaceMapping {
	return &datastore.NamespaceMapping{
		NamespaceID: r.NamespaceID.String(),
		Name:        r.Name,
		RepoID:      r.RepoID.String(),
	}
}

func (s *scyllaDatastore) CreateNamespaceMapping(_ context.Context, m *datastore.NamespaceMapping) error {
	row := toMappingRow(m)
	// LWT: INSERT IF NOT EXISTS so a duplicate (namespace_id, name) returns
	// ErrAlreadyExists instead of silently overwriting another repo's mapping.
	stmt, names := qb.Insert("namespace_mappings").
		Columns(s.namespaceMappingTable.Metadata().Columns...).
		Unique().
		ToCql()
	applied, err := s.session.Query(stmt, names).BindStruct(row).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: create namespace_mapping: %w", err)
	}
	if !applied {
		return fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrAlreadyExists, m.NamespaceID, m.Name)
	}
	return nil
}

func (s *scyllaDatastore) LookupRepository(_ context.Context, namespaceID, name string) (*datastore.NamespaceMapping, error) {
	var row namespaceMappingRow
	stmt, names := s.namespaceMappingTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace_id": namespaceID, "name": name}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace_mapping (%s, %s)", datastore.ErrNotFound, namespaceID, name)
		}
		return nil, fmt.Errorf("scylla: lookup repository: %w", err)
	}
	return fromMappingRow(&row), nil
}

func (s *scyllaDatastore) LookupNamespaceByRepoID(_ context.Context, repoID string) (*datastore.NamespaceMapping, error) {
	stmt, names := qb.Select("namespace_mappings").
		Columns(s.namespaceMappingTable.Metadata().Columns...).
		Where(qb.Eq("repo_id")).
		ToCql()
	var row namespaceMappingRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"repo_id": repoID}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: namespace_mapping repo_id %s", datastore.ErrNotFound, repoID)
		}
		return nil, fmt.Errorf("scylla: lookup namespace by repo_id: %w", err)
	}
	return fromMappingRow(&row), nil
}

func (s *scyllaDatastore) RenameRepository(ctx context.Context, namespaceID, oldName, newName string) error {
	old, err := s.LookupRepository(ctx, namespaceID, oldName)
	if err != nil {
		return err
	}
	// Insert the new name first via LWT; if the target name is already taken,
	// CreateNamespaceMapping returns ErrAlreadyExists and the old mapping stays intact.
	if err := s.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		NamespaceID: namespaceID,
		Name:        newName,
		RepoID:      old.RepoID,
	}); err != nil {
		return err
	}
	return s.DeleteNamespaceMapping(ctx, namespaceID, oldName)
}

func (s *scyllaDatastore) TransferRepository(ctx context.Context, repoID, fromNamespaceID, toNamespaceID string) error {
	old, err := s.LookupNamespaceByRepoID(ctx, repoID)
	if err != nil {
		return err
	}
	// Insert the destination mapping first via LWT so we don't clobber an
	// existing repo with the same name in the target namespace.
	if err := s.CreateNamespaceMapping(ctx, &datastore.NamespaceMapping{
		NamespaceID: toNamespaceID,
		Name:        old.Name,
		RepoID:      repoID,
	}); err != nil {
		return err
	}
	return s.DeleteNamespaceMapping(ctx, fromNamespaceID, old.Name)
}

func (s *scyllaDatastore) DeleteNamespaceMapping(_ context.Context, namespaceID, name string) error {
	stmt, names := s.namespaceMappingTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace_id": namespaceID, "name": name}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete namespace_mapping: %w", err)
	}
	return nil
}
