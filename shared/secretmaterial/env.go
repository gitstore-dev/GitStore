// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"os"
	"regexp"
	"strings"
)

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type EnvBinding struct {
	Scope    Scope
	Name     string
	Key      string
	Variable string
}

type envAddress struct {
	scope Scope
	name  string
	key   string
}

type EnvironmentProvider struct {
	format   Format
	bindings map[envAddress]string
}

func NewEnvironmentProvider(format Format, bindings []EnvBinding) (*EnvironmentProvider, error) {
	if format != FormatRaw && format != FormatJSONRecord {
		return nil, failure(ErrUnsupportedType, "format", nil)
	}
	if len(bindings) == 0 {
		return nil, failure(ErrInvalidRef, "bindings", nil)
	}
	p := &EnvironmentProvider{format: format, bindings: make(map[envAddress]string, len(bindings))}
	variables := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		ref := SecretRef{Kind: "SecretRef", Name: binding.Name}
		if format == FormatRaw {
			ref.Key = &binding.Key
		} else if binding.Key != "" {
			return nil, failure(ErrInvalidRef, "binding-key", nil)
		}
		if err := validateScope(binding.Scope, ref); err != nil {
			return nil, err
		}
		if !envNamePattern.MatchString(binding.Variable) {
			return nil, failure(ErrInvalidRef, "variable", nil)
		}
		address := envAddress{scope: binding.Scope, name: binding.Name, key: binding.Key}
		if _, exists := p.bindings[address]; exists || variables[binding.Variable] {
			return nil, failure(ErrInvalidRef, "duplicate-binding", nil)
		}
		p.bindings[address] = binding.Variable
		variables[binding.Variable] = true
	}
	return p, nil
}

// BootstrapEnvironmentVariable names one configured binding, not arbitrary
// runtime references. Whole-record bindings omit the keyed suffix.
func BootstrapEnvironmentVariable(prefix string, ref SecretRef) (string, error) {
	if !envNamePattern.MatchString(prefix) {
		return "", failure(ErrInvalidRef, "environment-binding", nil)
	}
	if err := ValidateSecretRef(ref, ""); err != nil {
		return "", err
	}
	name := prefix + legacyEnvironmentComponent(ref.Name)
	if ref.Key != nil {
		name += "__" + legacyEnvironmentComponent(*ref.Key)
	}
	return name, nil
}

func legacyEnvironmentComponent(value string) string {
	var builder strings.Builder
	for _, char := range value {
		switch char {
		case '-':
			builder.WriteString("_DASH_")
		case '.':
			builder.WriteString("_DOT_")
		case '_':
			builder.WriteString("_UNDERSCORE_")
		default:
			if char >= 'a' && char <= 'z' {
				char -= 'a' - 'A'
			}
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

func (p *EnvironmentProvider) Category() ProviderCategory { return ProviderEnv }
func (p *EnvironmentProvider) Format() Format             { return p.format }

func (p *EnvironmentProvider) Read(ctx context.Context, ref SecretRef, scope Scope) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure(ErrProviderUnavailable, "context", err)
	}
	if err := validateScope(scope, ref); err != nil {
		return nil, err
	}
	key := ""
	if p.format == FormatRaw {
		if ref.Key == nil {
			return nil, failure(ErrInvalidRef, "key", nil)
		}
		key = *ref.Key
	}
	variable, ok := p.bindings[envAddress{scope: scope, name: ref.Name, key: key}]
	if !ok {
		return nil, failure(ErrForbidden, "binding", nil)
	}
	value, ok := os.LookupEnv(variable)
	if !ok {
		return nil, failure(ErrNotFound, "record", nil)
	}
	if len(value) > MaxEncodedBytes {
		return nil, failure(ErrValueTooLarge, "encoded-bytes", nil)
	}
	if value == "" {
		return nil, failure(ErrMissingKey, "item", nil)
	}
	return []byte(value), nil
}
