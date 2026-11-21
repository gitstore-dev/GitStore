// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package storefront

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicDependencyGraphCannotReachManagement(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	internal := filepath.Dir(filepath.Dir(file))
	visited := map[string]bool{}
	var visit func(string)
	visit = func(dir string) {
		if visited[dir] {
			return
		}
		visited[dir] = true
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			require.NoError(t, err)
			for _, item := range parsed.Imports {
				name, err := strconv.Unquote(item.Path.Value)
				require.NoError(t, err)
				const prefix = "github.com/gitstore-dev/gitstore/api/internal/"
				if !strings.HasPrefix(name, prefix) {
					continue
				}
				rel := strings.TrimPrefix(name, prefix)
				for _, forbidden := range []string{"admin/", "datastore", "gitclient", "cataloggrpc"} {
					require.False(t, strings.HasPrefix(rel, forbidden), "public dependency %s from %s", name, path)
				}
				if !strings.HasPrefix(rel, "storefront/") {
					visit(filepath.Join(internal, rel))
				}
			}
			return nil
		})
		require.NoError(t, err)
	}
	visit(filepath.Dir(file))
}
