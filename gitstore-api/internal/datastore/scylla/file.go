// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/scylladb/gocqlx/v3/qb"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
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
	s.takeFileMutationSnapshot(ctx, f.Namespace, f.UID)
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

const fileSnapshotReadBatchLimit = 16

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
	readSnapshots := func(namespace string, indices []fileIndexRow) error {
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
	}
	batches := make([][]fileIndexRow, (len(keys)+fileSnapshotReadBatchLimit-1)/fileSnapshotReadBatchLimit)
	readLocator := func(batch int) (string, []fileIndexRow, error) {
		start := batch * fileSnapshotReadBatchLimit
		end := min(start+fileSnapshotReadBatchLimit, len(keys))
		args := make([]any, end-start)
		expected := make(map[gocql.UUID]bool, len(args))
		for i, key := range keys[start:end] {
			args[i], expected[key] = key, true
		}
		stmt := "SELECT " + strings.Join(s.fileByUIDTable.Metadata().Columns, ", ") +
			" FROM files_by_uid WHERE uid IN (" + strings.TrimSuffix(strings.Repeat("?,", len(args)), ",") + ")"
		queryCtx, finish := startCQLExchange(ctx, otel.Tracer("gitstore/datastore/scylla"),
			attribute.String("db.collection.name", "files_by_uid"))
		err := s.session.Query(stmt, nil).WithContext(queryCtx).Bind(args...).SelectRelease(&batches[batch])
		finish(err)
		if err != nil {
			return "", nil, err
		}
		fullNamespaceBatch := len(batches[batch]) == fileSnapshotReadBatchLimit
		for _, index := range batches[batch] {
			if index.Namespace == "" || !expected[index.UID] {
				return "", nil, fmt.Errorf("%w: invalid File UID locator", datastore.ErrRepairRequired)
			}
			delete(expected, index.UID)
			if index.Namespace != batches[batch][0].Namespace {
				fullNamespaceBatch = false
			}
		}
		if fullNamespaceBatch {
			indices := batches[batch]
			batches[batch] = nil
			return indices[0].Namespace, indices, nil
		}
		return "", nil, nil
	}
	locatorErrors, fullErrors := make([]error, len(batches)), make([]error, len(batches))
	var authoritativeBatches [][]fileIndexRow
	var authoritativeErrors []error
	var nextLocator, nextAuthoritative atomic.Int64
	var grouped sync.Once
	var locators, workers sync.WaitGroup
	locators.Add(len(batches))
	for range min(8, len(keys)) {
		workers.Go(func() {
			for {
				batch := int(nextLocator.Add(1) - 1)
				if batch >= len(batches) {
					break
				}
				if err := ctx.Err(); err != nil {
					locatorErrors[batch] = err
					locators.Done()
					continue
				}
				namespace, indices, err := readLocator(batch)
				locatorErrors[batch] = err
				// Publish locators before any unrelated authoritative read can block.
				locators.Done()
				if err == nil && len(indices) != 0 {
					fullErrors[batch] = readSnapshots(namespace, indices)
				}
			}
			locators.Wait()
			if ctx.Err() != nil {
				return
			}
			grouped.Do(func() {
				if errors.Join(locatorErrors...) != nil {
					return
				}
				groups := make(map[string][]fileIndexRow)
				order := make([]string, 0)
				for _, indices := range batches {
					for _, index := range indices {
						if _, seen := groups[index.Namespace]; !seen {
							order = append(order, index.Namespace)
						}
						groups[index.Namespace] = append(groups[index.Namespace], index)
					}
				}
				for _, namespace := range order {
					indices := groups[namespace]
					for start := 0; start < len(indices); start += fileSnapshotReadBatchLimit {
						authoritativeBatches = append(authoritativeBatches, indices[start:min(start+fileSnapshotReadBatchLimit, len(indices))])
					}
				}
				authoritativeErrors = make([]error, len(authoritativeBatches))
			})
			for ctx.Err() == nil {
				i := int(nextAuthoritative.Add(1) - 1)
				if i >= len(authoritativeBatches) {
					return
				}
				indices := authoritativeBatches[i]
				authoritativeErrors[i] = readSnapshots(indices[0].Namespace, indices)
			}
		})
	}
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := errors.Join(errors.Join(locatorErrors...), errors.Join(fullErrors...), errors.Join(authoritativeErrors...)); err != nil {
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
	queryCtx, finish := startCQLExchange(ctx, otel.Tracer("gitstore/datastore/scylla"),
		attribute.String("db.collection.name", "files_by_namespace"))
	err := s.session.Query(stmt, nil).WithContext(queryCtx).Bind(args...).SelectRelease(&rows)
	finish(err)
	if err != nil {
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
	queryCtx, finish := startCQLExchange(ctx, otel.Tracer("gitstore/datastore/scylla"),
		attribute.String("db.collection.name", "files_by_name"))
	err := s.session.Query(stmt, nil).WithContext(queryCtx).Bind(args...).SelectRelease(&indices)
	finish(err)
	if err != nil {
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
			if existing != nil {
				return s.getFileCatalogProjectionByKey(ctx, existing.Namespace, existing.CreationTimestamp, mustParseUUID(existing.UID))
			}
			var err error
			var found bool
			existing, err, found = s.takeFileMutationSnapshot(ctx, f.Namespace, f.UID)
			if !found {
				existing, err = s.GetFile(ctx, f.UID)
			}
			if err != nil {
				return nil, err
			}
			return fileCatalogProjection(existing), nil
		}, func() error {
			return s.updateFile(ctx, f, expectedResourceVersion, existing)
		})
}

