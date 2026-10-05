// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// FilesystemStore persists one JSON checkpoint file per kind under Dir.
// Save writes to a temp file in Dir and renames it into place, so a crash
// during write never leaves a partially-written checkpoint readable.
type FilesystemStore struct {
	Dir string
}

// NewFilesystemStore creates dir (if absent) and returns a FilesystemStore
// rooted at it.
func NewFilesystemStore(dir string) (*FilesystemStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("checkpoint: failed to create dir %q: %w", dir, err)
	}
	return &FilesystemStore{Dir: dir}, nil
}

func (s *FilesystemStore) path(kind string) string {
	return filepath.Join(s.Dir, kind+".checkpoint.json")
}

// Load reads and unmarshals the checkpoint file for kind. Any error —
// missing file, permission error, or malformed JSON — is returned as-is;
// callers treat every error identically.
func (s *FilesystemStore) Load(_ context.Context, kind string) (Record, error) {
	data, err := os.ReadFile(s.path(kind))
	if err != nil {
		return Record{}, fmt.Errorf("checkpoint: failed to read %q: %w", kind, err)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, fmt.Errorf("checkpoint: failed to parse %q: %w", kind, err)
	}
	if err := validateRecord(kind, rec); err != nil {
		return Record{}, err
	}
	return rec, nil
}

// Save atomically writes rec to its kind's checkpoint file: write to a temp
// file in Dir, fsync, close, then rename into place.
func (s *FilesystemStore) Save(_ context.Context, rec Record) error {
	if err := validateRecord(rec.Kind, rec); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("checkpoint: failed to marshal %q: %w", rec.Kind, err)
	}

	tmp, err := os.CreateTemp(s.Dir, rec.Kind+".checkpoint.*.tmp")
	if err != nil {
		return fmt.Errorf("checkpoint: failed to create temp file for %q: %w", rec.Kind, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: failed to write temp file for %q: %w", rec.Kind, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("checkpoint: failed to sync temp file for %q: %w", rec.Kind, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("checkpoint: failed to close temp file for %q: %w", rec.Kind, err)
	}

	if err := os.Rename(tmpName, s.path(rec.Kind)); err != nil {
		return fmt.Errorf("checkpoint: failed to rename checkpoint for %q: %w", rec.Kind, err)
	}
	return nil
}

func validateRecord(kind string, rec Record) error {
	if rec.Kind == "" || rec.Kind != kind {
		return fmt.Errorf("checkpoint: invalid kind %q for %q", rec.Kind, kind)
	}
	if rec.ResourceVersion == "" {
		return fmt.Errorf("checkpoint: resourceVersion is empty for %q", kind)
	}
	var items []json.RawMessage
	if len(rec.Snapshot) == 0 || json.Unmarshal(rec.Snapshot, &items) != nil || items == nil {
		return fmt.Errorf("checkpoint: snapshot is missing or invalid for %q", kind)
	}
	for _, key := range rec.ReplayKeys {
		if key.Kind != kind {
			return fmt.Errorf("checkpoint: replay key kind %q does not match %q", key.Kind, kind)
		}
	}
	return nil
}

const (
	DiskPageItems = 256
	DiskPageBytes = 4 << 20
	DiskItemBytes = 1 << 20
)

// DiskItem stores only the controller projection, never credential material.
// Indexes identify relation groups; Related names the owners to reconcile when
// those memberships change.
type DiskItem struct {
	Key            types.WorkItemKey
	Version        string
	Value          json.RawMessage
	Indexes        []string
	Related        []types.WorkItemKey
	RelatedVersion string
}

type DiskWork struct {
	Key    types.WorkItemKey
	Token  uint64
	Cursor string
}

type DiskSchedule struct {
	Token         uint64
	Due           int64
	Priority      byte
	Attempts      int
	LastError     string
	QuarantinedAt time.Time
}

type DiskPoison struct {
	Key types.WorkItemKey
	DiskSchedule
}

type DiskCounts struct {
	Pending uint64
	Poison  uint64
}

type diskBatch struct {
	leveldb.Batch
	counts map[string]int64
}

func newDiskBatch() *diskBatch {
	return &diskBatch{counts: make(map[string]int64)}
}

func (b *diskBatch) count(generation uint64, name string, delta int64) {
	b.counts[string(diskPrefix(generation, name))] += delta
}

// DiskStore bounds engine buffers and every application read/write batch.
// Each kind has its own locked directory on the existing checkpoint volume.
// A snapshot is published by one atomic generation-pointer/cursor write.
type DiskStore struct {
	mu             sync.RWMutex
	db             *leveldb.DB
	kind           string
	counts         atomic.Pointer[DiskCounts]
	longOperations atomic.Int64
	lastWrite      atomic.Int64
	writeFailures  atomic.Uint64
}

var diskSync = &opt.WriteOptions{Sync: true}
var diskScan = &opt.ReadOptions{DontFillCache: true}
var ErrSnapshotInProgress = errors.New("checkpoint: snapshot recovery is in progress")

