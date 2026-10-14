// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
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
	if f == nil {
		return datastore.ErrInvalidArgument
	}
	return s.catalogMutation(ctx, "File", f.Namespace, f.RepositoryID, f.UID, false, func() error {
		return s.createFile(ctx, f)
	})
}

func (s *scyllaDatastore) createFile(ctx context.Context, f *datastore.File) error {
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
	err = datastore.MeasureCatalogMutationPhase(ctx, datastore.FileMutationLocatorRead, func() error {
		err := s.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"uid": parsed}).GetRelease(&idx)
		if errors.Is(err, gocql.ErrNotFound) {
			return datastore.ErrNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.getFileByKey(ctx, idx.Namespace, idx.CreationTimestamp, idx.UID)
}

const fileSnapshotLocatorBatchLimit = 16

func (s *scyllaDatastore) GetFileSnapshots(ctx context.Context, uids []string) ([]*datastore.File, error) {
	if len(uids) > datastore.FileSnapshotBatchLimit {
		return nil, fmt.Errorf("%w: File snapshots exceed %d IDs", datastore.ErrInvalidArgument, datastore.FileSnapshotBatchLimit)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := make([]*datastore.File, len(uids))
	keys := make([]gocql.UUID, 0, len(uids))
	positions := make(map[gocql.UUID][]int)
	for i, uid := range uids {
		key, err := gocql.ParseUUID(uid)
		if err != nil {
			continue
		}
		if _, seen := positions[key]; !seen {
			keys = append(keys, key)
		}
		positions[key] = append(positions[key], i)
	}
	run := func(count int, read func(int) error) error {
		errs := make([]error, count)
		var next atomic.Int64
		var workers sync.WaitGroup
		for range min(8, count) {
			workers.Go(func() {
				for {
					i := int(next.Add(1) - 1)
					if i >= count || ctx.Err() != nil {
						return
					}
					errs[i] = read(i)
				}
			})
		}
		workers.Wait()
		if err := ctx.Err(); err != nil {
			return err
		}
		return errors.Join(errs...)
	}
	indices := make([]fileIndexRow, 0, len(keys))
	for start := 0; start < len(keys); start += fileSnapshotLocatorBatchLimit {
		end := min(start+fileSnapshotLocatorBatchLimit, len(keys))
		args := make([]any, end-start)
		expected := make(map[gocql.UUID]bool, len(args))
		for i, key := range keys[start:end] {
			args[i], expected[key] = key, true
		}
		stmt := "SELECT " + strings.Join(s.fileByUIDTable.Metadata().Columns, ", ") +
			" FROM files_by_uid WHERE uid IN (" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
		var batch []fileIndexRow
		if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&batch); err != nil {
			return nil, err
		}
		for _, index := range batch {
			if index.Namespace == "" || !expected[index.UID] {
				return nil, fmt.Errorf("%w: invalid File UID locator", datastore.ErrRepairRequired)
			}
			delete(expected, index.UID)
			indices = append(indices, index)
		}
	}
	groups := make(map[string][]fileIndexRow)
	order := make([]string, 0)
	for _, index := range indices {
		if index.Namespace == "" {
			continue
		}
		if _, seen := groups[index.Namespace]; !seen {
			order = append(order, index.Namespace)
		}
		groups[index.Namespace] = append(groups[index.Namespace], index)
	}
	err := run(len(order), func(i int) error {
		namespace := order[i]
		indices := groups[namespace]
		rows, err := s.getFileSnapshotRows(ctx, namespace, indices)
		if err != nil {
			return err
		}
		for _, row := range rows {
			for _, position := range positions[row.UID] {
				results[position] = fileSnapshotFromRow(&row)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

func (s *scyllaDatastore) getFileSnapshotRows(ctx context.Context, namespace string, indices []fileIndexRow) ([]fileRow, error) {
	expected := make(map[gocql.UUID]time.Time, len(indices))
	args := make([]any, 0, 1+2*len(indices))
	args = append(args, namespace)
	for _, index := range indices {
		args = append(args, index.CreationTimestamp, index.UID)
		expected[index.UID] = index.CreationTimestamp
	}
	stmt := "SELECT " + strings.Join(s.fileByNamespaceTable.Metadata().Columns, ", ") +
		" FROM files_by_namespace WHERE namespace=? AND (creation_timestamp,uid) IN (" +
		strings.TrimSuffix(strings.Repeat("(?,?),", len(indices)), ",") + ")"
	var rows []fileRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		created, ok := expected[row.UID]
		if !ok || row.Namespace != namespace || !row.CreationTimestamp.Equal(created) {
			return nil, fmt.Errorf("%w: invalid File snapshot identity", datastore.ErrRepairRequired)
		}
	}
	return rows, nil
}

func fileSnapshotFromRow(row *fileRow) *datastore.File {
	file := fromFileRow(row)
	file.Labels, file.Annotations = maps.Clone(file.Labels), maps.Clone(file.Annotations)
	if file.DeletionTimestamp != nil {
		deleted := *file.DeletionTimestamp
		file.DeletionTimestamp = &deleted
	}
	return file
}

func (s *scyllaDatastore) GetFileSnapshotsByName(ctx context.Context, namespace string, names []string) ([]*datastore.File, error) {
	if namespace == "" || len(names) > datastore.FileNameSnapshotBatchLimit {
		return nil, datastore.ErrInvalidArgument
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	results := make([]*datastore.File, len(names))
	positions := make(map[string][]int, len(names))
	args := []any{namespace}
	for i, name := range names {
		if name == "" {
			return nil, datastore.ErrInvalidArgument
		}
		if _, seen := positions[name]; !seen {
			args = append(args, name)
		}
		positions[name] = append(positions[name], i)
	}
	if len(names) == 0 {
		return results, nil
	}
	stmt := "SELECT " + strings.Join(s.fileByNameTable.Metadata().Columns, ", ") +
		" FROM files_by_name WHERE namespace=? AND name IN (" +
		strings.TrimSuffix(strings.Repeat("?,", len(args)-1), ",") + ")"
	var indices []fileIndexRow
	if err := s.session.Query(stmt, nil).WithContext(ctx).Bind(args...).SelectRelease(&indices); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(indices) == 0 {
		return results, nil
	}
	expected := make(map[gocql.UUID]string, len(indices))
	for _, index := range indices {
		if _, ok := positions[index.Name]; !ok || index.Namespace != namespace {
			return nil, fmt.Errorf("%w: invalid File name locator", datastore.ErrRepairRequired)
		}
		if _, duplicate := expected[index.UID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate File name locator", datastore.ErrRepairRequired)
		}
		expected[index.UID] = index.Name
	}
	rows, err := s.getFileSnapshotRows(ctx, namespace, indices)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Name != expected[row.UID] {
			return nil, fmt.Errorf("%w: invalid File snapshot name", datastore.ErrRepairRequired)
		}
		for _, position := range positions[row.Name] {
			results[position] = fileSnapshotFromRow(&row)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *scyllaDatastore) GetFileByName(ctx context.Context, ns, name string) (*datastore.File, error) {
	stmt, names := s.fileByNameTable.Get()
	var idx fileIndexRow
	if err := s.session.Query(stmt, names).WithContext(ctx).BindMap(qb.M{"namespace": ns, "name": name}).GetRelease(&idx); err != nil {
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
	pq := buildPaginatedSelect(s.fileByNamespaceTable, page, "namespace", ns, namespaceClusterKeys, nil, nil)
	var rows []fileRow
	if err := s.session.Query(pq.Stmt, nil).WithContext(ctx).Bind(pq.Args...).SelectRelease(&rows); err != nil {
		return nil, err
	}
	items := make([]*datastore.File, len(rows))
	for i := range rows {
		items[i] = fromFileRow(&rows[i])
	}
	return buildPageResult(items, page.Limit(), page), nil
}
func (s *scyllaDatastore) UpdateFile(ctx context.Context, f *datastore.File, expectedResourceVersion string) error {
	if f == nil {
		return datastore.ErrInvalidArgument
	}
	var existing *datastore.File
	return s.catalogMutationWithProjection(ctx, "File", f.Namespace, f.RepositoryID, f.UID, false,
		func() (*ProjectionRecord, error) {
			var err error
			if existing == nil {
				existing, err = s.GetFile(ctx, f.UID)
			} else {
				existing, err = s.getFileByKey(ctx, existing.Namespace, existing.CreationTimestamp, mustParseUUID(existing.UID))
			}
			if err != nil {
				return nil, err
			}
			return fileCatalogProjection(existing), nil
		}, func() error {
			return s.updateFile(ctx, f, expectedResourceVersion, existing)
		})
}

func fileCatalogProjection(f *datastore.File) *ProjectionRecord {
	return &ProjectionRecord{
		Table: repositoryResourcesTable, Kind: "File", UID: f.UID,
		RepositoryID: f.RepositoryID, Namespace: f.Namespace, Name: f.Name,
		CreationTimestamp: f.CreationTimestamp, Shard: categoryProductShard(f.UID),
	}
}

func (s *scyllaDatastore) updateFile(ctx context.Context, f *datastore.File, expectedResourceVersion string, old *datastore.File) error {
	if old == nil {
		var err error
		old, err = s.GetFile(ctx, f.UID)
		if err != nil {
			return err
		}
	}
	if old.Namespace != f.Namespace || old.Name != f.Name {
		return datastore.ErrConflict
	}
	if err := datastore.ValidateCatalogUpdateVersion(old.ResourceVersion, expectedResourceVersion, f.ResourceVersion); err != nil {
		return err
	}
	row := toFileRow(f)
	row.CreationTimestamp = old.CreationTimestamp
	if err := s.updateFileAuthoritative(ctx, row, toFileRow(old), expectedResourceVersion); err != nil {
		return err
	}
	return datastore.MeasureCatalogMutationPhase(ctx, datastore.FileMutationOwnerReferenceSync, func() error {
		return s.syncOwnerReferenceDependents(ctx, f.Namespace, f.RepositoryID, "File", f.UID, f.Name, f.ResourceVersion, old.OwnerReferences, f.OwnerReferences)
	})
}
func (s *scyllaDatastore) DeleteFile(ctx context.Context, uid string) error {
	f, err := s.GetFile(ctx, uid)
	if err != nil {
		return err
	}
	return s.deleteFileWithResourceVersion(ctx, f, f.ResourceVersion)
}
func (s *scyllaDatastore) DeleteFileWithResourceVersion(ctx context.Context, uid, expectedResourceVersion string) error {
	f, err := s.GetFile(ctx, uid)
	if err != nil {
		return err
	}
	return s.deleteFileWithResourceVersion(ctx, f, expectedResourceVersion)
}
func (s *scyllaDatastore) deleteFileWithResourceVersion(ctx context.Context, f *datastore.File, expectedResourceVersion string) error {
	return s.catalogMutation(ctx, "File", f.Namespace, f.RepositoryID, f.UID, true, func() error {
		return s.deleteFileFenced(ctx, f, expectedResourceVersion)
	})
}

func (s *scyllaDatastore) deleteFileFenced(ctx context.Context, f *datastore.File, expectedResourceVersion string) error {
	uid := f.UID
	parsed := mustParseUUID(uid)
	if err := s.deleteFileAuthoritative(ctx, toFileRow(f), expectedResourceVersion); err != nil {
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
	if err := s.UpdateFile(ctx, f, p.ResourceVersion); err != nil {
		return nil, err
	}
	return f, nil
}

func fileUpdateValues(row, previous *fileRow) []any {
	values := []any{row.Name, row.APIVersion, row.Kind, row.Generation, row.ResourceVersion, row.Revision, row.CreationActor, row.UpdateTimestamp, row.UpdateActor, row.Labels, row.Annotations, row.OwnerReferences, row.Finalizers, row.DeletionTimestamp, row.RepositoryID, row.SourcePath, row.GitCommitSHA, row.GitRef, row.Spec, row.Body, row.Status}
	before := []any{previous.Name, previous.APIVersion, previous.Kind, previous.Generation, previous.ResourceVersion, previous.Revision, previous.CreationActor, previous.UpdateTimestamp, previous.UpdateActor, previous.Labels, previous.Annotations, previous.OwnerReferences, previous.Finalizers, previous.DeletionTimestamp, previous.RepositoryID, previous.SourcePath, previous.GitCommitSHA, previous.GitRef, previous.Spec, previous.Body, previous.Status}
	for i := range values {
		// JSON reads collapse CQL null and empty strings to logical absence;
		// preserve explicit empty writes that normalize that representation.
		if i == 4 || (i == 11 && row.OwnerReferences == "") ||
			(i == 18 && row.Spec == "") || (i == 20 && row.Status == "") {
			continue
		}
		if reflect.DeepEqual(values[i], before[i]) {
			values[i] = gocql.UnsetValue
		}
	}
	return values
}

const fileUpdateStatement = "UPDATE files_by_namespace SET name=?,api_version=?,kind=?,generation=?,resource_version=?,revision=?,creation_actor=?,update_timestamp=?,update_actor=?,labels=?,annotations=?,owner_references=?,finalizers=?,deletion_timestamp=?,repository_id=?,source_path=?,git_commit_sha=?,git_ref=?,spec=?,body=?,status=? WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?"

func (s *scyllaDatastore) updateFileAuthoritative(ctx context.Context, row, previous *fileRow, expected string) error {
	values := append(fileUpdateValues(row, previous), row.Namespace, row.CreationTimestamp, row.UID, expected)
	return datastore.MeasureCatalogMutationPhase(ctx, datastore.FileMutationAuthoritativeCAS, func() error {
		if scope, _ := ctx.Value(catalogFenceScopeKey{}).(*catalogFenceScope); scope.isActiveFor(s, row.Namespace) {
			return scope.updateFileCAS(ctx, row.UID, values)
		}
		return s.executeFileCAS(&fileCASRequest{ctx: ctx, uid: row.UID, values: values})
	})
}

type fileCASRequest struct {
	ctx    context.Context
	uid    gocql.UUID
	values []any
	reply  chan error
}

const fileCASBatchPayloadLimit = 64 << 10

func (request *fileCASRequest) payloadSize() int {
	size := 32 * len(request.values)
	for _, value := range request.values {
		switch value := value.(type) {
		case string:
			size += len(value)
		case []byte:
			size += len(value)
		}
	}
	return size
}

type fileCASWriter struct {
	mu       sync.Mutex
	closed   bool
	requests chan *fileCASRequest
	done     chan struct{}
	single   func(*fileCASRequest) error
	batch    func(context.Context, []*fileCASRequest) (bool, error)
}

func newFileCASWriter(single func(*fileCASRequest) error, batch func(context.Context, []*fileCASRequest) (bool, error)) *fileCASWriter {
	writer := &fileCASWriter{requests: make(chan *fileCASRequest, 16), done: make(chan struct{}), single: single, batch: batch}
	go writer.run()
	return writer
}

func (writer *fileCASWriter) submit(ctx context.Context, uid gocql.UUID, values []any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	request := &fileCASRequest{ctx: ctx, uid: uid, values: values, reply: make(chan error, 1)}
	writer.mu.Lock()
	if writer.closed {
		writer.mu.Unlock()
		return errors.New("scylla: File conditional writer is closed")
	}
	select {
	case writer.requests <- request:
		writer.mu.Unlock()
	default:
		writer.mu.Unlock()
		return errors.New("scylla: File conditional writer queue exhausted")
	}
	// An accepted request must drain before its reservation can be released.
	return <-request.reply
}

func (writer *fileCASWriter) close() {
	writer.mu.Lock()
	if !writer.closed {
		writer.closed = true
		close(writer.requests)
	}
	writer.mu.Unlock()
	<-writer.done
}

func (writer *fileCASWriter) run() {
	defer close(writer.done)
	var next *fileCASRequest
	for {
		first := next
		next = nil
		if first == nil {
			var open bool
			first, open = <-writer.requests
			if !open {
				return
			}
		}
		if err := first.ctx.Err(); err != nil {
			first.reply <- err
			continue
		}
		group := []*fileCASRequest{first}
		payloadSize := first.payloadSize()
		seen := map[gocql.UUID]bool{first.uid: true}
	drain:
		for len(group) < cap(writer.requests) {
			select {
			case request, open := <-writer.requests:
				if !open {
					break drain
				}
				if err := request.ctx.Err(); err != nil {
					request.reply <- err
					continue
				}
				requestSize := request.payloadSize()
				if seen[request.uid] || (len(group) >= 4 && payloadSize+requestSize > fileCASBatchPayloadLimit) {
					next = request
					break drain
				}
				seen[request.uid] = true
				payloadSize += requestSize
				group = append(group, request)
			default:
				break drain
			}
		}
		writer.execute(group)
	}
}

func (writer *fileCASWriter) execute(group []*fileCASRequest) {
	if len(group) == 1 {
		group[0].reply <- writer.single(group[0])
		return
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(group[0].ctx))
	defer cancel()
	var live atomic.Int64
	live.Store(int64(len(group)))
	for _, request := range group {
		if request.ctx.Err() != nil {
			if live.Add(-1) == 0 {
				cancel()
			}
			continue
		}
		stop := context.AfterFunc(request.ctx, func() {
			if live.Add(-1) == 0 {
				cancel()
			}
		})
		defer stop()
	}
	applied, err := writer.batch(ctx, group)
	for _, request := range group {
		switch {
		case err != nil:
			request.reply <- err
		case applied:
			request.reply <- nil
		default:
			// Only a definite all-or-none miss permits independent fallback.
			request.reply <- writer.single(request)
		}
	}
}

func (s *scyllaDatastore) executeFileCAS(request *fileCASRequest) error {
	if err := request.ctx.Err(); err != nil {
		return err
	}
	applied, err := s.session.Query(fileUpdateStatement, nil).WithContext(request.ctx).Bind(request.values...).ExecCASRelease()
	if err != nil {
		return err
	}
	if !applied {
		return datastore.ErrConflict
	}
	return nil
}

func (s *scyllaDatastore) executeFileCASBatch(ctx context.Context, group []*fileCASRequest) (bool, error) {
	phase := datastore.FileMutationConditionalBatch2 + datastore.CatalogMutationPhase(len(group)-2)
	var applied bool
	err := datastore.MeasureCatalogMutationPhase(ctx, phase, func() error {
		batch := s.session.Batch(gocql.LoggedBatch).WithContext(ctx).RetryPolicy(&gocql.SimpleRetryPolicy{})
		for _, request := range group {
			batch.Query(fileUpdateStatement, request.values...)
		}
		var err error
		var iter *gocql.Iter
		applied, iter, err = s.session.MapExecuteBatchCAS(batch, make(map[string]any))
		if iter != nil {
			err = errors.Join(err, iter.Close())
		}
		return err
	})
	return applied, err
}

func (s *scyllaDatastore) deleteFileAuthoritative(ctx context.Context, row *fileRow, expected string) error {
	return s.deleteAuthoritative(ctx, "DELETE FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=? IF resource_version=?", row.Namespace, row.CreationTimestamp, row.UID, expected)
}
