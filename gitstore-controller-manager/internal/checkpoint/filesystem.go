// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package checkpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	return rec, nil
}

// Save atomically writes rec to its kind's checkpoint file: write to a temp
// file in Dir, fsync, close, then rename into place.
func (s *FilesystemStore) Save(_ context.Context, rec Record) error {
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
