// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
)

type repositoryRow struct {
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Namespace         string            `db:"namespace"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	CreationActor     string            `db:"creation_actor"`
	UpdateTimestamp   time.Time         `db:"update_timestamp"`
	UpdateActor       string            `db:"update_actor"`
	Labels            map[string]string `db:"labels"`
	Annotations       map[string]string `db:"annotations"`
	OwnerReferences   string            `db:"owner_references"`
	Finalizers        []string          `db:"finalizers"`
	DeletionTimestamp *time.Time        `db:"deletion_timestamp"`
	RepositoryID      *gocql.UUID       `db:"repository_id"`
	SourcePath        string            `db:"source_path"`
	GitCommitSHA      string            `db:"git_commit_sha"`
	GitRef            string            `db:"git_ref"`
	Spec              string            `db:"spec"`
	Body              string            `db:"body"`
	Status            string            `db:"status"`
	DefaultBranch     string            `db:"default_branch"`
	StorageClass      string            `db:"storage_class"`
	MaxPackSizeBytes  int64             `db:"max_pack_size_bytes"`
	MaxFileSizeBytes  int64             `db:"max_file_size_bytes"`
}

type repositoryIndexRow struct {
	Namespace         string     `db:"namespace"`
	Bucket            string     `db:"bucket"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
	UID               gocql.UUID `db:"uid"`
}

func (s *scyllaDatastore) CreateRepository(ctx context.Context, repository *datastore.Repository) error {
	if repository == nil {
		return fmt.Errorf("%w: repository is nil", datastore.ErrInvalidArgument)
	}
	if repository.UID == "" || repository.Namespace == "" || repository.Name == "" {
		return fmt.Errorf("%w: repository uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	if _, err := gocql.ParseUUID(repository.UID); err != nil {
		return fmt.Errorf("%w: invalid repository uid %s", datastore.ErrInvalidArgument, repository.UID)
	}
	datastore.NormalizeRepositoryContract(repository)
	row := toRepositoryRow(repository)
	path := repositoryPath{namespace: repository.Namespace, name: repository.Name}
	pathCreated, err := s.reserveRepositoryPathOwned(ctx, path, repository.UID)
	if err != nil {
		return err
	}

	insert, names := s.repositoryByUIDTable.Insert()
	applied, err := s.session.Query(insert+" IF NOT EXISTS", names).WithContext(ctx).
		BindStruct(row).ExecCASRelease()
	if err != nil {
		if pathCreated {
			if compensationErr := s.releaseRepositoryPath(ctx, path, repository.UID); compensationErr != nil {
				return datastore.NewRepairRequiredError(
					datastore.MutationStep{
						Operation:    "create_repository",
						ResourceKind: "Repository",
						ResourceUID:  repository.UID,
						Projection:   "namespace_mappings",
						LookupKey:    path.key(),
						Action:       "reserve",
					},
					fmt.Errorf("scylla: create repository authoritative row: %w", err),
					compensationErr,
				)
			}
		}
		return fmt.Errorf("scylla: create repository authoritative row: %w", err)
	}
	if !applied {
		if pathCreated {
			if compensationErr := s.releaseRepositoryPath(ctx, path, repository.UID); compensationErr != nil {
				return datastore.NewRepairRequiredError(
					datastore.MutationStep{
						Operation:    "create_repository",
						ResourceKind: "Repository",
						ResourceUID:  repository.UID,
						Projection:   "namespace_mappings",
						LookupKey:    path.key(),
						Action:       "reserve",
					},
					fmt.Errorf("%w: repository uid %s", datastore.ErrAlreadyExists, repository.UID),
					compensationErr,
				)
			}
		}
		return fmt.Errorf("%w: repository uid %s", datastore.ErrAlreadyExists, repository.UID)
	}

	reverseCreated, err := s.reserveRepositoryReverseMappingOwned(ctx, path, repository.UID)
	if err != nil {
		return s.failRepositoryCreate(ctx, repository, path, pathCreated, false, err)
	}
	bucket := namespaceBucket(repository.CreationTimestamp)
	if err := s.insertRepositoryProjections(ctx, repository.Namespace, bucket, repository.CreationTimestamp, row.UID); err != nil {
		return s.failRepositoryCreate(ctx, repository, path, pathCreated, reverseCreated, err)
	}
	return nil
}

func (s *scyllaDatastore) failRepositoryCreate(
	ctx context.Context,
	repository *datastore.Repository,
	path repositoryPath,
	pathCreated, reverseCreated bool,
	primary error,
) error {
	var compensationErr error
	uid := mustParseUUID(repository.UID)
	applied, err := s.session.Query(
		"DELETE FROM repositories_by_uid WHERE uid=? IF resource_version=?",
		nil,
	).WithContext(ctx).Bind(uid, repository.ResourceVersion).ExecCASRelease()
	if err != nil {
		compensationErr = errors.Join(compensationErr, fmt.Errorf("delete authoritative repository: %w", err))
	} else if !applied {
		compensationErr = errors.Join(compensationErr, fmt.Errorf("authoritative repository changed during compensation"))
	}
	if reverseCreated {
		if err := s.deleteRepositoryReverseMapping(ctx, path, repository.UID); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	if pathCreated {
		if err := s.releaseRepositoryPath(ctx, path, repository.UID); err != nil {
			compensationErr = errors.Join(compensationErr, err)
		}
	}
	if compensationErr != nil {
		return datastore.NewRepairRequiredError(
			datastore.MutationStep{
				Operation:    "create_repository",
				ResourceKind: "Repository",
				ResourceUID:  repository.UID,
				Projection:   "repository_projections",
				LookupKey:    path.key(),
				Action:       "compensate",
			},
			primary,
			compensationErr,
		)
	}
	return primary
}

func (s *scyllaDatastore) GetRepository(ctx context.Context, uidString string) (*datastore.Repository, error) {
	uid, err := gocql.ParseUUID(uidString)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid repository uid %s", datastore.ErrNotFound, uidString)
	}
	stmt, names := s.repositoryByUIDTable.Get()
	var row repositoryRow
	if err := s.session.Query(stmt, names).WithContext(ctx).
		BindMap(qb.M{"uid": uid}).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, fmt.Errorf("%w: repository uid %s", datastore.ErrNotFound, uidString)
		}
		return nil, fmt.Errorf("scylla: get repository: %w", err)
	}
	return fromRepositoryRow(&row), nil
}

