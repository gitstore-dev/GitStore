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

type fileRow struct {
	Namespace         string            `db:"namespace"`
	CreationTimestamp time.Time         `db:"creation_timestamp"`
	UID               gocql.UUID        `db:"uid"`
	Name              string            `db:"name"`
	APIVersion        string            `db:"api_version"`
	Kind              string            `db:"kind"`
	Generation        int64             `db:"generation"`
	ResourceVersion   string            `db:"resource_version"`
	Revision          string            `db:"revision"`
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
}

type fileIndexRow struct {
	Namespace         string     `db:"namespace"`
	Name              string     `db:"name"`
	UID               gocql.UUID `db:"uid"`
	CreationTimestamp time.Time  `db:"creation_timestamp"`
}

func toFileRow(f *datastore.File) *fileRow {
	return &fileRow{Namespace: f.Namespace, CreationTimestamp: f.CreationTimestamp, UID: mustParseUUID(f.UID), Name: f.Name, APIVersion: f.APIVersion, Kind: f.Kind, Generation: f.Generation, ResourceVersion: f.ResourceVersion, Revision: f.Revision, CreationActor: f.CreationActor, UpdateTimestamp: f.UpdateTimestamp, UpdateActor: f.UpdateActor, Labels: f.Labels, Annotations: f.Annotations, OwnerReferences: string(f.OwnerReferences), Finalizers: append([]string(nil), f.Finalizers...), DeletionTimestamp: f.DeletionTimestamp, RepositoryID: optionalUUID(f.RepositoryID), SourcePath: f.SourcePath, GitCommitSHA: f.GitCommitSHA, GitRef: f.GitRef, Spec: string(f.Spec), Body: f.Body, Status: string(f.Status)}
}
func fromFileRow(r *fileRow) *datastore.File {
	return &datastore.File{UID: r.UID.String(), Namespace: r.Namespace, Name: r.Name, APIVersion: r.APIVersion, Kind: r.Kind, Generation: r.Generation, ResourceVersion: r.ResourceVersion, CreationTimestamp: r.CreationTimestamp, CreationActor: r.CreationActor, UpdateTimestamp: r.UpdateTimestamp, UpdateActor: r.UpdateActor, Revision: r.Revision, Labels: r.Labels, Annotations: r.Annotations, OwnerReferences: jsonOrNil(r.OwnerReferences), Finalizers: append([]string(nil), r.Finalizers...), DeletionTimestamp: r.DeletionTimestamp, RepositoryID: uuidString(r.RepositoryID), SourcePath: r.SourcePath, GitCommitSHA: r.GitCommitSHA, GitRef: r.GitRef, Spec: jsonOrNil(r.Spec), Body: r.Body, Status: jsonOrNil(r.Status)}
}

func (s *scyllaDatastore) CreateFile(ctx context.Context, f *datastore.File) error {
	if f == nil || f.UID == "" || f.Namespace == "" || f.Name == "" {
		return fmt.Errorf("%w: file uid, namespace, and name are required", datastore.ErrInvalidArgument)
	}
	uid, err := gocql.ParseUUID(f.UID)
	if err != nil {
		return fmt.Errorf("%w: invalid file uid", datastore.ErrInvalidArgument)
	}
	if f.CreationTimestamp.IsZero() {
		f.CreationTimestamp = time.Now().UTC().Truncate(time.Millisecond)
	}
	row := toFileRow(f)
	applied, err := s.insertAuthoritative(ctx, s.fileByNamespaceTable, row)
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrAlreadyExists
	}
	if err := s.reserveName(ctx, "File", "files_by_name", row.Namespace, row.Name, uid, row.CreationTimestamp); err != nil {
		_ = s.deleteFileAuthoritative(ctx, row, row.ResourceVersion)
		return err
	}
	if err := s.reserveUID(ctx, "File", "files_by_uid", row.Namespace, uid, row.CreationTimestamp); err != nil {
		_ = s.releaseName(ctx, "files_by_name", row.Namespace, row.Name, uid)
		_ = s.deleteFileAuthoritative(ctx, row, row.ResourceVersion)
		return err
	}
	if err := s.syncOwnerReferenceDependents(ctx, f.Namespace, f.RepositoryID, "File", f.UID, f.Name, f.ResourceVersion, nil, f.OwnerReferences); err != nil {
		_ = s.releaseUID(ctx, "files_by_uid", row.Namespace, uid, row.CreationTimestamp)
		_ = s.releaseName(ctx, "files_by_name", row.Namespace, row.Name, uid)
		_ = s.deleteFileAuthoritative(ctx, row, row.ResourceVersion)
		return err
	}
	return nil
}

