// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type sourceFrameWriter struct {
	bytes.Buffer
	writes int
	err    error
	after  func()
}

type blockingEvidenceFile struct {
	rbacEvidenceFile
	started chan struct{}
	release chan struct{}
	writes  int
	maximum int
	fail    error
	short   bool
}

func (f *blockingEvidenceFile) Write(data []byte) (int, error) {
	f.writes++
	f.maximum = max(f.maximum, len(data))
	if f.writes == 1 {
		close(f.started)
		<-f.release
	}
	if f.fail != nil {
		return 0, f.fail
	}
	if f.short {
		return len(data) - 1, nil
	}
	return f.rbacEvidenceFile.Write(data)
}

func TestRBACEvidenceCoalescesConcurrentWritesWithoutDeferringPublication(t *testing.T) {
	for _, terminal := range []string{"sync", "close"} {
		t.Run(terminal, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.Chmod(directory, 0700))
			path := filepath.Join(directory, "evidence.jsonl")
			stream, err := newRBACEvidenceStream(path, 1<<20)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stream.close() })
			file := &blockingEvidenceFile{rbacEvidenceFile: stream.file,
				started: make(chan struct{}), release: make(chan struct{})}
			stream.file = file
			t.Cleanup(func() {
				select {
				case <-file.release:
				default:
					close(file.release)
				}
			})
			const record = "{\"outcome\":\"deny\"}\n"
			results := make(chan error, 13)
			write := func() {
				n, err := stream.Write([]byte(record))
				if err == nil && n != len(record) {
					err = io.ErrShortWrite
				}
				results <- err
			}
			go write()
			<-file.started
			for range 12 {
				go write()
			}
			require.Eventually(t, func() bool {
				stream.mu.Lock()
				defer stream.mu.Unlock()
				return stream.accepted == 13*int64(len(record))
			}, 5*time.Second, time.Millisecond, "waiting writers must enqueue without acquiring the filesystem lock")
			select {
			case <-results:
				t.Fatal("Write returned before its evidence reached the file")
			default:
			}
			finished := make(chan error, 1)
			go func() {
				if terminal == "sync" {
					finished <- stream.Sync()
				} else {
					finished <- stream.close()
				}
			}()
			close(file.release)
			for range 13 {
				require.NoError(t, <-results)
			}
			require.NoError(t, <-finished)
			require.Equal(t, 2, file.writes, "concurrent receipts must share a bounded filesystem write")
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat(record, 13), string(content))
			if terminal == "close" {
				_, err = stream.Write([]byte(record))
				require.ErrorIs(t, err, os.ErrClosed)
			}
		})
	}
}

func TestRBACEvidenceCoalescedWriteFailuresRemainUnsealable(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "write-error", true: "short-write"}[short], func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.Chmod(directory, 0700))
			stream, err := newRBACEvidenceStream(filepath.Join(directory, "evidence.jsonl"), 1<<20)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stream.close() })
			failure := errors.New("owned evidence write failed")
			file := &blockingEvidenceFile{rbacEvidenceFile: stream.file,
				started: make(chan struct{}), release: make(chan struct{}), fail: failure}
			if short {
				file.fail, file.short, failure = nil, true, io.ErrShortWrite
			}
			stream.file = file
			t.Cleanup(func() {
				select {
				case <-file.release:
				default:
					close(file.release)
				}
			})
			results := make(chan error, 2)
			go func() { _, err := stream.Write([]byte("first")); results <- err }()
			<-file.started
			go func() { _, err := stream.Write([]byte("second")); results <- err }()
			require.Eventually(t, func() bool {
				stream.mu.Lock()
				defer stream.mu.Unlock()
				return stream.accepted == 11
			}, 5*time.Second, time.Millisecond)
			close(file.release)
			for range 2 {
				require.ErrorIs(t, <-results, failure)
			}
			_, err = stream.Write([]byte("later"))
			require.ErrorIs(t, err, failure)
			require.ErrorIs(t, stream.Sync(), failure)
			require.ErrorIs(t, stream.close(), failure)
			require.ErrorIs(t, stream.close(), failure)
		})
	}
}

