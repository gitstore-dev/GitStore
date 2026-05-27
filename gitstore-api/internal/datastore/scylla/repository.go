// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

type repositoryRow struct {
	ID            gocql.UUID `db:"id"`
	NamespaceID   gocql.UUID `db:"namespace_id"`
	Name          string     `db:"name"`
	DefaultBranch string     `db:"default_branch"`
	StorageClass  string     `db:"storage_class"`
	CreatedAt     time.Time  `db:"created_at"`
	CreatedBy     string     `db:"created_by"`
	UpdatedAt     time.Time  `db:"updated_at"`
	UpdatedBy     string     `db:"updated_by"`
}

func toRepositoryRow(r *datastore.Repository) *repositoryRow {
	return &repositoryRow{
		ID:            mustParseUUID(r.ID),
		NamespaceID:   mustParseUUID(r.NamespaceID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
	}
}

func fromRepositoryRow(r *repositoryRow) *datastore.Repository {
	return &datastore.Repository{
		ID:            r.ID.String(),
		NamespaceID:   r.NamespaceID.String(),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
		CreatedAt:     r.CreatedAt,
		CreatedBy:     r.CreatedBy,
		UpdatedAt:     r.UpdatedAt,
		UpdatedBy:     r.UpdatedBy,
	}
}

func (s *scyllaDatastore) CreateRepository(ctx context.Context, r *datastore.Repository) error {
	if _, err := s.GetRepository(ctx, r.ID); err == nil {
		return fmt.Errorf("%w: repository id %s", datastore.ErrAlreadyExists, r.ID)
	}
	row := toRepositoryRow(r)
	stmt, names := s.repositoryTable.Insert()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: create repository: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) GetRepository(_ context.Context, id string) (*datastore.Repository, error) {
	var row repositoryRow
	stmt, names := s.repositoryTable.Get()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: repository id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get repository: %w", err)
	}
	return fromRepositoryRow(&row), nil
}

func (s *scyllaDatastore) ListRepositoriesByNamespace(_ context.Context, namespaceID string) ([]*datastore.Repository, error) {
	stmt, names := qb.Select("repositories").
		Columns(s.repositoryTable.Metadata().Columns...).
		Where(qb.Eq("namespace_id")).
		ToCql()
	var rows []repositoryRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace_id": namespaceID}).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list repositories by namespace: %w", err)
	}
	repos := make([]*datastore.Repository, len(rows))
	for i := range rows {
		repos[i] = fromRepositoryRow(&rows[i])
	}
	return repos, nil
}

func (s *scyllaDatastore) UpdateRepository(_ context.Context, r *datastore.Repository) error {
	row := toRepositoryRow(r)
	stmt, names := s.repositoryTable.Update()
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update repository: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteRepository(ctx context.Context, id string) error {
	if _, err := s.GetRepository(ctx, id); err != nil {
		return err
	}
	stmt, names := s.repositoryTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": id}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete repository: %w", err)
	}
	return nil
}