func (s *scyllaDatastore) GetFile(ctx context.Context, uid string) (*datastore.File, error) {
	parsed, err := gocql.ParseUUID(uid)
	if err != nil {
		return nil, datastore.ErrNotFound
	}
	stmt, names := s.fileByUIDTable.Get()
	var idx fileIndexRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"uid": parsed}).GetRelease(&idx); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return s.getFileByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
}
func (s *scyllaDatastore) GetFileByName(ctx context.Context, ns, name string) (*datastore.File, error) {
	stmt, names := s.fileByNameTable.Get()
	var idx fileIndexRow
	if err := s.session.Query(stmt, names).BindMap(qb.M{"namespace": ns, "name": name}).GetRelease(&idx); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return s.getFileByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
}
func (s *scyllaDatastore) getFileByKey(ctx context.Context, ns string, created time.Time, uid gocql.UUID) (*datastore.File, error) {
	cols := strings.Join(s.fileByNamespaceTable.Metadata().Columns, ", ")
	stmt := fmt.Sprintf("SELECT %s FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?", cols)
	var row fileRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(ns, created, uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return fromFileRow(&row), nil
}
func (s *scyllaDatastore) ListFiles(ctx context.Context, ns string, page datastore.PageParams) (*datastore.PageResult[datastore.File], error) {
	stmt := fmt.Sprintf("SELECT %s FROM files_by_namespace WHERE namespace=?", strings.Join(s.fileByNamespaceTable.Metadata().Columns, ", "))
	var rows []fileRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(ns).SelectRelease(&rows); err != nil {
		return nil, err
	}
	items := make([]*datastore.File, len(rows))
	for i := range rows {
		items[i] = fromFileRow(&rows[i])
	}
	return &datastore.PageResult[datastore.File]{Items: items, TotalCount: int32(len(items))}, nil
}
func (s *scyllaDatastore) UpdateFile(ctx context.Context, f *datastore.File) error {
	old, err := s.GetFile(ctx, f.UID)
	if err != nil {
		return err
	}
	if old.Namespace != f.Namespace || old.Name != f.Name {
		return datastore.ErrConflict
	}
	if err := validateResourceVersionTransition(old.ResourceVersion, f.ResourceVersion); err != nil {
		return err
	}
	row := toFileRow(f)
	row.CreationTimestamp = old.CreationTimestamp
	if err := s.updateFileAuthoritative(ctx, row, old.ResourceVersion); err != nil {
		return err
	}
	return s.syncOwnerReferenceDependents(ctx, f.Namespace, f.RepositoryID, "File", f.UID, f.Name, f.ResourceVersion, old.OwnerReferences, f.OwnerReferences)
}
func (s *scyllaDatastore) DeleteFile(ctx context.Context, uid string) error {
	f, err := s.GetFile(ctx, uid)
	if err != nil {
		return err
	}
	parsed := mustParseUUID(uid)
	if err := s.deleteFileAuthoritative(ctx, toFileRow(f), f.ResourceVersion); err != nil {
		return err
	}
	if err := s.syncOwnerReferenceDependents(ctx, f.Namespace, f.RepositoryID, "File", f.UID, f.Name, f.ResourceVersion, f.OwnerReferences, nil); err != nil {
		return err
	}
	_ = s.releaseName(ctx, "files_by_name", f.Namespace, f.Name, parsed)
	_ = s.releaseUID(ctx, "files_by_uid", f.Namespace, parsed, f.CreationTimestamp)
	return nil
}
func (s *scyllaDatastore) UpdateFileStatus(ctx context.Context, ns, name string, p datastore.FileStatusPatch) (*datastore.File, error) {
	f, err := s.GetFileByName(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	if err := datastore.ApplyFileStatusPatch(f, p); err != nil {
		return nil, err
	}
	if err := s.UpdateFile(ctx, f); err != nil {
		return nil, err
	}
	return f, nil
}

func (s *scyllaDatastore) updateFileAuthoritative(ctx context.Context, row *fileRow, expected string) error {
	stmt := "UPDATE files_by_namespace SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?,creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?,repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"
	applied, err := s.session.Query(stmt, nil).WithContext(ctx).Bind(row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision, row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations, row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID, row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status, row.Namespace, row.CreationTimestamp, row.UID, expected).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}
func (s *scyllaDatastore) deleteFileAuthoritative(ctx context.Context, row *fileRow, expected string) error {
	return s.deleteAuthoritative(ctx, "DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?", row.Namespace, row.CreationTimestamp, row.UID, expected)
}