func TestRBACEvidenceBoundsQueuedBatchesAndReservesUnpublishedBytes(t *testing.T) {
	for _, limited := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch-bound", true: "stream-bound"}[limited], func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.Chmod(directory, 0700))
			path := filepath.Join(directory, "evidence.jsonl")
			record := []byte(strings.Repeat("x", rbacEvidenceBatchBytes/2-1) + "\n")
			limit := int64(1 << 20)
			if limited {
				limit = int64(3 * len(record))
			}
			stream, err := newRBACEvidenceStream(path, limit)
			require.NoError(t, err)
			t.Cleanup(func() { _ = stream.close() })
			file := &blockingEvidenceFile{rbacEvidenceFile: stream.file,
				started: make(chan struct{}), release: make(chan struct{})}
			stream.file = file
			t.Cleanup(func() {
				select {
				case <-file.release:
				default:
					close(file.release)
				}
			})
			results := make(chan error, 8)
			write := func() { _, err := stream.Write(record); results <- err }
			go write()
			<-file.started
			for range 2 {
				go write()
			}
			require.Eventually(t, func() bool {
				stream.mu.Lock()
				defer stream.mu.Unlock()
				return len(stream.pending) == rbacEvidenceBatchBytes && stream.accepted == 3*int64(len(record))
			}, 5*time.Second, time.Millisecond)
			if limited {
				_, err = stream.Write([]byte("overflow"))
				require.ErrorContains(t, err, "evidence bound", "reserved queued bytes count before filesystem publication")
			} else {
				for range 5 {
					go write()
				}
			}
			close(file.release)
			count := 8
			if limited {
				count = 3
			}
			for range count {
				err := <-results
				if limited {
					require.ErrorContains(t, err, "evidence bound")
				} else {
					require.NoError(t, err)
				}
			}
			require.LessOrEqual(t, file.maximum, rbacEvidenceBatchBytes)
			if limited {
				require.ErrorContains(t, stream.close(), "evidence bound")
			} else {
				require.NoError(t, stream.close())
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, bytes.Repeat(record, count), content)
			}
		})
	}
}

func (w *sourceFrameWriter) Write(data []byte) (int, error) {
	w.writes++
	if w.err != nil {
		return 0, w.err
	}
	n, err := w.Buffer.Write(data)
	if w.after != nil {
		w.after()
	}
	return n, err
}

func TestRBACSourceCaptureCoalescesWritesWithoutDeferringCompleteEvidence(t *testing.T) {
	source := rbac.Snapshot{ContentDigest: strings.Repeat("a", 64)}
	for range 257 {
		source.Resources = append(source.Resources, &rbac.Resource{Kind: rbac.ClusterRole,
			Metadata: rbac.Metadata{Name: "source-role", Annotations: map[string]string{"padding": strings.Repeat("x", 8192)}},
			Spec:     json.RawMessage(`{"rules":[]}`)})
	}
	var writer sourceFrameWriter
	require.NoError(t, writeRBACSourceFrame(t.Context(), &writer, source))
	require.Less(t, writer.writes, len(source.Resources)/2, "source export must not issue one filesystem write per resource")
	decoder := json.NewDecoder(&writer.Buffer)
	for i := range len(source.Resources) + 2 {
		var record struct {
			Type          string         `json:"type"`
			ResourceCount int            `json:"resourceCount"`
			Resource      *rbac.Resource `json:"resource"`
			Digest        string         `json:"digest"`
		}
		require.NoError(t, decoder.Decode(&record), "observe must return only after the entire frame is written")
		switch i {
		case 0:
			require.Equal(t, "begin", record.Type)
			require.Equal(t, len(source.Resources), record.ResourceCount)
		case len(source.Resources) + 1:
			require.Equal(t, "end", record.Type)
			require.Equal(t, source.ContentDigest, record.Digest)
		default:
			require.Equal(t, "resource", record.Type)
			require.Equal(t, source.Resources[i-1], record.Resource)
		}
	}
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF)
}

