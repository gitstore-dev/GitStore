// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

type repositoryRow struct {
	Bucket        string     `db:"bucket"`
	CreatedAt     time.Time  `db:"created_at"`
	ID            gocql.UUID `db:"id"`
	NamespaceID   gocql.UUID `db:"namespace_id"`
	Name          string     `db:"name"`
	DefaultBranch string     `db:"default_branch"`
	StorageClass  string     `db:"storage_class"`
	CreatedBy     string     `db:"created_by"`
	UpdatedAt     time.Time  `db:"updated_at"`
	UpdatedBy     string     `db:"updated_by"`
}

func toRepositoryRow(r *datastore.Repository) *repositoryRow {
	return &repositoryRow{
		Bucket:        BucketAll,
		CreatedAt:     r.CreatedAt,
		ID:            mustParseUUID(r.ID),
		NamespaceID:   mustParseUUID(r.NamespaceID),
		Name:          r.Name,
		DefaultBranch: r.DefaultBranch,
		StorageClass:  r.StorageClass,
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
	uid, err := gocql.ParseUUID(id)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid repository id %s", datastore.ErrNotFound, id)
	}
	// Use secondary index on id
	stmt, names := qb.Select("repositories").
		Columns(s.repositoryTable.Metadata().Columns...).
		Where(qb.Eq("id")).
		Limit(1).
		ToCql()
	var row repositoryRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"id": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: repository id %s", datastore.ErrNotFound, id)
		}
		return nil, fmt.Errorf("scylla: get repository: %w", err)
	}
	return fromRepositoryRow(&row), nil
}

func (s *scyllaDatastore) ListRepositoriesByNamespace(_ context.Context, namespaceID string, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	nsUUID, err := gocql.ParseUUID(namespaceID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid namespace_id", datastore.ErrInvalidArgument)
	}

	// Secondary index query — ORDER BY is not supported, so fetch all and paginate in-memory.
	cols := s.repositoryTable.Metadata().Columns
	stmt, names := qb.Select("repositories").
		Columns(cols...).
		Where(qb.Eq("namespace_id")).
		ToCql()

	var rows []repositoryRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace_id": nsUUID}).SelectRelease(&rows); err != nil {
		return nil, fmt.Errorf("scylla: list repositories by namespace: %w", err)
	}

	// Sort descending by (created_at, id) — matching partition table ordering
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].ID.String() > rows[j].ID.String()
		}
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})

	repos := make([]*datastore.Repository, len(rows))
	for i := range rows {
		repos[i] = fromRepositoryRow(&rows[i])
	}

	return paginateInMemory(repos, page), nil
}

func (s *scyllaDatastore) UpdateRepository(_ context.Context, r *datastore.Repository) error {
	row := toRepositoryRow(r)
	stmt, names := s.repositoryTable.Update(
		"namespace_id", "name", "default_branch", "storage_class",
		"created_by", "updated_at", "updated_by",
	)
	if err := s.session.Query(stmt, names).BindStruct(row).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: update repository: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteRepository(ctx context.Context, id string) error {
	repo, err := s.GetRepository(ctx, id)
	if err != nil {
		return err
	}
	stmt, names := s.repositoryTable.Delete()
	if err := s.session.Query(stmt, names).BindMap(qb.M{
		"bucket":     BucketAll,
		"created_at": repo.CreatedAt,
		"id":         mustParseUUID(id),
	}).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete repository: %w", err)
	}
	return nil
}

// reverseRows reverses a slice of repositoryRow in place.
func reverseRows[T any](rows []T) {
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
}