func (s *scyllaDatastore) ListRepositoriesByNamespace(ctx context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return s.listRepositories(ctx, namespace, page)
}

func (s *scyllaDatastore) ListRepositories(ctx context.Context, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	return s.listRepositories(ctx, "", page)
}

func (s *scyllaDatastore) listRepositories(ctx context.Context, namespace string, page datastore.PageParams) (*datastore.PageResult[datastore.Repository], error) {
	limit := page.Limit()
	items, err := collectRepositoryPage(
		ctx,
		repositoryBucketsForPage(page, time.Now().UTC()),
		page,
		func(ctx context.Context, bucket string, bucketPage datastore.PageParams) ([]repositoryIndexRow, error) {
			statement, args, err := repositoryIndexSelect(namespace, bucket, bucketPage)
			if err != nil {
				return nil, err
			}
			var rows []repositoryIndexRow
			if err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
				return nil, fmt.Errorf("scylla: list repositories bucket %s: %w", bucket, err)
			}
			return rows, nil
		},
		func(ctx context.Context, uid string) (*datastore.Repository, error) {
			return s.GetRepository(ctx, uid)
		},
	)
	if err != nil {
		return nil, err
	}
	result := buildPageResult(items, limit, page)
	result.TotalCount = -1
	return result, nil
}

type repositoryIndexFetcher func(context.Context, string, datastore.PageParams) ([]repositoryIndexRow, error)
type repositoryHydrator func(context.Context, string) (*datastore.Repository, error)

