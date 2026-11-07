// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"context"
	"reflect"

	"github.com/99designs/gqlgen/complexity"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/errcode"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator"
	"github.com/vektah/gqlparser/v2/validator/rules"
)

// ComplexityOptions charges resource work rather than copying stored envelope
// values. Metadata/spec/status are inline DTOs; related resource resolvers stay
// outside those envelopes and retain the multiplicative list/cardinality cost.
// gqlgen ignores zero-valued custom callbacks, so these supported options are
// required to make scalar/envelope values actually cost zero.
func ComplexityOptions(schema *ast.Schema) []complexity.Option {
	ignored := map[string]struct{}{}
	for _, definition := range schema.Types {
		node := definition.Name == "Node"
		for _, iface := range definition.Interfaces {
			node = node || iface == "Node"
		}
		for _, field := range definition.Fields {
			if (node && (field.Name == "metadata" || field.Name == "spec" || field.Name == "status")) || field.Type.Name() == "PageInfo" {
				ignored[definition.Name+"."+field.Name] = struct{}{}
			}
		}
	}
	return []complexity.Option{complexity.WithFixedScalarValue(0), complexity.WithIgnoreFields(ignored)}
}

func init() {
	for _, code := range []string{"MAX_DOCUMENT_SIZE_EXCEEDED", "MAX_DEPTH_EXCEEDED", "MAX_ALIASES_EXCEEDED", "MAX_ROOT_FIELDS_EXCEEDED", "MAX_DIRECTIVES_EXCEEDED", "MAX_FRAGMENT_VISITS_EXCEEDED", "MAX_PAGE_SIZE_EXCEEDED", "MAX_NODE_IDS_EXCEEDED", "COMPLEXITY_LIMIT_EXCEEDED"} {
		errcode.RegisterErrorType(code, errcode.KindProtocol)
	}
}

func boundError(code string) *gqlerror.Error {
	return &gqlerror.Error{Message: "operation exceeds configured bounds", Extensions: map[string]any{"code": code}}
}

// OperationBounds runs on every operation, including APQ cache hits. It never
// reads the datastore; traversal and reported errors have finite budgets.
type OperationBounds struct{ Limits config.GraphQLLimits }

func (OperationBounds) ExtensionName() string                   { return "GitStoreOperationBounds" }
func (OperationBounds) Validate(graphql.ExecutableSchema) error { return nil }
func (b OperationBounds) MutateOperationParameters(_ context.Context, p *graphql.RawParams) *gqlerror.Error {
	if len(p.Query) > b.Limits.DocumentBytes {
		return boundError("MAX_DOCUMENT_SIZE_EXCEEDED")
	}
	return nil
}
func (b OperationBounds) MutateOperationContext(_ context.Context, op *graphql.OperationContext) *gqlerror.Error {
	return ValidateOperation(op, b.Limits)
}

func integer(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), int64(int(value)) == value
	case int32:
		return int(value), true
	case float64:
		return int(value), float64(int(value)) == value
	default:
		return 0, false
	}
}

func listSize(value any) int {
	if value == nil {
		return 0
	}
	v := reflect.ValueOf(value)
	if v.Kind() != reflect.Slice && v.Kind() != reflect.Array {
		return 0
	}
	return v.Len()
}

