// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/gitstore-dev/gitstore/translation"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TranslationTargetFields preserves absent and null leaves while normalizing
// declared JSON names to their authored YAML names. Callers establish authority.
func TranslationTargetFields(ctx context.Context, raw []byte, schema reflect.Type) (map[string]any, error) {
	if len(raw) > translation.MaxTargetBytes {
		return nil, status.Error(codes.ResourceExhausted, "target validation budget exceeded")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var visit func(int) error
	visit = func(depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if nodes > translation.MaxTargetNodes || depth > translation.MaxTargetDepth {
			return status.Error(codes.ResourceExhausted, "target validation budget exceeded")
		}
		token, err := decoder.Token()
		if err != nil {
			return status.Error(codes.Unavailable, "target content unavailable")
		}
		delimiter, container := token.(json.Delim)
		if !container {
			return nil
		}
		if delimiter != '{' && delimiter != '[' {
			return status.Error(codes.Unavailable, "target content unavailable")
		}
		var keys map[string]bool
		if delimiter == '{' {
			keys = make(map[string]bool)
		}
		for decoder.More() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if nodes >= translation.MaxTargetNodes {
				return status.Error(codes.ResourceExhausted, "target validation budget exceeded")
			}
			if delimiter == '{' {
				token, err := decoder.Token()
				key, ok := token.(string)
				if err != nil || !ok || keys[key] {
					return status.Error(codes.Unavailable, "ambiguous target content")
				}
				keys[key] = true
			}
			if err := visit(depth + 1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || delimiter == '{' && closing != json.Delim('}') || delimiter == '[' && closing != json.Delim(']') {
			return status.Error(codes.Unavailable, "target content unavailable")
		}
		return nil
	}
	if err := visit(0); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, status.Error(codes.Unavailable, "target content unavailable")
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil || decoded == nil {
		return nil, status.Error(codes.Unavailable, "target content unavailable")
	}
	nodes = 0
	normalized, err := translationAuthoredJSON(ctx, decoded, schema, &nodes, 0)
	if err != nil {
		return nil, err
	}
	return normalized.(map[string]any), nil
}

func translationAuthoredJSON(ctx context.Context, value any, schema reflect.Type, nodes *int, depth int) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	*nodes++
	if *nodes > translation.MaxTargetNodes || depth > translation.MaxTargetDepth {
		return nil, status.Error(codes.ResourceExhausted, "target validation budget exceeded")
	}
	for schema != nil && schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, child := range value {
			name := key
			var childSchema reflect.Type
			if schema != nil && schema.Kind() == reflect.Map {
				childSchema = schema.Elem()
			} else if schema != nil && schema.Kind() == reflect.Struct {
				for i := 0; i < schema.NumField(); i++ {
					field := schema.Field(i)
					jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
					yamlName, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
					if field.PkgPath != "" || jsonName == "-" || yamlName == "-" {
						continue
					}
					if jsonName == "" {
						jsonName = field.Name
					}
					if yamlName == "" {
						yamlName = strings.ToLower(field.Name)
					}
					if key == jsonName || key == yamlName {
						name, childSchema = yamlName, field.Type
						break
					}
				}
			}
			if _, duplicate := result[name]; duplicate {
				return nil, status.Error(codes.Unavailable, "ambiguous target content")
			}
			normalized, err := translationAuthoredJSON(ctx, child, childSchema, nodes, depth+1)
			if err != nil {
				return nil, err
			}
			result[name] = normalized
		}
		return result, nil
	case []any:
		var childSchema reflect.Type
		if schema != nil && (schema.Kind() == reflect.Slice || schema.Kind() == reflect.Array) {
			childSchema = schema.Elem()
		}
		result := make([]any, len(value))
		for i, child := range value {
			normalized, err := translationAuthoredJSON(ctx, child, childSchema, nodes, depth+1)
			if err != nil {
				return nil, err
			}
			result[i] = normalized
		}
		return result, nil
	default:
		return value, nil
	}
}
