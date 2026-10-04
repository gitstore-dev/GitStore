// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package secret binds controller bootstrap identity to shared secret acquisition.
package secret

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/gitstore-dev/gitstore/secretmaterial"
)

const (
	ProviderFile        = "file"
	ProviderEnvironment = "env"
)

type Ref struct {
	Kind string `mapstructure:"kind"`
	Name string `mapstructure:"name"`
	Key  string `mapstructure:"key"`
}

func (ref Ref) SecretRef() secretmaterial.SecretRef {
	result := secretmaterial.SecretRef{Kind: ref.Kind, Name: ref.Name}
	if ref.Key != "" {
		key := ref.Key
		result.Key = &key
	}
	return result
}

type BootstrapProviderConfig struct {
	Type      string `mapstructure:"type"`
	Format    string `mapstructure:"format"`
	BasePath  string `mapstructure:"base_path"`
	EnvPrefix string `mapstructure:"env_prefix"`
}

type BootstrapResolver struct {
	resolver *secretmaterial.Resolver
	ref      secretmaterial.SecretRef
	owner    string
	keyID    string
	closer   io.Closer
}

func NewBootstrapResolver(cfg BootstrapProviderConfig, owner string, ref Ref, keyID string, observer secretmaterial.Observer) (*BootstrapResolver, error) {
	binding := ref.SecretRef()
	if owner == "" {
		return nil, secretmaterial.ErrForbidden
	}
	if err := secretmaterial.ValidateSecretRef(binding, ""); err != nil {
		return nil, err
	}
	format := secretmaterial.Format(cfg.Format)
	if format == "" {
		format = secretmaterial.FormatRaw
	}
	if format != secretmaterial.FormatRaw && format != secretmaterial.FormatJSONRecord {
		return nil, secretmaterial.ErrUnsupportedType
	}
	if format == secretmaterial.FormatRaw && (binding.Key == nil || strings.TrimSpace(keyID) == "") {
		return nil, secretmaterial.ErrInvalidRef
	}
	if format == secretmaterial.FormatJSONRecord && binding.Key != nil {
		return nil, secretmaterial.ErrInvalidRef
	}
	var provider secretmaterial.Provider
	var err error
	switch cfg.Type {
	case ProviderFile:
		provider, err = secretmaterial.NewFileProvider(cfg.BasePath, format)
	case ProviderEnvironment:
		var variable string
		variable, err = secretmaterial.BootstrapEnvironmentVariable(cfg.EnvPrefix, binding)
		if err == nil {
			provider, err = secretmaterial.NewEnvironmentProvider(format, []secretmaterial.EnvBinding{{
				Scope: secretmaterial.Scope{Tier: secretmaterial.TierBootstrap},
				Name:  ref.Name, Key: ref.Key, Variable: variable,
			}})
		}
	default:
		return nil, secretmaterial.ErrUnsupportedType
	}
	if err != nil {
		return nil, err
	}
	closer, _ := provider.(io.Closer)
	resolver, err := secretmaterial.NewBootstrapResolver(provider, secretmaterial.BootstrapBinding{Owner: owner, Ref: binding}, observer)
	if err != nil {
		if closer != nil {
			err = errors.Join(err, closer.Close())
		}
		return nil, err
	}
	return &BootstrapResolver{resolver: resolver, ref: binding, owner: owner, keyID: keyID, closer: closer}, nil
}

func (r *BootstrapResolver) Resolve(ctx context.Context) (secretmaterial.SecretMaterial, error) {
	return r.resolver.ResolveSecret(ctx, r.ref, secretmaterial.ResolutionRequest{Principal: r.owner})
}

func (r *BootstrapResolver) Close() error {
	if r.closer != nil {
		return r.closer.Close()
	}
	return nil
}