// ValidateOperation bounds expanded selections before complexity calculation or
// execution. Fragment spreads count per use, with a hard visit budget for cycles.
func ValidateOperation(op *graphql.OperationContext, limit config.GraphQLLimits) *gqlerror.Error {
	if len(op.RawQuery) > limit.DocumentBytes {
		return boundError("MAX_DOCUMENT_SIZE_EXCEEDED")
	}
	if op.Operation == nil {
		return boundError("MAX_ROOT_FIELDS_EXCEEDED")
	}
	visits, aliases, roots, directives := 0, 0, 0, len(op.Operation.Directives)
	var walk func(ast.SelectionSet, int) *gqlerror.Error
	walk = func(set ast.SelectionSet, depth int) *gqlerror.Error {
		for _, selection := range set {
			visits++
			if visits > limit.FragmentVisits {
				return boundError("MAX_FRAGMENT_VISITS_EXCEEDED")
			}
			switch field := selection.(type) {
			case *ast.Field:
				if depth+1 > limit.Depth {
					return boundError("MAX_DEPTH_EXCEEDED")
				}
				if depth == 0 {
					roots++
					if roots > limit.RootFields {
						return boundError("MAX_ROOT_FIELDS_EXCEEDED")
					}
				}
				if field.Alias != "" && field.Alias != field.Name {
					aliases++
				}
				if aliases > limit.Aliases {
					return boundError("MAX_ALIASES_EXCEEDED")
				}
				directives += len(field.Directives)
				if field.Definition != nil {
					args := field.ArgumentMap(op.Variables)
					explicitPage := false
					for _, name := range []string{"first", "last"} {
						if value := args[name]; value != nil {
							n, ok := integer(value)
							if !ok || n < 0 || n > limit.PageSize {
								return boundError("MAX_PAGE_SIZE_EXCEEDED")
							}
							explicitPage = explicitPage || n > 0
						}
					}
					paginated := field.Definition.Arguments.ForName("first") != nil || field.Definition.Arguments.ForName("last") != nil
					if paginated && !explicitPage && datastore.DefaultPageSize > limit.PageSize {
						return boundError("MAX_PAGE_SIZE_EXCEEDED")
					}
					if field.Name == "nodes" && listSize(args["ids"]) > limit.NodeIDs {
						return boundError("MAX_NODE_IDS_EXCEEDED")
					}
				}
				if err := walk(field.SelectionSet, depth+1); err != nil {
					return err
				}
			case *ast.InlineFragment:
				directives += len(field.Directives)
				if err := walk(field.SelectionSet, depth); err != nil {
					return err
				}
			case *ast.FragmentSpread:
				directives += len(field.Directives)
				definition := op.Doc.Fragments.ForName(field.Name)
				if definition != nil {
					directives += len(definition.Directives)
					if err := walk(definition.SelectionSet, depth); err != nil {
						return err
					}
				}
			}
			if directives > limit.Directives {
				return boundError("MAX_DIRECTIVES_EXCEEDED")
			}
		}
		return nil
	}
	return walk(op.Operation.SelectionSet, 0)
}

// BoundedValidationRules retains every specified validation rule but caps the
// number of errors retained/reported. Each invocation owns its counter.
func BoundedValidationRules(maximum int) func() *rules.Rules {
	return func() *rules.Rules {
		result := rules.NewRules()
		count := 0
		for name, rule := range rules.NewDefaultRules().GetInner() {
			result.AddRule(name, func(events *validator.Events, add validator.AddErrFunc) {
				rule(events, func(options ...validator.ErrorOption) {
					if count < maximum {
						count++
						add(options...)
					}
				})
			})
		}
		return result
	}
}

type limitedSchema struct {
	graphql.ExecutableSchema
	limit config.GraphQLLimits
}

// LimitSchema preserves the executable and its generated/custom callbacks. The
// generic callback fallback adds pagination cardinality without maintaining a
// parallel field-name catalog or modifying generated code.
func LimitSchema(schema graphql.ExecutableSchema, limit config.GraphQLLimits) graphql.ExecutableSchema {
	return limitedSchema{schema, limit}
}
func (s limitedSchema) Complexity(ctx context.Context, typeName, fieldName string, child int, args map[string]any) (int, bool) {
	if typeName == "Query" && (fieldName == "__schema" || fieldName == "__type") {
		return 100, true
	}
	cost, known := s.ExecutableSchema.Complexity(ctx, typeName, fieldName, child, args)
	definition := s.Schema().Types[typeName]
	var field *ast.FieldDefinition
	if definition != nil {
		field = definition.Fields.ForName(fieldName)
	}
	if !known {
		cost = 1 + child
		if field != nil {
			kind := s.Schema().Types[field.Type.Name()]
			if kind != nil && (kind.Kind == ast.Scalar || kind.Kind == ast.Enum) {
				cost = 0
			}
		}
	}
	factor := 1
	if field != nil && (field.Arguments.ForName("first") != nil || field.Arguments.ForName("last") != nil) {
		factor = datastore.DefaultPageSize
		requested := 0
		for _, name := range []string{"first", "last"} {
			if n, ok := integer(args[name]); ok && n > 0 {
				requested = max(requested, n)
			}
		}
		if requested > 0 {
			factor = requested
		}
	}
	if fieldName == "nodes" && args["ids"] != nil {
		factor = listSize(args["ids"])
	}
	if factor > 0 && cost > s.limit.Complexity/factor {
		return s.limit.Complexity + 1, true
	}
	return cost * factor, true
}