func collectRepositoryPage(
	ctx context.Context,
	buckets []string,
	page datastore.PageParams,
	fetch repositoryIndexFetcher,
	hydrate repositoryHydrator,
) ([]*datastore.Repository, error) {
	limit := page.Limit()
	items := make([]*datastore.Repository, 0, limit+1)
	for _, bucket := range buckets {
		bucketPage := page
		if page.After != "" && !cursorInNamespaceBucket(page.After, bucket) {
			bucketPage.After = ""
		}
		if page.Before != "" && !cursorInNamespaceBucket(page.Before, bucket) {
			bucketPage.Before = ""
		}

		for len(items) < limit+1 {
			rows, err := fetch(ctx, bucket, bucketPage)
			if err != nil {
				return nil, err
			}
			if len(rows) == 0 {
				break
			}
			for _, index := range rows {
				repository, err := hydrate(ctx, index.UID.String())
				if errors.Is(err, datastore.ErrNotFound) {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("scylla: hydrate listed repository: %w", err)
				}
				items = append(items, repository)
				if len(items) >= limit+1 {
					break
				}
			}
			if len(items) >= limit+1 || len(rows) < limit+1 {
				break
			}

			last := rows[len(rows)-1]
			cursor := encodeKeysetCursor(last.CreationTimestamp, last.UID.String())
			if page.Last > 0 {
				bucketPage.Before = cursor
				bucketPage.After = ""
			} else {
				bucketPage.After = cursor
				bucketPage.Before = ""
			}
		}
		if len(items) >= limit+1 {
			break
		}
	}
	if page.Last > 0 {
		for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
			items[left], items[right] = items[right], items[left]
		}
	}
	return items, nil
}

func repositoryBucketsForPage(page datastore.PageParams, now time.Time) []string {
	// Include the adjacent future bucket so accepted clock-skewed timestamps remain pageable.
	return namespaceBucketsForPage(page, now.AddDate(0, 1, 0))
}

func repositoryIndexSelect(namespace, bucket string, page datastore.PageParams) (string, []any, error) {
	tableName := "repositories_by_bucket"
	columns := "bucket, creation_timestamp, uid"
	where := "bucket = ?"
	args := []any{bucket}
	if namespace != "" {
		tableName = "repositories_by_namespace"
		columns = "namespace, bucket, creation_timestamp, uid"
		where = "namespace = ? AND bucket = ?"
		args = []any{namespace, bucket}
	}

	backward := page.Last > 0
	cursor := page.After
	operator := "<"
	order := "DESC"
	if backward {
		cursor = page.Before
		operator = ">"
		order = "ASC"
	}
	if cursor != "" {
		parsed, err := parsePageCursor(cursor)
		if err != nil {
			return "", nil, err
		}
		where += fmt.Sprintf(" AND (creation_timestamp, uid) %s (?, ?)", operator)
		args = append(args, parsed.CreatedAt, mustParseUUID(parsed.ID))
	}
	args = append(args, page.Limit()+1)
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY creation_timestamp %s, uid %s LIMIT ?",
		columns,
		tableName,
		where,
		order,
		order,
	), args, nil
}

func (s *scyllaDatastore) UpdateRepository(ctx context.Context, repository *datastore.Repository, expectedResourceVersion string) error {
	if repository == nil {
		return fmt.Errorf("%w: repository is nil", datastore.ErrInvalidArgument)
	}
	existing, err := s.GetRepository(ctx, repository.UID)
	if err != nil {
		return err
	}
	if err := s.updateRepositoryAuthoritative(ctx, repository, existing.CreationTimestamp, expectedResourceVersion); err != nil {
		return err
	}

	if existing.Namespace != repository.Namespace {
		if err := s.syncRepositoryNamespaceProjection(ctx, existing, repository); err != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "update_repository",
					ResourceKind: "Repository",
					ResourceUID:  repository.UID,
					Projection:   "repositories_by_namespace",
					LookupKey:    repository.Namespace,
					Action:       "roll_forward",
				},
				err,
				fmt.Errorf("authoritative repository version %s is already committed", repository.ResourceVersion),
			)
		}
	}
	return nil
}