func OpenDiskStore(dir, kind string) (*DiskStore, error) {
	if kind == "" {
		return nil, errors.New("checkpoint: disk store kind is empty")
	}
	for _, c := range kind {
		if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return nil, errors.New("checkpoint: invalid disk store kind")
		}
	}
	path := filepath.Join(dir, kind+".disk-v2")
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, fmt.Errorf("checkpoint: create disk store: %w", err)
	}
	db, err := leveldb.OpenFile(path, &opt.Options{
		WriteBuffer: 4 << 20, BlockCacheCapacity: 8 << 20,
		OpenFilesCacheCapacity: 32, CompactionTableSize: 2 << 20,
		BlockCacheEvictRemoved: true,
	})
	if err != nil {
		return nil, fmt.Errorf("checkpoint: open disk store: %w", err)
	}
	s := &DiskStore{db: db, kind: kind}
	version, err := db.Get([]byte("schema"), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		it := db.NewIterator(nil, diskScan)
		hasData := it.First()
		err = it.Error()
		it.Release()
		if err == nil && hasData {
			err = errors.New("disk checkpoint schema is missing from a nonempty store")
		}
		if err == nil {
			err = db.Put([]byte("schema"), []byte("2"), diskSync)
		}
	} else if err == nil && string(version) != "2" {
		err = errors.New("unsupported disk checkpoint schema")
	}
	if err != nil {
		return nil, errors.Join(fmt.Errorf("checkpoint: disk schema: %w", err), db.Close())
	}
	if err := s.refreshCounts(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

func (s *DiskStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db.Close()
}

func diskNumber(n uint64) []byte {
	return binary.BigEndian.AppendUint64(nil, n)
}

func (s *DiskStore) number(name string) (uint64, error) {
	value, err := s.db.Get([]byte(name), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(value) != 8 {
		return 0, fmt.Errorf("checkpoint: invalid %s metadata", name)
	}
	return binary.BigEndian.Uint64(value), nil
}

func diskPrefix(generation uint64, family string) []byte {
	return append(append([]byte("g/"), diskNumber(generation)...), []byte("/"+family)...)
}

func diskLogical(key types.WorkItemKey) (string, error) {
	if key.Kind == "" || key.Name == "" || len(key.Kind)+len(key.Namespace)+len(key.Name) > 1024 ||
		strings.ContainsRune(key.Kind+key.Namespace+key.Name, 0) {
		return "", errors.New("checkpoint: invalid resource key")
	}
	return key.Namespace + "\x00" + key.Name, nil
}

func (s *DiskStore) logical(key types.WorkItemKey) (string, error) {
	if key.Kind != s.kind {
		return "", errors.New("checkpoint: resource kind does not match disk store")
	}
	return diskLogical(key)
}

func diskItem(item DiskItem) ([]byte, error) {
	if item.Version == "" || len(item.Version) > 1024 || len(item.RelatedVersion) > 1024 || len(item.Value) > DiskItemBytes ||
		!json.Valid(item.Value) || len(item.Indexes) > 32 || len(item.Related) > 32 {
		return nil, errors.New("checkpoint: invalid or oversized resource projection")
	}
	seen := make(map[string]struct{}, len(item.Indexes))
	for _, index := range item.Indexes {
		if index == "" || len(index) > 1024 {
			return nil, errors.New("checkpoint: invalid relation index")
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, errors.New("checkpoint: duplicate relation index")
		}
		seen[index] = struct{}{}
	}
	for _, key := range item.Related {
		if _, err := diskLogical(key); err != nil {
			return nil, err
		}
	}
	return json.Marshal(item)
}

func diskIndexPrefix(generation uint64, index string) []byte {
	return diskPrefix(generation, "i/"+base64.RawURLEncoding.EncodeToString([]byte(index))+"/")
}

func (s *DiskStore) item(generation uint64, logical string) (DiskItem, bool, error) {
	var item DiskItem
	data, err := s.db.Get(append(diskPrefix(generation, "d/"), logical...), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	if len(data) > 2*DiskItemBytes {
		return item, false, errors.New("checkpoint: oversized stored projection")
	}
	if err := json.Unmarshal(data, &item); err != nil {
		return item, false, fmt.Errorf("checkpoint: invalid stored projection: %w", err)
	}
	actual, err := s.logical(item.Key)
	if err != nil || actual != logical || item.Version == "" || len(item.Value) > DiskItemBytes || !json.Valid(item.Value) {
		return item, false, errors.New("checkpoint: inconsistent stored projection")
	}
	return item, true, nil
}

func (s *DiskStore) Active(ctx context.Context) (uint64, string, error) {
	if err := s.readLock(ctx); err != nil {
		return 0, "", err
	}
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, "", err
	}
	generation, err := s.number("active")
	if err != nil || generation == 0 {
		return generation, "", err
	}
	cursor, err := s.db.Get([]byte("cursor"), nil)
	if err == nil && len(cursor) == 0 {
		err = errors.New("checkpoint: active disk snapshot has no cursor")
	}
	return generation, string(cursor), err
}

// BeginSnapshot leaves the old generation readable, but fences watch writes.
// Interrupted staging/retired generations are reclaimed in bounded batches.
func (s *DiskStore) BeginSnapshot(ctx context.Context) (uint64, error) {
	s.longOperations.Add(1)
	defer s.longOperations.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.number("active")
	if err != nil {
		return 0, err
	}
	next, err := s.number("generation")
	if err != nil {
		return 0, err
	}
	var retired [2]uint64
	for i, name := range []string{"staging", "garbage"} {
		generation, err := s.number(name)
		if err != nil {
			return 0, err
		}
		if generation != 0 && generation == active || generation > next || active > next {
			return 0, errors.New("checkpoint: inconsistent generation metadata")
		}
		retired[i] = generation
	}
	for _, generation := range retired {
		if generation != 0 {
			if err := s.deleteGeneration(ctx, generation); err != nil {
				return 0, err
			}
		}
	}
	next++
	if next == 0 {
		return 0, errors.New("checkpoint: generation overflow")
	}
	batch := newDiskBatch()
	batch.Put([]byte("generation"), diskNumber(next))
	batch.Put([]byte("staging"), diskNumber(next))
	batch.Delete([]byte("garbage"))
	if err := s.write(ctx, batch); err != nil {
		return 0, err
	}
	return next, nil
}

func (s *DiskStore) write(ctx context.Context, batch *diskBatch) (writeErr error) {
	defer func() {
		if writeErr != nil {
			s.writeFailures.Add(1)
		} else {
			s.lastWrite.Store(time.Now().Unix())
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	for name, delta := range batch.counts {
		current, err := s.number(name)
		if err != nil {
			return err
		}
		if delta < 0 && current < uint64(-delta) || delta > 0 && current > ^uint64(0)-uint64(delta) {
			return errors.New("checkpoint: invalid work count")
		}
		batch.Put([]byte(name), diskNumber(uint64(int64(current)+delta)))
	}
	if len(batch.Dump()) > DiskPageBytes {
		return errors.New("checkpoint: disk batch exceeds byte bound")
	}
	if err := s.db.Write(&batch.Batch, diskSync); err != nil {
		return fmt.Errorf("checkpoint: durable disk write: %w", err)
	}
	return s.refreshCounts()
}

func (s *DiskStore) refreshCounts() error {
	active, err := s.number("active")
	if err != nil {
		return err
	}
	pending, err := s.number(string(diskPrefix(active, "pending-count")))
	if err != nil {
		return err
	}
	poison, err := s.number(string(diskPrefix(active, "poison-count")))
	if err != nil {
		return err
	}
	s.counts.Store(&DiskCounts{Pending: pending, Poison: poison})
	return nil
}

// Counts is the last committed snapshot, so health never waits behind a long
// generation merge or garbage collection.
func (s *DiskStore) Counts() DiskCounts {
	return *s.counts.Load()
}

func (s *DiskStore) PersistenceStats() (lastWrite int64, failures uint64) {
	return s.lastWrite.Load(), s.writeFailures.Load()
}

func diskRelated(batch *diskBatch, generation uint64, keys []types.WorkItemKey, token uint64) {
	for _, key := range keys {
		logical := key.Kind + "\x00" + key.Namespace + "\x00" + key.Name
		batch.Put(append(diskPrefix(generation, "r/"), logical...), diskNumber(token))
	}
}

func (s *DiskStore) put(batch *diskBatch, generation uint64, item DiskItem, data []byte, token uint64, deleted, enqueue bool) error {
	logical, err := s.logical(item.Key)
	if err != nil {
		return err
	}
	old, exists, err := s.item(generation, logical)
	if err != nil {
		return err
	}
	for _, index := range old.Indexes {
		batch.Delete(append(diskIndexPrefix(generation, index), logical...))
		batch.count(generation, "ic/"+base64.RawURLEncoding.EncodeToString([]byte(index)), -1)
	}
	if deleted {
		batch.Delete(append(diskPrefix(generation, "d/"), logical...))
		diskRelated(batch, generation, old.Related, token)
	} else {
		batch.Put(append(diskPrefix(generation, "d/"), logical...), data)
		for _, index := range item.Indexes {
			batch.Put(append(diskIndexPrefix(generation, index), logical...), nil)
			batch.count(generation, "ic/"+base64.RawURLEncoding.EncodeToString([]byte(index)), 1)
		}
		if !exists || old.RelatedVersion != item.RelatedVersion || !slices.Equal(old.Indexes, item.Indexes) || !slices.Equal(old.Related, item.Related) {
			diskRelated(batch, generation, old.Related, token)
			diskRelated(batch, generation, item.Related, token)
		}
	}
	if enqueue {
		priority := byte(0)
		staging, err := s.number("staging")
		if err != nil {
			return err
		}
		if staging == generation {
			priority = 1
		}
		return s.queueWork(batch, generation, logical, DiskSchedule{Token: token, Priority: priority})
	}
	return nil
}

func diskReadyKey(generation uint64, logical string, state DiskSchedule) []byte {
	prefix := diskPrefix(generation, fmt.Sprintf("q/%d/", state.Priority))
	prefix = append(prefix, diskNumber(uint64(state.Due))...)
	prefix = append(prefix, diskNumber(state.Token)...)
	return append(prefix, logical...)
}

func (s *DiskStore) schedule(generation uint64, logical string, token uint64) (DiskSchedule, error) {
	state := DiskSchedule{Token: token, Priority: 1}
	data, err := s.db.Get(append(diskPrefix(generation, "s/"), logical...), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("checkpoint: invalid work schedule: %w", err)
	}
	if state.Token != token || state.Priority > 1 || state.Due < 0 {
		return state, errors.New("checkpoint: inconsistent work schedule")
	}
	return state, nil
}

func (s *DiskStore) forgetWork(batch *diskBatch, generation uint64, logical string) error {
	token, err := s.token(diskPrefix(generation, "w/"), logical)
	if err != nil || token == 0 {
		return err
	}
	state, err := s.schedule(generation, logical, token)
	if err != nil {
		return err
	}
	batch.Delete(diskReadyKey(generation, logical, state))
	batch.Delete(append(diskPrefix(generation, "w/"), logical...))
	batch.Delete(append(diskPrefix(generation, "s/"), logical...))
	batch.Delete(append(diskPrefix(generation, "p/"), logical...))
	batch.count(generation, "pending-count", -1)
	if !state.QuarantinedAt.IsZero() {
		batch.count(generation, "poison-count", -1)
	}
	return nil
}

func (s *DiskStore) queueWork(batch *diskBatch, generation uint64, logical string, state DiskSchedule) error {
	if state.Priority == 0 && state.Due == 0 {
		state.Due = time.Now().UnixMilli()
	}
	if err := s.forgetWork(batch, generation, logical); err != nil {
		return err
	}
	batch.Put(append(diskPrefix(generation, "w/"), logical...), diskNumber(state.Token))
	batch.count(generation, "pending-count", 1)
	if state.Priority != 1 || state.Due != 0 || !state.QuarantinedAt.IsZero() {
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		batch.Put(append(diskPrefix(generation, "s/"), logical...), data)
	}
	if state.QuarantinedAt.IsZero() {
		batch.Put(diskReadyKey(generation, logical, state), nil)
	} else {
		data, err := json.Marshal(state)
		if err != nil {
			return err
		}
		batch.Put(append(diskPrefix(generation, "p/"), logical...), data)
		batch.count(generation, "poison-count", 1)
	}
	return nil
}

func (s *DiskStore) PutSnapshotPage(ctx context.Context, generation uint64, items []DiskItem) error {
	if len(items) == 0 || len(items) > DiskPageItems {
		return errors.New("checkpoint: invalid snapshot page size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil {
		return err
	}
	if staging == 0 || staging != generation {
		return errors.New("checkpoint: snapshot generation is not writable")
	}
	token, err := s.number("sequence")
	if err != nil {
		return err
	}
	batch := newDiskBatch()
	seen := make(map[types.WorkItemKey]struct{}, len(items))
	for _, item := range items {
		if _, duplicate := seen[item.Key]; duplicate {
			return errors.New("checkpoint: duplicate resource in snapshot page")
		}
		seen[item.Key] = struct{}{}
		data, err := diskItem(item)
		if err != nil {
			return err
		}
		token++
		if token == 0 {
			return errors.New("checkpoint: work token overflow")
		}
		if err := s.put(batch, generation, item, data, token, false, true); err != nil {
			return err
		}
		if len(batch.Dump()) > DiskPageBytes {
			return errors.New("checkpoint: snapshot page exceeds byte bound")
		}
	}
	batch.Put([]byte("sequence"), diskNumber(token))
	return s.write(ctx, batch)
}

// scan copies only a bounded page and closes its engine iterator before return.
func (s *DiskStore) scan(ctx context.Context, prefix []byte, after string, limit int) ([][2][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > DiskPageItems || len(after) > 4096 {
		return nil, errors.New("checkpoint: invalid disk scan limit")
	}
	it := s.db.NewIterator(util.BytesPrefix(prefix), diskScan)
	defer it.Release()
	var page [][2][]byte
	size := 0
	for ok := it.Seek(append(bytes.Clone(prefix), after...)); ok; ok = it.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := it.Key()[len(prefix):]
		if after != "" && string(key) == after {
			continue
		}
		n := len(key) + len(it.Value())
		if n > DiskPageBytes {
			return nil, errors.New("checkpoint: stored row exceeds page byte bound")
		}
		if len(page) == limit || size+n > DiskPageBytes {
			break
		}
		page = append(page, [2][]byte{bytes.Clone(key), bytes.Clone(it.Value())})
		size += n
	}
	return page, it.Error()
}

func (s *DiskStore) token(prefix []byte, logical string) (uint64, error) {
	value, err := s.db.Get(append(prefix, logical...), nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if len(value) != 8 {
		return 0, errors.New("checkpoint: invalid work token")
	}
	return binary.BigEndian.Uint64(value), nil
}

// FinishSnapshot preserves unfinished/deleted work before atomically publishing.
// Watch writes remain fenced throughout; acknowledgements use unique tokens.
func (s *DiskStore) FinishSnapshot(ctx context.Context, generation uint64, cursor string, observe ...func(int64)) error {
	if cursor == "" || len(cursor) > 4096 {
		return errors.New("checkpoint: invalid snapshot cursor")
	}
	s.longOperations.Add(1)
	defer s.longOperations.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil {
		return err
	}
	if staging != generation || staging == 0 {
		return errors.New("checkpoint: snapshot is not staged")
	}
	old, err := s.number("active")
	if err != nil {
		return err
	}
	sequence, err := s.number("sequence")
	if err != nil {
		return err
	}
	var pages int64
	for _, family := range []string{"d/", "w/", "r/"} {
		after := ""
		for old != 0 || family != "d/" {
			page, err := s.scan(ctx, diskPrefix(old, family), after, DiskPageItems)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			batch := newDiskBatch()
			for _, row := range page {
				logical := string(row[0])
				if family == "r/" {
					sequence++
					if sequence == 0 {
						return errors.New("checkpoint: work token overflow")
					}
					batch.Put(append(diskPrefix(generation, "r/"), logical...), diskNumber(sequence))
					continue
				}
				if family == "d/" {
					current, exists, err := s.item(generation, logical)
					if err != nil {
						return err
					}
					var prior DiskItem
					if err := json.Unmarshal(row[1], &prior); err != nil {
						return err
					}
					if !exists {
						sequence++
						if sequence == 0 {
							return errors.New("checkpoint: work token overflow")
						}
						if err := s.queueWork(batch, generation, logical, DiskSchedule{Token: sequence, Priority: 1}); err != nil {
							return err
						}
						diskRelated(batch, generation, prior.Related, sequence)
					} else if current.RelatedVersion != prior.RelatedVersion || !slices.Equal(current.Indexes, prior.Indexes) || !slices.Equal(current.Related, prior.Related) {
						sequence++
						if sequence == 0 {
							return errors.New("checkpoint: work token overflow")
						}
						diskRelated(batch, generation, prior.Related, sequence)
						diskRelated(batch, generation, current.Related, sequence)
					} else if current.Version == prior.Version && bytes.Equal(current.Value, prior.Value) {
						token, err := s.token(diskPrefix(old, "w/"), logical)
						if err != nil {
							return err
						}
						if token == 0 {
							if err := s.forgetWork(batch, generation, logical); err != nil {
								return err
							}
						}
					}
				} else {
					sequence++
					if sequence == 0 {
						return errors.New("checkpoint: work token overflow")
					}
					if len(row[1]) != 8 {
						return errors.New("checkpoint: invalid old work token")
					}
					state, err := s.schedule(old, logical, binary.BigEndian.Uint64(row[1]))
					if err != nil {
						return err
					}
					prior, _, err := s.item(old, logical)
					if err != nil {
						return err
					}
					current, _, err := s.item(generation, logical)
					if err != nil {
						return err
					}
					if prior.Version != current.Version || prior.RelatedVersion != current.RelatedVersion || !bytes.Equal(prior.Value, current.Value) ||
						!slices.Equal(prior.Indexes, current.Indexes) || !slices.Equal(prior.Related, current.Related) {
						state = DiskSchedule{Priority: 1}
					}
					state.Token = sequence
					if err := s.queueWork(batch, generation, logical, state); err != nil {
						return err
					}
				}
			}
			batch.Put([]byte("sequence"), diskNumber(sequence))
			if err := s.write(ctx, batch); err != nil {
				return err
			}
			pages++
			for _, report := range observe {
				report(pages)
			}
			after = string(page[len(page)-1][0])
		}
	}
	batch := newDiskBatch()
	batch.Put([]byte("active"), diskNumber(generation))
	batch.Put([]byte("cursor"), []byte(cursor))
	batch.Put([]byte("garbage"), diskNumber(old))
	batch.Delete([]byte("staging"))
	return s.write(ctx, batch)
}

func (s *DiskStore) Apply(ctx context.Context, item DiskItem, deleted, enqueue bool, cursor string) error {
	if cursor == "" || len(cursor) > 4096 {
		return errors.New("checkpoint: invalid watch cursor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil {
		return err
	}
	if staging != 0 {
		return ErrSnapshotInProgress
	}
	active, err := s.number("active")
	if err != nil || active == 0 {
		return errors.Join(err, errors.New("checkpoint: no active disk snapshot"))
	}
	sequence, err := s.number("sequence")
	if err != nil {
		return err
	}
	sequence++
	if sequence == 0 {
		return errors.New("checkpoint: work token overflow")
	}
	var data []byte
	if !deleted {
		data, err = diskItem(item)
		if err != nil {
			return err
		}
	}
	batch := newDiskBatch()
	if err := s.put(batch, active, item, data, sequence, deleted, enqueue); err != nil {
		return err
	}
	batch.Put([]byte("sequence"), diskNumber(sequence))
	batch.Put([]byte("cursor"), []byte(cursor))
	return s.write(ctx, batch)
}

func (s *DiskStore) AdvanceCursor(ctx context.Context, cursor string) error {
	if cursor == "" || len(cursor) > 4096 {
		return errors.New("checkpoint: invalid watch cursor")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil {
		return err
	}
	if staging != 0 {
		return ErrSnapshotInProgress
	}
	active, err := s.number("active")
	if err != nil {
		return err
	}
	if active == 0 {
		return errors.New("checkpoint: no active disk snapshot")
	}
	batch := newDiskBatch()
	batch.Put([]byte("cursor"), []byte(cursor))
	return s.write(ctx, batch)
}

func (s *DiskStore) CollectGarbage(ctx context.Context, observe ...func(int64)) error {
	s.longOperations.Add(1)
	defer s.longOperations.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	active, err := s.number("active")
	if err != nil {
		return err
	}

	garbage, err := s.number("garbage")
	if err != nil {
		return err
	}
	if active == 0 {
		return errors.New("checkpoint: cannot collect before snapshot publication")
	}
	if garbage == active {
		return errors.New("checkpoint: refusing to collect the active generation")
	}
	if err := s.deleteGeneration(ctx, garbage, observe...); err != nil {
		return err
	}
	batch := newDiskBatch()
	batch.Delete([]byte("garbage"))
	return s.write(ctx, batch)
}

func (s *DiskStore) DiscardStaging(ctx context.Context) error {
	s.longOperations.Add(1)
	defer s.longOperations.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil || staging == 0 {
		return err
	}
	active, err := s.number("active")
	if err != nil {
		return err
	}
	if staging == active {
		return errors.New("checkpoint: refusing to discard active generation")
	}
	if err := s.deleteGeneration(ctx, staging); err != nil {
		return err
	}
	batch := newDiskBatch()
	batch.Delete([]byte("staging"))
	return s.write(ctx, batch)
}

func (s *DiskStore) DataPage(ctx context.Context, after string, limit int) ([]DiskItem, string, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, "", err
	}
	defer s.mu.RUnlock()
	active, err := s.number("active")
	if err != nil {
		return nil, "", err
	}
	if active == 0 {
		return nil, "", errors.New("checkpoint: no active disk snapshot")
	}
	page, err := s.scan(ctx, diskPrefix(active, "d/"), after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]DiskItem, 0, len(page))
	for _, row := range page {
		var item DiskItem
		if err := json.Unmarshal(row[1], &item); err != nil {
			return nil, "", fmt.Errorf("checkpoint: invalid stored projection: %w", err)
		}
		logical, err := s.logical(item.Key)
		if err != nil || logical != string(row[0]) {
			return nil, "", errors.New("checkpoint: stored projection key mismatch")
		}
		items = append(items, item)
		after = string(row[0])
	}
	return items, after, nil
}

func (s *DiskStore) Read(ctx context.Context, key types.WorkItemKey) (DiskItem, bool, uint64, error) {
	if err := s.readLock(ctx); err != nil {
		return DiskItem{}, false, 0, err
	}
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return DiskItem{}, false, 0, err
	}
	logical, err := s.logical(key)
	if err != nil {
		return DiskItem{}, false, 0, err
	}
	active, err := s.number("active")
	if err != nil {
		return DiskItem{}, false, 0, err
	}
	if active == 0 {
		return DiskItem{}, false, 0, errors.New("checkpoint: no active disk snapshot")
	}
	item, exists, err := s.item(active, logical)
	if err != nil {
		return item, exists, 0, err
	}
	token, err := s.token(diskPrefix(active, "w/"), logical)
	return item, exists, token, err
}

func (s *DiskStore) Pending(ctx context.Context, after string, limit int) ([]DiskWork, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	active, err := s.number("active")
	if err != nil {
		return nil, err
	}
	page, err := s.scan(ctx, diskPrefix(active, "w/"), after, limit)
	if err != nil {
		return nil, err
	}
	work := make([]DiskWork, 0, len(page))
	for _, row := range page {
		parts := strings.SplitN(string(row[0]), "\x00", 2)
		if len(parts) != 2 || len(row[1]) != 8 {
			return nil, errors.New("checkpoint: invalid pending work")
		}
		work = append(work, DiskWork{
			Key:   types.WorkItemKey{Kind: s.kind, Namespace: parts[0], Name: parts[1]},
			Token: binary.BigEndian.Uint64(row[1]), Cursor: string(row[0]),
		})
	}
	return work, nil
}

func (s *DiskStore) Acknowledge(ctx context.Context, key types.WorkItemKey, token uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == 0 {
		return false, errors.New("checkpoint: missing acknowledgement token")
	}
	logical, err := s.logical(key)
	if err != nil {
		return false, err
	}
	active, err := s.number("active")
	if err != nil {
		return false, err
	}
	current, err := s.token(diskPrefix(active, "w/"), logical)
	if err != nil || current != token {
		return false, err
	}
	batch := newDiskBatch()
	if err := s.forgetWork(batch, active, logical); err != nil {
		return false, err
	}
	err = s.write(ctx, batch)
	return err == nil, err
}

// Ready prioritizes live changes over snapshot replay without materializing the
// backlog. Reserved keys remain durable until their captured tokens complete.
func (s *DiskStore) Ready(ctx context.Context, limit int) ([]DiskWork, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	if limit < 1 || limit > DiskPageItems {
		return nil, errors.New("checkpoint: invalid ready page size")
	}
	active, err := s.number("active")
	if err != nil {
		return nil, err
	}
	var work []DiskWork
	now := uint64(time.Now().UnixMilli())
	for priority := range 2 {
		page, err := s.scan(ctx, diskPrefix(active, fmt.Sprintf("q/%d/", priority)), "", limit-len(work))
		if err != nil {
			return nil, err
		}
		for _, row := range page {
			if len(row[0]) < 17 {
				return nil, errors.New("checkpoint: invalid ready index")
			}
			if binary.BigEndian.Uint64(row[0][:8]) > now {
				break
			}
			logical := string(row[0][16:])
			parts := strings.SplitN(logical, "\x00", 2)
			if len(parts) != 2 {
				return nil, errors.New("checkpoint: invalid ready key")
			}
			token := binary.BigEndian.Uint64(row[0][8:16])
			work = append(work, DiskWork{
				Key:   types.WorkItemKey{Kind: s.kind, Namespace: parts[0], Name: parts[1]},
				Token: token, Cursor: logical,
			})
		}
		if len(work) == limit {
			break
		}
	}
	return work, nil
}

func (s *DiskStore) WorkCounts(ctx context.Context) (pending, poison uint64, err error) {
	if err = s.readLock(ctx); err != nil {
		return
	}
	defer s.mu.RUnlock()
	if err = ctx.Err(); err != nil {
		return
	}
	active, err := s.number("active")
	if err != nil {
		return 0, 0, err
	}
	pending, err = s.number(string(diskPrefix(active, "pending-count")))
	if err != nil {
		return
	}
	poison, err = s.number(string(diskPrefix(active, "poison-count")))
	return
}

func (s *DiskStore) Enqueue(ctx context.Context, key types.WorkItemKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	logical, err := s.logical(key)
	if err != nil {
		return err
	}
	active, err := s.number("active")
	if err != nil {
		return err
	}
	token, err := s.number("sequence")
	if err != nil {
		return err
	}
	token++
	if token == 0 {
		return errors.New("checkpoint: work token overflow")
	}
	batch := newDiskBatch()
	if err := s.queueWork(batch, active, logical, DiskSchedule{Token: token}); err != nil {
		return err
	}
	batch.Put([]byte("sequence"), diskNumber(token))
	return s.write(ctx, batch)
}

// DeferWork preserves a captured obligation on disk instead of allocating a
// timer or quarantine entry per resource. A nonempty failure quarantines it.
func (s *DiskStore) DeferWork(ctx context.Context, key types.WorkItemKey, token uint64, delay time.Duration, failure string, attempts int) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token == 0 || delay < 0 || attempts < 0 {
		return false, errors.New("checkpoint: invalid deferred work")
	}
	logical, err := s.logical(key)
	if err != nil {
		return false, err
	}
	active, err := s.number("active")
	if err != nil {
		return false, err
	}
	current, err := s.token(diskPrefix(active, "w/"), logical)
	if err != nil || current != token {
		return false, err
	}
	state := DiskSchedule{Token: token, Due: time.Now().Add(delay).UnixMilli(), Attempts: attempts}
	if failure != "" {
		state.LastError = failure
		if len(state.LastError) > 2048 {
			state.LastError = state.LastError[:2048] + " [truncated]"
		}
		state.QuarantinedAt = time.Now().UTC()
	}
	batch := newDiskBatch()
	if err := s.queueWork(batch, active, logical, state); err != nil {
		return false, err
	}
	err = s.write(ctx, batch)
	return err == nil, err
}

func (s *DiskStore) WorkSchedule(ctx context.Context, key types.WorkItemKey) (DiskSchedule, error) {
	if err := s.readLock(ctx); err != nil {
		return DiskSchedule{}, err
	}
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return DiskSchedule{}, err
	}
	logical, err := s.logical(key)
	if err != nil {
		return DiskSchedule{}, err
	}
	active, err := s.number("active")
	if err != nil {
		return DiskSchedule{}, err
	}
	token, err := s.token(diskPrefix(active, "w/"), logical)
	if err != nil {
		return DiskSchedule{}, err
	}
	return s.schedule(active, logical, token)
}

func (s *DiskStore) PoisonPage(ctx context.Context, after string, limit int) ([]DiskPoison, string, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, "", err
	}
	defer s.mu.RUnlock()
	active, err := s.number("active")
	if err != nil {
		return nil, "", err
	}
	page, err := s.scan(ctx, diskPrefix(active, "p/"), after, limit)
	if err != nil {
		return nil, "", err
	}
	items := make([]DiskPoison, 0, len(page))
	for _, row := range page {
		parts := strings.SplitN(string(row[0]), "\x00", 2)
		if len(parts) != 2 {
			return nil, "", errors.New("checkpoint: invalid quarantined key")
		}
		item := DiskPoison{Key: types.WorkItemKey{Kind: s.kind, Namespace: parts[0], Name: parts[1]}}
		if err := json.Unmarshal(row[1], &item.DiskSchedule); err != nil {
			return nil, "", fmt.Errorf("checkpoint: invalid quarantine record: %w", err)
		}
		items = append(items, item)
		after = string(row[0])
	}
	return items, after, nil
}

func (s *DiskStore) RelatedPending(ctx context.Context, after string, limit int) ([]DiskWork, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, err
	}
	defer s.mu.RUnlock()
	active, err := s.number("active")
	if err != nil {
		return nil, err
	}
	page, err := s.scan(ctx, diskPrefix(active, "r/"), after, limit)
	if err != nil {
		return nil, err
	}
	work := make([]DiskWork, 0, len(page))
	for _, row := range page {
		parts := strings.Split(string(row[0]), "\x00")
		if len(parts) != 3 || len(row[1]) != 8 {
			return nil, errors.New("checkpoint: invalid related work")
		}
		work = append(work, DiskWork{
			Key:   types.WorkItemKey{Kind: parts[0], Namespace: parts[1], Name: parts[2]},
			Token: binary.BigEndian.Uint64(row[1]), Cursor: string(row[0]),
		})
	}
	return work, nil
}

func (s *DiskStore) AcknowledgeRelated(ctx context.Context, key types.WorkItemKey, token uint64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if token == 0 {
		return false, errors.New("checkpoint: missing related acknowledgement token")
	}
	logical, err := diskLogical(key)
	if err != nil {
		return false, err
	}
	logical = key.Kind + "\x00" + logical
	active, err := s.number("active")
	if err != nil {
		return false, err
	}
	prefix := diskPrefix(active, "r/")
	current, err := s.token(prefix, logical)
	if err != nil || current != token {
		return false, err
	}
	batch := newDiskBatch()
	batch.Delete(append(prefix, logical...))
	err = s.write(ctx, batch)
	return err == nil, err
}

func (s *DiskStore) IndexCount(ctx context.Context, index string) (uint64, error) {
	if err := s.readLock(ctx); err != nil {
		return 0, err
	}
	defer s.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if index == "" || len(index) > 1024 {
		return 0, errors.New("checkpoint: invalid index")
	}
	active, err := s.number("active")
	if err != nil {
		return 0, err
	}
	if active == 0 {
		return 0, errors.New("checkpoint: no active disk snapshot")
	}
	return s.number(string(diskPrefix(active, "ic/"+base64.RawURLEncoding.EncodeToString([]byte(index)))))
}

type diskFanout struct {
	Generation uint64
	After      string
}

func (s *DiskStore) RequestFanout(ctx context.Context, index string) error {
	if index == "" || len(index) > 1024 {
		return errors.New("checkpoint: invalid fanout index")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := []byte("fanout/" + base64.RawURLEncoding.EncodeToString([]byte(index)))
	if index == "*" {
		exists, err := s.db.Has(key, nil)
		if err != nil {
			return err
		}
		if exists {
			return ctx.Err()
		}
	}
	batch := newDiskBatch()
	batch.Put(key, []byte(`{}`))
	return s.write(ctx, batch)
}

// ProcessFanout advances one durable range task by at most one bounded page.
// A changed generation or dependency request restarts its scan; periodic
// resync requests preserve an already-running sweep.
func (s *DiskStore) ProcessFanout(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	staging, err := s.number("staging")
	if err != nil {
		return false, err
	}
	if staging != 0 {
		return false, ErrSnapshotInProgress
	}
	active, err := s.number("active")
	if err != nil || active == 0 {
		return false, errors.Join(err, errors.New("checkpoint: no active disk snapshot"))
	}
	tasks, err := s.scan(ctx, []byte("fanout/"), "", 1)
	if err != nil || len(tasks) == 0 {
		return false, err
	}
	index, err := base64.RawURLEncoding.DecodeString(string(tasks[0][0]))
	if err != nil {
		return false, fmt.Errorf("checkpoint: invalid fanout index: %w", err)
	}
	var task diskFanout
	if err := json.Unmarshal(tasks[0][1], &task); err != nil {
		return false, fmt.Errorf("checkpoint: invalid fanout task: %w", err)
	}
	if task.Generation != active {
		task = diskFanout{Generation: active}
	}
	prefix := diskIndexPrefix(active, string(index))
	if string(index) == "*" {
		prefix = diskPrefix(active, "d/")
	}
	page, err := s.scan(ctx, prefix, task.After, DiskPageItems)
	if err != nil {
		return false, err
	}
	batch := newDiskBatch()
	taskKey := append([]byte("fanout/"), tasks[0][0]...)
	if len(page) == 0 {
		batch.Delete(taskKey)
		return true, s.write(ctx, batch)
	}
	token, err := s.number("sequence")
	if err != nil {
		return false, err
	}
	for _, row := range page {
		logical := string(row[0])
		if string(index) == "*" {
			pending, err := s.token(diskPrefix(active, "w/"), logical)
			if err != nil {
				return false, err
			}
			if pending != 0 {
				continue
			}
		}
		token++
		if token == 0 {
			return false, errors.New("checkpoint: work token overflow")
		}
		state := DiskSchedule{Token: token}
		if string(index) == "*" {
			state.Priority = 1
		}
		if err := s.queueWork(batch, active, logical, state); err != nil {
			return false, err
		}
	}
	task.After = string(page[len(page)-1][0])
	data, err := json.Marshal(task)
	if err != nil {
		return false, err
	}
	batch.Put(taskKey, data)
	batch.Put([]byte("sequence"), diskNumber(token))
	return true, s.write(ctx, batch)
}

func (s *DiskStore) IndexPage(ctx context.Context, index, after string, limit int) ([]types.WorkItemKey, string, error) {
	if err := s.readLock(ctx); err != nil {
		return nil, "", err
	}
	defer s.mu.RUnlock()
	if len(index) > 1024 || index == "" {
		return nil, "", errors.New("checkpoint: invalid index")
	}
	active, err := s.number("active")
	if err != nil {
		return nil, "", err
	}
	page, err := s.scan(ctx, diskIndexPrefix(active, index), after, limit)
	if err != nil {
		return nil, "", err
	}
	var keys []types.WorkItemKey
	for _, row := range page {
		parts := strings.SplitN(string(row[0]), "\x00", 2)
		if len(parts) != 2 {
			return nil, "", errors.New("checkpoint: invalid indexed key")
		}
		keys = append(keys, types.WorkItemKey{Kind: s.kind, Namespace: parts[0], Name: parts[1]})
		after = string(row[0])
	}
	return keys, after, nil
}

// Cross-kind readers must not wait behind a multi-million-row merge while
// holding another kind's dispatch permit. Short transactions remain waitable.
func (s *DiskStore) readLock(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if s.longOperations.Load() != 0 {
			return ErrSnapshotInProgress
		}
		if s.mu.TryRLock() {
			return nil
		}
		timer := time.NewTimer(time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *DiskStore) deleteGeneration(ctx context.Context, generation uint64, observe ...func(int64)) error {
	prefix := diskPrefix(generation, "")
	var pages int64
	after := ""
	for {
		page, err := s.scan(ctx, prefix, after, DiskPageItems)
		if err != nil || len(page) == 0 {
			return err
		}
		batch := newDiskBatch()
		for _, row := range page {
			batch.Delete(append(bytes.Clone(prefix), row[0]...))
		}
		if err := s.write(ctx, batch); err != nil {
			return err
		}
		// Avoid re-scanning every tombstone accumulated by preceding pages.
		after = string(page[len(page)-1][0])
		pages++
		for _, report := range observe {
			report(pages)
		}
	}
}