func TestRBACSourceCaptureReturnsFinalFlushFailureAndCancellation(t *testing.T) {
	failure := errors.New("owned evidence write failed")
	writer := &sourceFrameWriter{err: failure}
	require.ErrorIs(t, writeRBACSourceFrame(t.Context(), writer, rbac.Snapshot{}), failure)
	ctx, cancel := context.WithCancel(t.Context())
	writer = &sourceFrameWriter{after: cancel}
	require.ErrorIs(t, writeRBACSourceFrame(ctx, writer, rbac.Snapshot{}), context.Canceled)
}

func TestRBACEvidencePreservesDefaultsAndCapturesUnsampledConcurrentDecisions(t *testing.T) {
	var ordinary bytes.Buffer
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.Lock(zapcore.AddSync(&ordinary)), zap.InfoLevel)
	logger := zap.New(zapcore.NewSamplerWithOptions(core, time.Hour, 1, 100))
	disabled, unchanged, err := newRBACRuntimeEvidence("", logger)
	require.NoError(t, err)
	require.Nil(t, disabled)
	require.Same(t, logger, unchanged)

	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	evidence, observed, err := newRBACRuntimeEvidence(directory, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = evidence.close() })
	observed = observed.Named("auth").With(zap.String("fixture", "owned"))
	var writers sync.WaitGroup
	for range 12 {
		writers.Go(func() {
			for range 100 {
				observed.Info("authz decision", zap.String("outcome", "allow"))
			}
		})
	}
	writers.Wait()
	observed.Warn("authz decision error", zap.String("outcome", "deny"))
	observed.Info("unrelated runtime event")
	source := rbac.Snapshot{ContentDigest: strings.Repeat("a", 64),
		Resources: []*rbac.Resource{{Kind: rbac.Role, Metadata: rbac.Metadata{Name: "source-role"}}}}
	require.NoError(t, evidence.source.observe(t.Context(), source))
	require.NoError(t, evidence.close())
	require.NoError(t, evidence.close(), "shutdown may be invoked more than once")
	content, err := os.ReadFile(filepath.Join(directory, "decisions.jsonl"))
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSpace(content), []byte("\n"))
	require.Len(t, lines, 1201, "sampling must not silently erase individual ordinary decision receipts")
	for _, line := range lines {
		var receipt map[string]any
		require.NoError(t, json.Unmarshal(line, &receipt))
		require.Equal(t, "owned", receipt["fixture"])
		require.Contains(t, []any{"authz decision", "authz decision error"}, receipt["msg"])
	}
	require.Less(t, bytes.Count(ordinary.Bytes(), []byte("\n")), 30, "default operational logging remains sampled")
	model, err := os.ReadFile(filepath.Join(directory, "source-models.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(model), `"observedAt"`)
	require.Contains(t, string(model), `"name":"source-role"`)
	require.Len(t, source.Resources, 1, "evidence must not consume the provider source")
}

func TestRBACEvidenceRequiresPrivateExclusiveFilesAndPreservesFailedStreams(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0700))
	decision := filepath.Join(directory, "decisions.jsonl")
	require.NoError(t, os.WriteFile(decision, []byte("existing"), 0600))
	_, _, err := newRBACRuntimeEvidence(directory, zap.NewNop())
	require.ErrorIs(t, err, os.ErrExist)
	content, err := os.ReadFile(decision)
	require.NoError(t, err)
	require.Equal(t, "existing", string(content))
	_, err = os.Stat(filepath.Join(directory, "source-models.jsonl"))
	require.ErrorIs(t, err, os.ErrNotExist, "only the new, unused source file is removed after failed construction")
	require.NoError(t, os.Remove(decision))
	evidence, _, err := newRBACRuntimeEvidence(directory, zap.NewNop())
	require.NoError(t, err)
	t.Cleanup(func() { _ = evidence.close() })
	evidence.source.remaining = 1
	require.ErrorContains(t, evidence.source.observe(t.Context(), rbac.Snapshot{}), "evidence bound")
	require.ErrorContains(t, evidence.source.observe(t.Context(), rbac.Snapshot{}), "evidence bound")
	require.ErrorContains(t, evidence.close(), "evidence bound")
	require.ErrorContains(t, evidence.close(), "evidence bound", "failed receipt streams cannot be sealed as successful")
}