func (s *scyllaDatastore) updateRepositoryAuthoritative(
	ctx context.Context,
	repository *datastore.Repository,
	creationTimestamp time.Time,
	expectedResourceVersion string,
) error {
	datastore.NormalizeRepositoryContract(repository)
	row := toRepositoryRow(repository)
	row.CreationTimestamp = creationTimestamp

	const statement = "UPDATE repositories_by_uid SET api_version=?, kind=?, namespace=?, name=?, generation=?, resource_version=?, revision=?, " +
		"creation_timestamp=?, creation_actor=?, update_timestamp=?, update_actor=?, labels=?, annotations=?, owner_references=?, finalizers=?, " +
		"deletion_timestamp=?, repository_id=?, source_path=?, git_commit_sha=?, git_ref=?, spec=?, body=?, status=?, default_branch=?, storage_class=?, " +
		"max_pack_size_bytes=?, max_file_size_bytes=? WHERE uid=? IF resource_version=?"
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(
		row.APIVersion, row.Kind, row.Namespace, row.Name, row.Generation, row.ResourceVersion, row.Revision,
		row.CreationTimestamp, row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations, row.OwnerReferences, row.Finalizers,
		row.DeletionTimestamp, row.RepositoryID, row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status,
		row.DefaultBranch, row.StorageClass, row.MaxPackSizeBytes, row.MaxFileSizeBytes, row.UID, expectedResourceVersion,
	).ExecCASRelease()
	if err != nil {
		return fmt.Errorf("scylla: update repository: %w", err)
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) syncRepositoryNamespaceProjection(
	ctx context.Context,
	existing, repository *datastore.Repository,
) error {
	if existing.Namespace == repository.Namespace {
		return nil
	}
	uid := mustParseUUID(repository.UID)
	bucket := namespaceBucket(existing.CreationTimestamp)
	if err := s.session.Query(
		"INSERT INTO repositories_by_namespace (namespace, bucket, creation_timestamp, uid) VALUES (?, ?, ?, ?)",
		nil,
	).WithContext(ctx).Bind(repository.Namespace, bucket, existing.CreationTimestamp, uid).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: insert repository namespace projection: %w", err)
	}
	if err := s.session.Query(
		"DELETE FROM repositories_by_namespace WHERE namespace=? AND bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(existing.Namespace, bucket, existing.CreationTimestamp, uid).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: delete stale repository namespace projection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) DeleteRepository(ctx context.Context, uidString string) error {
	repository, err := s.GetRepository(ctx, uidString)
	if err != nil {
		return err
	}
	uid := mustParseUUID(uidString)
	bucket := namespaceBucket(repository.CreationTimestamp)
	if err := s.deleteRepositoryProjections(ctx, repository.Namespace, bucket, repository.CreationTimestamp, uid); err != nil {
		return err
	}
	if err := s.session.Query("DELETE FROM repositories_by_uid WHERE uid=?", nil).WithContext(ctx).Bind(uid).ExecRelease(); err != nil {
		if restoreErr := s.insertRepositoryProjections(ctx, repository.Namespace, bucket, repository.CreationTimestamp, uid); restoreErr != nil {
			return datastore.NewRepairRequiredError(
				datastore.MutationStep{
					Operation:    "delete_repository",
					ResourceKind: "Repository",
					ResourceUID:  uidString,
					Projection:   "repositories_by_uid",
					Action:       "delete_authoritative",
				},
				fmt.Errorf("scylla: delete repository: %w", err),
				restoreErr,
			)
		}
		return fmt.Errorf("scylla: delete repository: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) HasRepositories(ctx context.Context, namespace string) (bool, error) {
	result, err := s.ListRepositoriesByNamespace(ctx, namespace, datastore.PageParams{First: 1})
	if err != nil {
		return false, fmt.Errorf("scylla: has repositories: %w", err)
	}
	return len(result.Items) > 0, nil
}

func (s *scyllaDatastore) insertRepositoryProjections(ctx context.Context, namespace, bucket string, createdAt time.Time, uid gocql.UUID) error {
	if err := s.session.Query(
		"INSERT INTO repositories_by_namespace (namespace, bucket, creation_timestamp, uid) VALUES (?, ?, ?, ?)",
		nil,
	).WithContext(ctx).Bind(namespace, bucket, createdAt, uid).ExecRelease(); err != nil {
		return fmt.Errorf("scylla: insert repository namespace projection: %w", err)
	}
	if err := s.session.Query(
		"INSERT INTO repositories_by_bucket (bucket, creation_timestamp, uid) VALUES (?, ?, ?)",
		nil,
	).WithContext(ctx).Bind(bucket, createdAt, uid).ExecRelease(); err != nil {
		_ = s.session.Query(
			"DELETE FROM repositories_by_namespace WHERE namespace=? AND bucket=? AND creation_timestamp=? AND uid=?",
			nil,
		).WithContext(ctx).Bind(namespace, bucket, createdAt, uid).ExecRelease()
		return fmt.Errorf("scylla: insert repository global projection: %w", err)
	}
	return nil
}

func (s *scyllaDatastore) deleteRepositoryProjections(ctx context.Context, namespace, bucket string, createdAt time.Time, uid gocql.UUID) error {
	var cleanupErr error
	if err := s.session.Query(
		"DELETE FROM repositories_by_namespace WHERE namespace=? AND bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(namespace, bucket, createdAt, uid).ExecRelease(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete namespace projection: %w", err))
	}
	if err := s.session.Query(
		"DELETE FROM repositories_by_bucket WHERE bucket=? AND creation_timestamp=? AND uid=?",
		nil,
	).WithContext(ctx).Bind(bucket, createdAt, uid).ExecRelease(); err != nil {
		cleanupErr = errors.Join(cleanupErr, fmt.Errorf("delete global projection: %w", err))
	}
	if cleanupErr != nil {
		return fmt.Errorf("scylla: delete repository projections: %w", cleanupErr)
	}
	return nil
}

func toRepositoryRow(repository *datastore.Repository) *repositoryRow {
	return &repositoryRow{
		APIVersion:        repository.APIVersion,
		Kind:              repository.Kind,
		Namespace:         repository.Namespace,
		UID:               mustParseUUID(repository.UID),
		Name:              repository.Name,
		Generation:        repository.Generation,
		ResourceVersion:   repository.ResourceVersion,
		Revision:          repository.Revision,
		CreationTimestamp: repository.CreationTimestamp,
		CreationActor:     repository.CreationActor,
		UpdateTimestamp:   repository.UpdateTimestamp,
		UpdateActor:       repository.UpdateActor,
		Labels:            repository.Labels,
		Annotations:       repository.Annotations,
		OwnerReferences:   string(repository.OwnerReferences),
		Finalizers:        append([]string(nil), repository.Finalizers...),
		DeletionTimestamp: repository.DeletionTimestamp,
		RepositoryID:      optionalUUID(repository.RepositoryID),
		SourcePath:        repository.SourcePath,
		GitCommitSHA:      repository.GitCommitSHA,
		GitRef:            repository.GitRef,
		Spec:              string(repository.Spec),
		Body:              repository.Body,
		Status:            string(repository.Status),
		DefaultBranch:     repository.DefaultBranch,
		StorageClass:      repository.StorageClass,
		MaxPackSizeBytes:  repository.MaxPackSizeBytes,
		MaxFileSizeBytes:  repository.MaxFileSizeBytes,
	}
}

func fromRepositoryRow(row *repositoryRow) *datastore.Repository {
	repository := &datastore.Repository{
		APIVersion:        row.APIVersion,
		Kind:              row.Kind,
		Namespace:         row.Namespace,
		UID:               row.UID.String(),
		Name:              row.Name,
		Generation:        row.Generation,
		ResourceVersion:   row.ResourceVersion,
		Revision:          row.Revision,
		CreationTimestamp: row.CreationTimestamp,
		CreationActor:     row.CreationActor,
		UpdateTimestamp:   row.UpdateTimestamp,
		UpdateActor:       row.UpdateActor,
		Labels:            row.Labels,
		Annotations:       row.Annotations,
		OwnerReferences:   jsonOrNil(row.OwnerReferences),
		Finalizers:        append([]string(nil), row.Finalizers...),
		DeletionTimestamp: row.DeletionTimestamp,
		RepositoryID:      uuidString(row.RepositoryID),
		SourcePath:        row.SourcePath,
		GitCommitSHA:      row.GitCommitSHA,
		GitRef:            row.GitRef,
		Spec:              jsonOrNil(row.Spec),
		Body:              row.Body,
		Status:            jsonOrNil(row.Status),
		DefaultBranch:     row.DefaultBranch,
		StorageClass:      row.StorageClass,
		MaxPackSizeBytes:  row.MaxPackSizeBytes,
		MaxFileSizeBytes:  row.MaxFileSizeBytes,
	}
	datastore.NormalizeRepositoryContract(repository)
	return repository
}

func optionalUUID(value string) *gocql.UUID {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed := mustParseUUID(value)
	return &parsed
}

func uuidString(value *gocql.UUID) string {
	if value == nil {
		return ""
	}
	return value.String()
}

func reverseRows[T any](rows []T) {
	for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
		rows[left], rows[right] = rows[right], rows[left]
	}
}
