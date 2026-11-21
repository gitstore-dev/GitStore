// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const rbacEvidenceBatchBytes = 64 << 10

type rbacEvidenceFile interface {
	io.WriteCloser
	Sync() error
}

type rbacEvidenceStream struct {
	file      rbacEvidenceFile
	remaining int64
	lifecycle sync.RWMutex
	mu        sync.Mutex
	ready     *sync.Cond
	pending   []byte
	accepted  int64
	written   int64
	writing   bool
	err       error
	closed    bool
	closeErr  error
}

func newRBACEvidenceStream(path string, limit int64) (*rbacEvidenceStream, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("source capture requires a clean absolute private file")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !parent.IsDir() || parent.Mode().Perm()&0077 != 0 {
		return nil, errors.New("source capture requires a private non-symlink parent")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	stream := &rbacEvidenceStream{file: file, remaining: limit}
	stream.ready = sync.NewCond(&stream.mu)
	return stream, nil
}

func (s *rbacEvidenceStream) Write(data []byte) (int, error) {
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if s.err != nil {
			return 0, s.err
		}
		if s.closed {
			return 0, os.ErrClosed
		}
		if int64(len(data)) > s.remaining {
			s.err = errors.New("RBAC capture exceeds its evidence bound")
			s.ready.Broadcast()
			return 0, s.err
		}
		if (len(s.pending) == 0 || len(s.pending)+len(data) <= rbacEvidenceBatchBytes) &&
			(!s.writing || len(data) <= rbacEvidenceBatchBytes) {
			break
		}
		s.flushPendingLocked()
	}
	if len(data) > rbacEvidenceBatchBytes {
		s.pending = data
	} else {
		s.pending = append(s.pending, data...)
	}
	s.remaining -= int64(len(data))
	s.accepted += int64(len(data))
	target := s.accepted
	for s.written < target && s.err == nil {
		s.flushPendingLocked()
	}
	if s.err != nil {
		return 0, s.err
	}
	return len(data), nil
}

// Keep filesystem I/O outside the queue lock, but publish each caller's bytes
// before returning so live evidence readers never need an asynchronous flush.
func (s *rbacEvidenceStream) flushPendingLocked() {
	if s.writing {
		s.ready.Wait()
		return
	}
	data := s.pending
	s.pending = nil
	s.writing = true
	s.mu.Unlock()
	n, err := s.file.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	s.mu.Lock()
	s.writing = false
	s.written += int64(n)
	if s.err == nil {
		s.err = err
	}
	s.ready.Broadcast()
}

func (s *rbacEvidenceStream) Sync() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.closeErr
	}
	return errors.Join(s.err, s.file.Sync())
}

func (s *rbacEvidenceStream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil {
		s.err = err
		s.ready.Broadcast()
	}
}

func (s *rbacEvidenceStream) close() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		s.closeErr = errors.Join(s.err, s.file.Sync(), s.file.Close())
	}
	return s.closeErr
}

type rbacSourceCapture struct {
	*rbacEvidenceStream
	lock chan struct{}
}

func newRBACSourceCapture(path string) (*rbacSourceCapture, error) {
	stream, err := newRBACEvidenceStream(path, 1<<30)
	if err != nil {
		return nil, err
	}
	return &rbacSourceCapture{rbacEvidenceStream: stream, lock: make(chan struct{}, 1)}, nil
}

func (c *rbacSourceCapture) observe(ctx context.Context, snapshot rbac.Snapshot) (err error) {
	select {
	case c.lock <- struct{}{}:
		defer func() { <-c.lock }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			c.fail(err)
		}
	}()
	return writeRBACSourceFrame(ctx, c, snapshot)
}

func writeRBACSourceFrame(ctx context.Context, writer io.Writer, snapshot rbac.Snapshot) error {
	resources := snapshot.Resources
	snapshot.Resources = nil
	buffer := bufio.NewWriterSize(writer, 64<<10)
	encoder := json.NewEncoder(buffer)
	if err := encoder.Encode(struct {
		Type          string        `json:"type"`
		Snapshot      rbac.Snapshot `json:"snapshot"`
		ResourceCount int           `json:"resourceCount"`
		ObservedAt    time.Time     `json:"observedAt"`
	}{"begin", snapshot, len(resources), time.Now().UTC()}); err != nil {
		return err
	}
	for _, resource := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := encoder.Encode(struct {
			Type     string         `json:"type"`
			Resource *rbac.Resource `json:"resource"`
		}{"resource", resource}); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := encoder.Encode(struct {
		Type   string `json:"type"`
		Digest string `json:"digest"`
	}{"end", snapshot.ContentDigest}); err != nil {
		return err
	}
	if err := buffer.Flush(); err != nil {
		return err
	}
	return ctx.Err()
}

func (c *rbacSourceCapture) close() error {
	c.lock <- struct{}{}
	defer func() { <-c.lock }()
	return c.rbacEvidenceStream.close()
}

type rbacDecisionEvidenceCore struct{ zapcore.Core }

func (c rbacDecisionEvidenceCore) With(fields []zapcore.Field) zapcore.Core {
	return rbacDecisionEvidenceCore{c.Core.With(fields)}
}

func (c rbacDecisionEvidenceCore) Check(entry zapcore.Entry, checked *zapcore.CheckedEntry) *zapcore.CheckedEntry {
	if entry.Message != "authz decision" && entry.Message != "authz decision error" {
		return checked
	}
	return c.Core.Check(entry, checked)
}

type rbacRuntimeEvidence struct {
	source    *rbacSourceCapture
	decisions *rbacEvidenceStream
}

func newRBACRuntimeEvidence(directory string, logger *zap.Logger) (*rbacRuntimeEvidence, *zap.Logger, error) {
	if directory == "" {
		return nil, logger, nil
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return nil, nil, errors.New("RBAC evidence requires a clean absolute private directory")
	}
	source, err := newRBACSourceCapture(filepath.Join(directory, "source-models.jsonl"))
	if err != nil {
		return nil, nil, err
	}
	decisions, err := newRBACEvidenceStream(filepath.Join(directory, "decisions.jsonl"), 8<<30)
	if err != nil {
		_ = source.close()
		_ = os.Remove(filepath.Join(directory, "source-models.jsonl"))
		return nil, nil, err
	}
	core := rbacDecisionEvidenceCore{zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), decisions, zap.InfoLevel)}
	observed := logger.WithOptions(zap.WrapCore(func(existing zapcore.Core) zapcore.Core {
		return zapcore.NewTee(existing, core)
	}))
	return &rbacRuntimeEvidence{source: source, decisions: decisions}, observed, nil
}

func (e *rbacRuntimeEvidence) close() error {
	return errors.Join(e.source.close(), e.decisions.close())
}
