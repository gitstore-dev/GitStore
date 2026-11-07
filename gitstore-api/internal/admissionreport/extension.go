// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package admissionreport collects non-fatal admission diagnostics produced
// while a GraphQL request runs and reports them in the top-level
// extensions.admission response key.
package admissionreport

import (
	"context"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/vektah/gqlparser/v2/ast"
)

// ExtensionKey is the top-level response extensions key.
const ExtensionKey = "admission"

// Entry is the report for one mutation field.
type Entry struct {
	Path        []any
	Commit      string
	Diagnostics []admission.Diagnostic
}

// Collector accumulates entries for one GraphQL response.
type Collector struct {
	mu      sync.Mutex
	entries []Entry
}

type collectorKey struct{}

// WithCollector returns a context carrying a new collector.
func WithCollector(ctx context.Context) (context.Context, *Collector) {
	collector := &Collector{}
	return context.WithValue(ctx, collectorKey{}, collector), collector
}

// FromContext returns the request's collector, or nil outside a request.
func FromContext(ctx context.Context) *Collector {
	collector, _ := ctx.Value(collectorKey{}).(*Collector)
	return collector
}

// Report records diagnostics for the field resolving in ctx. The response
// path reflects aliases. It is a no-op without diagnostics or a collector.
func Report(ctx context.Context, commit string, diagnostics []admission.Diagnostic) {
	collector := FromContext(ctx)
	if collector == nil || len(diagnostics) == 0 {
		return
	}
	var path []any
	if fieldContext := graphql.GetFieldContext(ctx); fieldContext != nil {
		for _, element := range fieldContext.Path() {
			switch value := element.(type) {
			case ast.PathName:
				path = append(path, string(value))
			case ast.PathIndex:
				path = append(path, int(value))
			}
		}
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.entries = append(collector.entries, Entry{
		Path:        path,
		Commit:      commit,
		Diagnostics: append([]admission.Diagnostic(nil), diagnostics...),
	})
}

func (c *Collector) render() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	rendered := make([]any, 0, len(c.entries))
	for _, entry := range c.entries {
		diagnostics := make([]map[string]any, 0, len(entry.Diagnostics))
		for _, d := range entry.Diagnostics {
			item := map[string]any{"reason": d.Reason, "message": d.Message, "level": string(d.Level)}
			if d.File != "" {
				item["file"] = d.File
			}
			if d.Field != "" {
				item["field"] = d.Field
			}
			diagnostics = append(diagnostics, item)
		}
		item := map[string]any{"path": entry.Path, "diagnostics": diagnostics}
		if entry.Commit != "" {
			item["commit"] = entry.Commit
		}
		rendered = append(rendered, item)
	}
	return rendered
}

// Extension installs a collector per response and merges its entries into
// extensions.admission without overwriting other extension keys.
type Extension struct{}

var (
	_ graphql.HandlerExtension    = Extension{}
	_ graphql.ResponseInterceptor = Extension{}
)

func (Extension) ExtensionName() string { return "AdmissionReport" }

func (Extension) Validate(graphql.ExecutableSchema) error { return nil }

func (Extension) InterceptResponse(ctx context.Context, next graphql.ResponseHandler) *graphql.Response {
	ctx, collector := WithCollector(ctx)
	response := next(ctx)
	if response == nil {
		return nil
	}
	entries := collector.render()
	if len(entries) == 0 {
		return response
	}
	if response.Extensions == nil {
		response.Extensions = map[string]any{}
	}
	if existing, ok := response.Extensions[ExtensionKey].([]any); ok {
		entries = append(existing, entries...)
	}
	response.Extensions[ExtensionKey] = entries
	return response
}