type fileMutationSnapshot struct {
	file  *datastore.File
	err   error
	ready bool
}

type fileMutationSnapshotRead struct {
	uids   []string
	batch  *fileMutationSnapshotReadBatch
	offset int
}

type fileMutationSnapshotReadBatch struct {
	ctx   context.Context
	uids  []string
	once  sync.Once
	files []*datastore.File
	err   error
}

func prepareFileMutationSnapshots(ctx context.Context, group []*fileMutationRequest) {
	var selected []*fileMutationSnapshotRead
	for i, request := range group {
		if request.snapshots == nil || len(request.snapshots.uids) < 2 || request.ctx.Err() != nil {
			continue
		}
		overlaps := false
		for _, predecessor := range group[:i] {
			if fileMutationRequestsOverlap(request, predecessor) {
				overlaps = true
				break
			}
			if predecessor.snapshots != nil {
				for _, uid := range request.snapshots.uids {
					if slices.Contains(predecessor.snapshots.uids, uid) {
						overlaps = true
						break
					}
				}
			}
			if overlaps {
				break
			}
		}
		if !overlaps {
			selected = append(selected, request.snapshots)
		}
	}
	if len(selected) < 2 {
		return
	}
	batch := &fileMutationSnapshotReadBatch{ctx: ctx}
	for _, read := range selected {
		read.offset = len(batch.uids)
		batch.uids = append(batch.uids, read.uids...)
		read.batch = batch
	}
}

