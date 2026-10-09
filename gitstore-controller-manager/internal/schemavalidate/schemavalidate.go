// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package schemavalidate is a controller-manager test helper. It parses the
// GraphQL schema gitstore-api actually serves (shared/schemas/*.graphqls)
// and validates operation strings against it using the same
// github.com/vektah/gqlparser/v2 rules gqlgen applies to every request
// server-side.
//
// This is the regression guard for bugs where a controller GraphQL client
// selects a field the schema no longer declares — e.g. the
// completeNamespaceDeletion `conflict` selection removed from
// CompleteNamespaceDeletionPayload in 84b7bb4 (#394): gqlgen rejects the
// unknown selection, so that mutation failed validation on every controller
// call until the client's selection set was fixed. Every package that sends
// GraphQL operations through internal/graphqlclient should have a
// *_schema_test.go registering its operation strings here (see
// internal/namespace/graphql_client_schema_test.go) so a future schema
// change that breaks a selection is caught by `go test` instead of only
// discovered against a live API.
package schemavalidate

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

var (
	once    sync.Once
	schema  *ast.Schema
	loadErr error
)

// Schema lazily parses every shared/schemas/*.graphqls file — located
// relative to this source file, so resolution does not depend on the test
// binary's working directory — and caches the result for the process.
func Schema() (*ast.Schema, error) {
	once.Do(func() {
		schema, loadErr = load()
	})
	return schema, loadErr
}

func load() (*ast.Schema, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("schemavalidate: resolve source file for schema root")
	}
	// file is .../gitstore-controller-manager/internal/schemavalidate/schemavalidate.go;
	// shared/schemas lives three directories above internal/schemavalidate,
	// at the repository root.
	root := filepath.Join(filepath.Dir(file), "..", "..", "..", "shared", "schemas")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("schemavalidate: read schema dir %s: %w", root, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".graphqls" {
			continue
		}
		names = append(names, entry.Name())
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("schemavalidate: no .graphqls files found under %s", root)
	}
	sort.Strings(names)

	sources := make([]*ast.Source, 0, len(names))
	for _, name := range names {
		path := filepath.Join(root, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("schemavalidate: read %s: %w", path, err)
		}
		sources = append(sources, &ast.Source{Name: name, Input: string(raw)})
	}
	return gqlparser.LoadSchema(sources...)
}

// Operation is one GraphQL operation string a controller client sends,
// paired with the name of the Go constant it was read from (purely for
// readable test failures — every operation here is anonymous, matching
// production, so Name is never the GraphQL operation name).
type Operation struct {
	Name  string
	Query string
}

// Validate asserts every operation in ops parses and validates against the
// real schema (Schema), exactly as gqlgen validates it server-side. It
// reports every failing operation through t.Errorf rather than stopping at
// the first one, so a single run surfaces every broken operation at once.
func Validate(t *testing.T, ops []Operation) {
	t.Helper()
	schema, err := Schema()
	if err != nil {
		t.Fatalf("schemavalidate: load shared/schemas: %v", err)
		return
	}
	for _, op := range ops {
		if op.Query == "" {
			t.Errorf("schemavalidate: %s is empty", op.Name)
			continue
		}
		// LoadQuery is deprecated in favor of LoadQueryWithRules; passing
		// rules.NewDefaultRules() reproduces the same validation gqlgen
		// applies to every incoming operation.
		if _, errs := gqlparser.LoadQueryWithRules(schema, op.Query, rules.NewDefaultRules()); errs != nil {
			t.Errorf("schemavalidate: %s is invalid against shared/schemas: %v", op.Name, errs)
		}
	}
}
