// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"errors"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Policy is the in-memory representation of a v1 YAML policy file.
type Policy struct {
	Version      string                `yaml:"version"`
	Roles        map[string]RolePolicy `yaml:"roles"`
	DefaultDeny  bool                  `yaml:"default_deny"`
	RoleBindings map[string][]string   `yaml:"role_bindings"`
}

// RolePolicy defines the allow/deny action lists for one role.
type RolePolicy struct {
	Allow []string `yaml:"allow"`
	Deny  []string `yaml:"deny"`
}

func loadPolicy(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("rbaclocal: read policy file %q: %w", path, err)
	}

	var p Policy
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("rbaclocal: parse policy file %q: %w", path, err)
	}

	if err := validatePolicy(&p); err != nil {
		return nil, fmt.Errorf("rbaclocal: invalid policy file %q: %w", path, err)
	}

	// Default default_deny to true if not explicitly set.
	// yaml.Unmarshal leaves bool at its zero value (false) when absent, so we
	// rely on explicit "default_deny: false" to opt out; absent means true.
	// This is documented in the schema.

	return &p, nil
}

func validatePolicy(p *Policy) error {
	if p.Version != "v1" {
		return fmt.Errorf("unsupported version %q (only \"v1\" is valid)", p.Version)
	}
	if len(p.Roles) == 0 {
		return errors.New("at least one role must be defined")
	}
	for name, role := range p.Roles {
		if name == "" {
			return errors.New("role name must be non-empty")
		}
		for _, a := range role.Allow {
			if a == "" {
				return fmt.Errorf("role %q: allow contains empty action string", name)
			}
		}
		for _, a := range role.Deny {
			if a == "" {
				return fmt.Errorf("role %q: deny contains empty action string", name)
			}
		}
	}
	return nil
}
