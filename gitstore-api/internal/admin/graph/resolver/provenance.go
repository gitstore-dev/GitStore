// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admin/graph/model"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/vektah/gqlparser/v2/ast"
)

type rbacProvenance struct {
	Path          []any      `json:"path,omitempty"`
	ID            string     `json:"id,omitempty"`
	RepositoryRef string     `json:"repositoryRef,omitempty"`
	Ref           string     `json:"ref,omitempty"`
	File          string     `json:"file,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	ChangeCommit  string     `json:"changeCommit,omitempty"`
	Author        string     `json:"author,omitempty"`
	AuthoredAt    *time.Time `json:"authoredAt,omitempty"`
	BuiltinName   string     `json:"builtinName,omitempty"`
	BuiltinDigest string     `json:"builtinDigest,omitempty"`
	Error         string     `json:"error,omitempty"`
}

type provenanceCollector struct {
	mu        sync.Mutex
	reserved  int
	truncated bool
	entries   []rbacProvenance
}

type provenanceContextKey struct{}
type provenanceExtension struct{ resolver *Resolver }

func (r *Resolver) ProvenanceExtension() graphql.HandlerExtension {
	return provenanceExtension{resolver: r}
}

func (provenanceExtension) ExtensionName() string                   { return "RBACProvenance" }
func (provenanceExtension) Validate(graphql.ExecutableSchema) error { return nil }

func (e provenanceExtension) InterceptField(ctx context.Context, next graphql.Resolver) (any, error) {
	value, err := next(ctx)
	collector, _ := ctx.Value(provenanceContextKey{}).(*provenanceCollector)
	if err != nil || collector == nil {
		return value, err
	}
	var kind rbac.Kind
	var metadata *model.ObjectMeta
	var id string
	switch node := value.(type) {
	case *model.Role:
		if node != nil {
			kind, metadata, id = rbac.Role, node.Metadata, node.ID
		}
	case *model.ClusterRole:
		if node != nil {
			kind, metadata, id = rbac.ClusterRole, node.Metadata, node.ID
		}
	case *model.RoleBinding:
		if node != nil {
			kind, metadata, id = rbac.RoleBinding, node.Metadata, node.ID
		}
	case *model.ClusterRoleBinding:
		if node != nil {
			kind, metadata, id = rbac.ClusterRoleBinding, node.Metadata, node.ID
		}
	}
	field := graphql.GetFieldContext(ctx)
	if metadata == nil || field == nil {
		return value, nil
	}
	collector.mu.Lock()
	if collector.reserved >= 64 {
		collector.truncated = true
		collector.mu.Unlock()
		return value, nil
	}
	collector.reserved++
	collector.mu.Unlock()
	provenance, lookupErr := e.resolver.rbacProvenance(ctx, kind, metadata)
	if lookupErr != nil {
		provenance = &rbacProvenance{Error: "PROVENANCE_UNAVAILABLE"}
	}
	provenance.ID = id
	for _, element := range field.Path() {
		switch part := element.(type) {
		case ast.PathName:
			provenance.Path = append(provenance.Path, string(part))
		case ast.PathIndex:
			provenance.Path = append(provenance.Path, int(part))
		}
	}
	collector.mu.Lock()
	collector.entries = append(collector.entries, *provenance)
	collector.mu.Unlock()
	return value, nil
}

func (provenanceExtension) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	operation := graphql.GetOperationContext(ctx)
	if operation == nil || !strings.EqualFold(operation.Headers.Get("GitStore-Provenance-Include"), "true") {
		return next(ctx)
	}
	collector := &provenanceCollector{}
	response := next(context.WithValue(ctx, provenanceContextKey{}, collector))
	if response == nil {
		return nil
	}
	var data any
	if json.Unmarshal(response.Data, &data) != nil {
		return response
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	entries := make([]rbacProvenance, 0, len(collector.entries)+1)
	for _, entry := range collector.entries {
		if provenancePathVisible(data, entry.Path) {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		left, _ := json.Marshal(entries[i].Path)
		right, _ := json.Marshal(entries[j].Path)
		return string(left) < string(right)
	})
	if collector.truncated && len(entries) > 0 {
		entries = append(entries, rbacProvenance{Error: "PROVENANCE_LIMIT_EXCEEDED"})
	}
	if len(entries) > 0 {
		if response.Extensions == nil {
			response.Extensions = map[string]any{}
		}
		response.Extensions["provenance"] = entries
	}
	return response
}

func provenancePathVisible(value any, path []any) bool {
	for _, part := range path {
		switch key := part.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				return false
			}
			value = object[key]
		case int:
			list, ok := value.([]any)
			if !ok || key < 0 || key >= len(list) {
				return false
			}
			value = list[key]
		default:
			return false
		}
	}
	_, ok := value.(map[string]any)
	return ok
}