func (read *fileMutationSnapshotRead) load(ctx context.Context, get func(context.Context, []string) ([]*datastore.File, error)) ([]*datastore.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if read.batch == nil {
		return get(ctx, read.uids)
	}
	batch := read.batch
	// Only declared disjoint callbacks share this read. Cancellation follows
	// the existing all-callers cohort context; selected callers still drain it.
	batch.once.Do(func() { batch.files, batch.err = get(batch.ctx, batch.uids) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if batch.err != nil {
		return nil, batch.err
	}
	return batch.files[read.offset : read.offset+len(read.uids)], nil
}

func (read *fileMutationSnapshotRead) loadWithParent(
	ctx context.Context,
	get func(context.Context, []string) ([]*datastore.File, error),
	checkParent func(context.Context) error,
) ([]*datastore.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parentDone := make(chan error, 1)
	go func() { parentDone <- checkParent(ctx) }()
	files, readErr := read.load(ctx, get)
	parentErr := <-parentDone
	if err := errors.Join(parentErr, readErr); err != nil {
		return nil, err
	}
	return files, nil
}

func (s *scyllaDatastore) WithFileMutationBatchSnapshots(ctx context.Context, namespace, repositoryID string, names, existingUIDs []string, operation func(context.Context) error) error {
	if len(existingUIDs) > 4 || len(existingUIDs) > len(names) {
		return fmt.Errorf("%w: File mutation snapshots exceed the callback bounds", datastore.ErrInvalidArgument)
	}
	uids := append([]string(nil), existingUIDs...)
	read := &fileMutationSnapshotRead{uids: uids}
	return s.withFileMutationBatch(ctx, namespace, repositoryID, names, read, func(batchCtx context.Context) error {
		if len(uids) < 2 {
			return operation(batchCtx)
		}
		scope, _ := batchCtx.Value(catalogFenceScopeKey{}).(*catalogFenceScope)
		if !scope.isActiveFor(s, namespace) {
			return errors.New("scylla: File mutation snapshots require an active reservation")
		}
		slots := make([]*fileMutationSnapshot, len(uids))
		// Consuming an unfinished slot also prevents its later read result
		// from becoming a stale write fact after a nested mutation.
		scope.snapshotMu.Lock()
		if scope.snapshots == nil {
			scope.snapshots = make(map[string]*fileMutationSnapshot)
		}
		for i, uid := range uids {
			if _, present := scope.snapshots[uid]; present || len(scope.snapshots) < 16 {
				slots[i] = &fileMutationSnapshot{}
				scope.snapshots[uid] = slots[i]
			}
		}
		scope.snapshotMu.Unlock()
		var files []*datastore.File
		err := datastore.MeasureCatalogMutationPhase(batchCtx, datastore.FileMutationSnapshotRead, func() error {
			var err error
			files, err = read.loadWithParent(batchCtx, s.GetFileSnapshots, func(parentCtx context.Context) error {
				return s.catalogParentsActive(parentCtx, namespace, repositoryID)
			})
			return err
		})
		scope.snapshotMu.Lock()
		for i, uid := range uids {
			if slot := slots[i]; slot != nil && scope.snapshots[uid] == slot {
				slot.err, slot.ready = err, true
				if err == nil {
					slot.file = files[i]
				}
			}
		}
		scope.snapshotMu.Unlock()
		return operation(batchCtx)
	})
}

func (s *scyllaDatastore) takeFileMutationSnapshot(ctx context.Context, namespace, uid string) (*datastore.File, error, bool) {
	scope, _ := ctx.Value(catalogFenceScopeKey{}).(*catalogFenceScope)
	if !scope.isActiveFor(s, namespace) {
		return nil, nil, false
	}
	scope.snapshotMu.Lock()
	defer scope.snapshotMu.Unlock()
	snapshot := scope.snapshots[uid]
	delete(scope.snapshots, uid)
	if snapshot == nil || !snapshot.ready {
		return nil, nil, false
	}
	if snapshot.err != nil {
		return nil, snapshot.err, true
	}
	if snapshot.file == nil {
		return nil, datastore.ErrNotFound, true
	}
	return snapshot.file, nil, true
}

func (s *scyllaDatastore) getFileCatalogProjectionByKey(ctx context.Context, namespace string, created time.Time, uid gocql.UUID) (*ProjectionRecord, error) {
	key := fileProjectionKey{created: created, uid: uid}
	if scope, _ := ctx.Value(catalogFenceScopeKey{}).(*catalogFenceScope); scope.isActiveFor(s, namespace) {
		return scope.readFileProjection(ctx, key)
	}
	return s.getFileCatalogProjectionPoint(ctx, namespace, key)
}

func (s *scyllaDatastore) getFileCatalogProjectionPoint(ctx context.Context, namespace string, key fileProjectionKey) (*ProjectionRecord, error) {
	const statement = "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace WHERE namespace=? AND creation_timestamp=? AND uid=?"
	var row fileRow
	if err := s.session.Query(statement, nil).WithContext(ctx).Bind(namespace, key.created, key.uid).GetRelease(&row); err != nil {
		if errors.Is(err, gocql.ErrNotFound) {
			return nil, datastore.ErrNotFound
		}
		return nil, err
	}
	return fileCatalogProjection(fromFileRow(&row)), nil
}

func (s *scyllaDatastore) getFileCatalogProjectionRows(ctx context.Context, namespace string, keys []fileProjectionKey) ([]*ProjectionRecord, error) {
	if len(keys) == 1 {
		record, err := s.getFileCatalogProjectionPoint(ctx, namespace, keys[0])
		if errors.Is(err, datastore.ErrNotFound) {
			return []*ProjectionRecord{nil}, nil
		}
		return []*ProjectionRecord{record}, err
	}
	if len(keys) == 0 || len(keys) > fileSnapshotReadBatchLimit {
		return nil, fmt.Errorf("%w: File projection reads require 1-%d keys", datastore.ErrInvalidArgument, fileSnapshotReadBatchLimit)
	}
	args := make([]any, 1, 1+2*len(keys))
	args[0] = namespace
	for _, key := range keys {
		args = append(args, key.created, key.uid)
	}
	statement := "SELECT namespace, creation_timestamp, uid, name, repository_id FROM files_by_namespace " +
		"WHERE namespace=? AND (creation_timestamp,uid) IN (" +
		strings.TrimSuffix(strings.Repeat("(?,?),", len(keys)), ",") + ")"
	var rows []fileRow
	if err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).SelectRelease(&rows); err != nil {
		return nil, err
	}
	results := make([]*ProjectionRecord, len(keys))
	seen := make(map[gocql.UUID]bool, len(rows))
	for _, row := range rows {
		matched := false
		for i, key := range keys {
			if row.UID == key.uid && row.CreationTimestamp.Equal(key.created) && row.Namespace == namespace {
				results[i] = fileCatalogProjection(fromFileRow(&row))
				matched = true
			}
		}
		if !matched || seen[row.UID] {
			return nil, fmt.Errorf("%w: invalid File projection identity", datastore.ErrRepairRequired)
		}
		seen[row.UID] = true
	}
	return results, nil
}

type fileProjectionKey struct {
	created time.Time
	uid     gocql.UUID
}

type fileProjectionResult struct {
	record *ProjectionRecord
	err    error
}

type fileProjectionRequest struct {
	ctx       context.Context
	key       fileProjectionKey
	reply     chan fileProjectionResult
	queueDone func(error)
}

func (request *fileProjectionRequest) finishQueue(err error) {
	if request.queueDone != nil {
		request.queueDone(err)
		request.queueDone = nil
	}
}

type fileProjectionReader struct {
	mu       sync.Mutex
	closed   bool
	requests chan *fileProjectionRequest
	done     chan struct{}
	read     func(context.Context, []fileProjectionKey) ([]*ProjectionRecord, error)
}

func newFileProjectionReader(read func(context.Context, []fileProjectionKey) ([]*ProjectionRecord, error)) *fileProjectionReader {
	reader := &fileProjectionReader{
		requests: make(chan *fileProjectionRequest, fileSnapshotReadBatchLimit),
		done:     make(chan struct{}), read: read,
	}
	go reader.run()
	return reader
}

func (reader *fileProjectionReader) submit(ctx context.Context, key fileProjectionKey) (*ProjectionRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request := &fileProjectionRequest{
		ctx: ctx, key: key, reply: make(chan fileProjectionResult, 1),
		queueDone: datastore.StartCatalogMutationPhase(ctx, datastore.FileProjectionReadQueue),
	}
	reader.mu.Lock()
	if reader.closed {
		reader.mu.Unlock()
		err := errors.New("scylla: File projection reader is closed")
		request.finishQueue(err)
		return nil, err
	}
	select {
	case reader.requests <- request:
		reader.mu.Unlock()
	default:
		reader.mu.Unlock()
		err := errors.New("scylla: File projection reader queue exhausted")
		request.finishQueue(err)
		return nil, err
	}
	// Accepted reads must join physical work before their fence can be released.
	result := <-request.reply
	return result.record, result.err
}

func (reader *fileProjectionReader) close() {
	reader.mu.Lock()
	if !reader.closed {
		reader.closed = true
		close(reader.requests)
	}
	reader.mu.Unlock()
	<-reader.done
}

func (reader *fileProjectionReader) run() {
	defer close(reader.done)
	for first := range reader.requests {
		group := []*fileProjectionRequest{first}
	drain:
		for len(group) < cap(reader.requests) {
			select {
			case request, open := <-reader.requests:
				if !open {
					break drain
				}
				group = append(group, request)
			default:
				break drain
			}
		}
		reader.execute(group)
	}
}

func (reader *fileProjectionReader) execute(group []*fileProjectionRequest) {
	liveRequests := make([]*fileProjectionRequest, 0, len(group))
	for _, request := range group {
		if err := request.ctx.Err(); err != nil {
			request.finishQueue(err)
			request.reply <- fileProjectionResult{err: err}
		} else {
			request.finishQueue(nil)
			liveRequests = append(liveRequests, request)
		}
	}
	if len(liveRequests) == 0 {
		return
	}
	keys := make([]fileProjectionKey, len(liveRequests))
	contexts := make([]context.Context, len(liveRequests))
	for i, request := range liveRequests {
		keys[i], contexts[i] = request.key, request.ctx
	}
	ctx, cancel := fileRequestGroupContext(contexts)
	defer cancel()
	var records []*ProjectionRecord
	phase := datastore.FileProjectionReadBatch1 + datastore.CatalogMutationPhase(len(keys)-1)
	err := datastore.MeasureCatalogMutationPhase(ctx, phase, func() error {
		var err error
		records, err = reader.read(ctx, keys)
		if err == nil && len(records) != len(keys) {
			return fmt.Errorf("%w: incomplete File projection read results", datastore.ErrRepairRequired)
		}
		return err
	})
	for i, request := range liveRequests {
		result := fileProjectionResult{err: err}
		switch {
		case request.ctx.Err() != nil:
			result.err = request.ctx.Err()
		case err != nil:
		case records[i] == nil:
			result.err = datastore.ErrNotFound
		default:
			result.record = records[i]
		}
		request.reply <- result
	}
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
	s.takeFileMutationSnapshot(ctx, f.Namespace, f.UID)
	return s.catalogMutation(ctx, "File", f.Namespace, f.RepositoryID, f.UID, true, func() error {
		return s.deleteFileFenced(ctx, f, expectedResourceVersion)
	})
}

func (s *scyllaDatastore) deleteFileFenced(ctx context.Context, f *datastore.File, expectedResourceVersion string) error {
	uid := f.UID
	parsed := mustParseUUID(uid)
	return s.mutations.executeConditionalDelete(ctx, expectedResourceVersion,
		mutationAction{
			Step: catalogueStep("delete", "File", uid, "files_by_namespace", uid, "delete-authoritative"),
			Apply: func(ctx context.Context) error {
				return s.deleteFileAuthoritative(ctx, toFileRow(f), expectedResourceVersion)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "File", uid, ownerReferenceDependentsTable, uid, "delete-owner-references"),
			Apply: func(ctx context.Context) error {
				return s.syncOwnerReferenceDependents(ctx, f.Namespace, f.RepositoryID, "File", uid, f.Name, f.ResourceVersion, f.OwnerReferences, nil)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "File", uid, "files_by_name", f.Namespace+"/"+f.Name, "delete-name"),
			Apply: func(ctx context.Context) error {
				return s.releaseName(ctx, "files_by_name", f.Namespace, f.Name, parsed)
			},
		},
		mutationAction{
			Step: catalogueStep("delete", "File", uid, "files_by_uid", uid, "delete-uid"),
			Apply: func(ctx context.Context) error {
				return s.releaseUID(ctx, "files_by_uid", f.Namespace, parsed, f.CreationTimestamp)
			},
		},
	)
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
	ctx       context.Context
	uid       gocql.UUID
	values    []any
	reply     chan error
	queueDone func(error)
}

func (request *fileCASRequest) finishQueue(err error) {
	if request.queueDone != nil {
		request.queueDone(err)
		request.queueDone = nil
	}
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
	request := &fileCASRequest{
		ctx: ctx, uid: uid, values: values, reply: make(chan error, 1),
		queueDone: datastore.StartCatalogMutationPhase(ctx, datastore.FileMutationConditionalQueue),
	}
	writer.mu.Lock()
	if writer.closed {
		writer.mu.Unlock()
		err := errors.New("scylla: File conditional writer is closed")
		request.finishQueue(err)
		return err
	}
	select {
	case writer.requests <- request:
		writer.mu.Unlock()
	default:
		writer.mu.Unlock()
		err := errors.New("scylla: File conditional writer queue exhausted")
		request.finishQueue(err)
		return err
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
			first.finishQueue(err)
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
					request.finishQueue(err)
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
	for _, request := range group {
		request.finishQueue(nil)
	}
	if len(group) == 1 {
		group[0].reply <- writer.single(group[0])
		return
	}
	contexts := make([]context.Context, len(group))
	for i, request := range group {
		contexts[i] = request.ctx
	}
	ctx, cancel := fileRequestGroupContext(contexts)
	defer cancel()
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

func fileRequestGroupContext(contexts []context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(contexts[0]))
	var live atomic.Int64
	live.Store(int64(len(contexts)))
	stops := make([]func() bool, 0, len(contexts))
	for _, requestCtx := range contexts {
		if requestCtx.Err() != nil {
			if live.Add(-1) == 0 {
				cancel()
			}
			continue
		}
		stop := context.AfterFunc(requestCtx, func() {
			if live.Add(-1) == 0 {
				cancel()
			}
		})
		stops = append(stops, stop)
	}
	return ctx, func() {
		for _, stop := range stops {
			stop()
		}
		cancel()
	}
}

func (s *scyllaDatastore) executeFileCAS(request *fileCASRequest) error {
	if err := request.ctx.Err(); err != nil {
		return err
	}
	return datastore.MeasureCatalogMutationPhase(request.ctx, datastore.FileMutationConditionalSingle, func() (err error) {
		ctx, finish := startCQLExchange(request.ctx, otel.Tracer("gitstore/datastore/scylla"))
		defer func() { finish(err) }()
		query := s.session.Query(fileUpdateStatement, nil).WithContext(ctx).Bind(request.values...)
		if tracer := sampleCQLServerTrace(ctx); tracer != nil {
			query.Trace(tracer)
		}
		applied, err := query.ExecCASRelease()
		if err != nil {
			return err
		}
		if !applied {
			return datastore.ErrConflict
		}
		return nil
	})
}

func (s *scyllaDatastore) executeFileCASBatch(ctx context.Context, group []*fileCASRequest) (bool, error) {
	phase := datastore.FileMutationConditionalBatch2 + datastore.CatalogMutationPhase(len(group)-2)
	var applied bool
	err := datastore.MeasureCatalogMutationPhase(ctx, phase, func() (err error) {
		queryCtx, finish := startCQLExchange(ctx, otel.Tracer("gitstore/datastore/scylla"))
		defer func() { finish(err) }()
		batch := s.session.Batch(gocql.LoggedBatch).WithContext(queryCtx).RetryPolicy(&gocql.SimpleRetryPolicy{})
		if tracer := sampleCQLServerTrace(queryCtx); tracer != nil {
			batch.Trace(tracer)
		}
		for _, request := range group {
			batch.Query(fileUpdateStatement, request.values...)
		}
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
