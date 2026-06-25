// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package rbaclocal

import (
	"context"
	"fmt"
	"sync"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

// RBACLocalProvider enforces access control using a YAML policy file.
type RBACLocalProvider struct {
	mu     sync.RWMutex
	policy *Policy
	path   string
	logger *zap.Logger
}

func New(cfg *viper.Viper, logger *zap.Logger) (*RBACLocalProvider, error) {
	path := cfg.GetString("auth.rbac.policy_file")
	if path == "" {
		path = "policy.yaml"
	}
	p := &RBACLocalProvider{path: path, logger: logger}
	if err := p.Reload(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *RBACLocalProvider) Name() string { return "rbac-local" }

// Reload re-reads and validates the policy file atomically. Safe to call from a
// SIGHUP handler.
func (p *RBACLocalProvider) Reload() error {
	policy, err := loadPolicy(p.path)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.policy = policy
	p.mu.Unlock()
	p.logger.Info("rbac-local policy reloaded", zap.String("path", p.path))
	return nil
}

func (p *RBACLocalProvider) Authorize(_ context.Context, principal *auth.Principal, action string, _ auth.ResourceContext) (auth.Decision, error) {
	p.mu.RLock()
	policy := p.policy
	p.mu.RUnlock()

	var anyAllow bool
	for _, roleName := range principal.Roles {
		role, ok := policy.Roles[roleName]
		if !ok {
			continue
		}
		// Explicit deny overrides everything.
		for _, d := range role.Deny {
			if d == action || d == "*" {
				return auth.Deny("rbac-local", fmt.Sprintf("role %q explicitly denies action %q", roleName, action)), nil
			}
		}
		// Check allow.
		for _, a := range role.Allow {
			if a == action || a == "*" {
				anyAllow = true
			}
		}
	}

	if anyAllow {
		return auth.Allow("rbac-local", fmt.Sprintf("action %q allowed by role policy", action)), nil
	}

	if policy.DefaultDeny {
		return auth.Deny("rbac-local", fmt.Sprintf("action %q not permitted (default deny)", action)), nil
	}
	return auth.Allow("rbac-local", fmt.Sprintf("action %q allowed (default allow)", action)), nil
}
