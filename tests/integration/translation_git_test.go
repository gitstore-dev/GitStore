// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func commitTranslationIntegrationSources(ctx context.Context, worker *capacityPushWorker, sources map[string][]byte) (string, error) {
	if len(sources) == 0 || len(sources) > 20 {
		return "", errors.New("translation fixture: commit requires 1..20 bounded sources")
	}
	paths := make([]string, 0, len(sources))
	for name, source := range sources {
		if path.Clean(name) != name || path.IsAbs(name) || name == "." || name == ".." ||
			strings.HasPrefix(name, "../") || strings.Contains(name, "\\") || strings.HasPrefix(name, "-") ||
			len(source) > (1<<20)+1 {
			return "", errors.New("translation fixture: unsafe path or oversized fixture")
		}
		for _, component := range strings.Split(name, "/") {
			if strings.HasPrefix(component, ".") {
				return "", errors.New("translation fixture: source may not address hidden paths")
			}
		}
		paths = append(paths, name)
	}
	slices.Sort(paths)
	for _, name := range paths {
		parent := worker.directory
		for _, component := range strings.Split(path.Dir(name), "/") {
			if component == "." {
				continue
			}
			parent = filepath.Join(parent, component)
			if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return "", errors.New("translation fixture: cannot create source parent")
			}
			info, err := os.Lstat(parent)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("translation fixture: source parent is not an owned directory")
			}
		}
		destination := filepath.Join(worker.directory, filepath.FromSlash(name))
		if info, err := os.Lstat(destination); err == nil {
			if !info.Mode().IsRegular() {
				return "", errors.New("translation fixture: source destination is not a regular file")
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("translation fixture: cannot inspect source destination")
		}
		if err := os.WriteFile(destination, sources[name], 0600); err != nil {
			return "", errors.New("translation fixture: cannot write source")
		}
	}
	if _, err := secretCapacityGit(ctx, worker.directory, "", append([]string{"add", "--"}, paths...)...); err != nil {
		return "", err
	}
	if _, err := secretCapacityGit(ctx, worker.directory, "", "commit", "--quiet", "-m", "Exercise Translation source ingestion"); err != nil {
		return "", err
	}
	commit, err := secretCapacityGit(ctx, worker.directory, "", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	hash := strings.TrimSpace(string(commit))
	for _, name := range paths {
		stored, err := secretCapacityGitBounded(ctx, worker.directory, "", (1<<20)+1, "cat-file", "blob", hash+":"+name)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(stored, sources[name]) {
			return "", fmt.Errorf("translation fixture: Git changed authored bytes for %s", name)
		}
	}
	return hash, nil
}

func TestTranslationIntegrationGitTransportPreservesCanonicalCorpus(t *testing.T) {
	for _, objectFormat := range []string{"sha1", "sha256"} {
		t.Run(objectFormat, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", "--object-format="+objectFormat, remote)
			require.NoError(t, err)
			worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, root, "", 0)
			require.NoError(t, err)
			raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
			require.NoError(t, err)
			var corpus struct {
				Namespace string
				Valid     map[string]string
				Targets   map[string]string
			}
			require.NoError(t, json.Unmarshal(raw, &corpus))
			sources := make(map[string][]byte)
			for directory, examples := range map[string]map[string]string{"targets": corpus.Targets, "translations": corpus.Valid} {
				for name, source := range examples {
					rendered, err := translationIntegrationSource([]byte(source), corpus.Namespace, "translation-owned")
					require.NoError(t, err)
					sources[directory+"/"+name+".md"] = rendered
				}
			}
			commit, err := commitTranslationIntegrationSources(t.Context(), worker, sources)
			require.NoError(t, err)
			_, err = secretCapacityGit(t.Context(), worker.directory, "", "push", "--quiet", "origin", "HEAD:refs/heads/main")
			require.NoError(t, err)
			beforeRefs, err := readTranslationCapacityReferences(t.Context(), remote, "")
			require.NoError(t, err)
			reader, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, root, "", 1)
			require.NoError(t, err)
			for name, expected := range sources {
				actual, err := secretCapacityGit(t.Context(), reader.directory, "", "cat-file", "blob", commit+":"+name)
				require.NoError(t, err)
				require.Equal(t, expected, actual)
			}
			_, err = secretCapacityGit(t.Context(), worker.directory, "", "push", "--quiet", "origin", "HEAD:refs/heads/owned-reference-probe")
			require.NoError(t, err)
			afterRefs, err := readTranslationCapacityReferences(t.Context(), remote, "")
			require.NoError(t, err)
			require.NotEqual(t, beforeRefs, afterRefs, "baseline evidence must detect ref changes even when authored blobs are unchanged")
		})
	}
}

func TestTranslationIntegrationGitTransportRejectsNormalizationBeforePush(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", remote)
	require.NoError(t, err)
	worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, root, "", 0)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(worker.directory, ".gitattributes"), []byte("*.md text eol=lf\n"), 0600))
	_, err = commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{
		"translations/crlf.md": []byte("---\r\nkind: Translation\r\n---\r\n\r\nExact body.\r\n"),
	})
	require.ErrorContains(t, err, "Git changed authored bytes")
	tip, err := secretCapacityGit(t.Context(), root, "", "--git-dir", remote, "for-each-ref", "--format=%(objectname)", "refs/heads/main")
	require.NoError(t, err)
	require.Empty(t, bytes.TrimSpace(tip), "normalization must be detected before a remote write")
}

func TestTranslationIntegrationGitTransportPreservesNearLimitBlob(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			remote := filepath.Join(root, "remote.git")
			_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", "--object-format="+format, remote)
			require.NoError(t, err)
			worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, root, "", 0)
			require.NoError(t, err)
			source := bytes.Repeat([]byte("x"), 1<<20)
			copy(source, []byte("---\r\nkind: Translation\r\n---\r\n"))
			commit, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{"translations/near-limit.md": source})
			require.NoError(t, err, "a valid document must fit exact-byte Git verification")
			_, err = secretCapacityGit(t.Context(), worker.directory, "", "cat-file", "blob", commit+":translations/near-limit.md")
			require.Error(t, err, "ordinary Git commands retain their existing output allowance")
		})
	}
}

func TestTranslationIntegrationGitTransportRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", remote)
	require.NoError(t, err)
	worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, root, "", 0)
	require.NoError(t, err)
	outside := filepath.Join(root, "preserved")
	require.NoError(t, os.Mkdir(outside, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(outside, "source.md"), []byte("unchanged"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(worker.directory, "escape")))
	for _, name := range []string{"../preserved/source.md", ".git/config", ".GIT/config", "escape/source.md"} {
		_, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{name: []byte("changed")})
		require.Error(t, err)
	}
	body, err := os.ReadFile(filepath.Join(outside, "source.md"))
	require.NoError(t, err)
	require.Equal(t, []byte("unchanged"), body)
}
