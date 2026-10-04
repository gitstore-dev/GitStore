// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package secretmaterial provides pure reference validation and bounded local
// material resolution. Service authorization, logging and signing stay local.
package secretmaterial

import (
	"context"
	"time"
)

const (
	MaxEncodedBytes = 256 * 1024
	MaxDecodedBytes = 128 * 1024
	MaxItemBytes    = 64 * 1024
	MaxItems        = 32
	MaxInflight     = 16
	ResolutionLimit = 2 * time.Second
)

type SecretRef struct {
	Kind      string  `json:"kind" yaml:"kind"`
	Name      string  `json:"name" yaml:"name"`
	Key       *string `json:"key,omitempty" yaml:"key,omitempty"`
	Namespace *string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
}

type CredentialsRef struct {
	Kind      string    `json:"kind" yaml:"kind"`
	Type      string    `json:"type" yaml:"type"`
	SecretRef SecretRef `json:"secretRef" yaml:"secretRef"`
}

type Format string

const (
	FormatRaw        Format = "raw"
	FormatJSONRecord Format = "json-record"
)

type ProviderCategory string

const (
	ProviderFile ProviderCategory = "file"
	ProviderEnv  ProviderCategory = "env"
)

type Tier string

const (
	TierBootstrap Tier = "bootstrap"
	TierRuntime   Tier = "runtime"
)

// Scope is selected by the constructor, never by authored reference metadata.
type Scope struct {
	Tier        Tier
	Environment string
	Namespace   string
}

type ResourceIdentity struct {
	Kind       string
	Namespace  string
	Repository string
	Name       string
	UID        string
}

type ResolutionRequest struct {
	Principal string
	Resource  ResourceIdentity
}

// Provider implementations must bound reads and honor cancellation themselves;
// the resolver never starts a goroutine to mask an indefinitely blocked read.
type Provider interface {
	Read(context.Context, SecretRef, Scope) ([]byte, error)
	Category() ProviderCategory
	Format() Format
}

type Authorizer func(context.Context, ResolutionRequest, SecretRef) error

type BootstrapBinding struct {
	Owner string
	Ref   SecretRef
}

type RuntimeBinding struct {
	Environment string
	Namespace   string
	Authorize   Authorizer
}

// Observation dimensions are fixed by the resolver rather than request fields.
type Observation struct {
	Consumer string
	Purpose  string
	Tier     Tier
	Provider ProviderCategory
}

type Observer interface {
	Inflight(Observation, int)
	Observe(Observation, string, time.Duration)
}

type SecretResolver interface {
	ResolveSecret(context.Context, SecretRef, ResolutionRequest) (SecretMaterial, error)
}

type SecretMaterial struct {
	values map[string][]byte
	format string
}
