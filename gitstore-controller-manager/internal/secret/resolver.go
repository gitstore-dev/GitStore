// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package secret resolves bootstrap-tier process identity material.
package secret

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"
)

const (
	// ProviderFile resolves key material from a mounted directory.
	ProviderFile = "file"
	// ProviderEnvironment resolves key material from process environment.
	ProviderEnvironment = "env"
)

var (
	// ErrInvalidRef identifies a malformed logical secret reference.
	ErrInvalidRef = errors.New("secret: InvalidRef")
	// ErrNotFound identifies a missing logical secret.
	ErrNotFound = errors.New("secret: NotFound")
	// ErrMissingKey identifies a missing or empty key in an existing secret.
	ErrMissingKey = errors.New("secret: MissingKey")
	// ErrForbidden identifies material that is outside the configured boundary
	// or cannot be read by this process.
	ErrForbidden = errors.New("secret: Forbidden")
	// ErrProviderUnavailable identifies an unavailable or unreadable provider.
	ErrProviderUnavailable = errors.New("secret: ProviderUnavailable")

	refComponentPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,252}$`)
	envPrefixPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// Ref is the bootstrap-safe subset of ADR 0001's SecretRef. Bootstrap
// references are deployment configuration, so they have no resource namespace.
type Ref struct {
	Kind string `mapstructure:"kind"`
	Name string `mapstructure:"name"`
	Key  string `mapstructure:"key"`
}

// BootstrapProviderConfig selects a local bootstrap provider. BasePath is used
// by ProviderFile; EnvPrefix is used by ProviderEnvironment.
type BootstrapProviderConfig struct {
	Type      string `mapstructure:"type"`
	BasePath  string `mapstructure:"base_path"`
	EnvPrefix string `mapstructure:"env_prefix"`
}

// Resolver resolves one value from a logical bootstrap secret reference.
// Implementations never return a fallback value on failure.
type Resolver interface {
	Resolve(context.Context, Ref) ([]byte, error)
}

// ResolutionError carries a stable ADR 0001 error class without exposing
// secret material. Callers should use errors.Is with one of Err* above.
type ResolutionError struct {
	Class error
	Ref   Ref
	cause error
}

func (e *ResolutionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Class.Error()
}

func (e *ResolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Class
}

func newResolutionError(class error, ref Ref, cause error) error {
	return &ResolutionError{Class: class, Ref: ref, cause: cause}
}

type provider interface {
	Resolve(context.Context, Ref) ([]byte, error)
}

type bootstrapResolver struct {
	provider provider
	logger   *zap.Logger
}

// NewBootstrapResolver constructs the restricted, local bootstrap resolver
// required for process identity. It deliberately supports no network provider.
func NewBootstrapResolver(config BootstrapProviderConfig, logger *zap.Logger) (Resolver, error) {
	var (
		p   provider
		err error
	)

	switch strings.ToLower(strings.TrimSpace(config.Type)) {
	case ProviderFile:
		p, err = newFileProvider(config.BasePath)
	case ProviderEnvironment:
		p, err = newEnvironmentProvider(config.EnvPrefix)
	default:
		return nil, fmt.Errorf("secret: unsupported bootstrap provider type %q", config.Type)
	}
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &bootstrapResolver{provider: p, logger: logger}, nil
}

func (r *bootstrapResolver) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateRef(ref); err != nil {
		return nil, err
	}
	value, err := r.provider.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	if len(value) == 0 {
		return nil, newResolutionError(ErrMissingKey, ref, nil)
	}

	// Only logical identifiers are logged; resolved bytes are never logged.
	r.logger.Debug("resolved bootstrap secret",
		zap.String("secret_name", ref.Name),
		zap.String("secret_key", ref.Key),
	)
	return append([]byte(nil), value...), nil
}

func validateRef(ref Ref) error {
	if ref.Kind != "SecretRef" ||
		!refComponentPattern.MatchString(ref.Name) ||
		!refComponentPattern.MatchString(ref.Key) {
		return newResolutionError(ErrInvalidRef, ref, nil)
	}
	return nil
}

type fileProvider struct {
	basePath string
}

func newFileProvider(basePath string) (*fileProvider, error) {
	if strings.TrimSpace(basePath) == "" {
		return nil, fmt.Errorf("secret: file bootstrap provider requires base_path")
	}
	absolutePath, err := filepath.Abs(basePath)
	if err != nil {
		return nil, newResolutionError(ErrProviderUnavailable, Ref{}, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrProviderUnavailable), Ref{}, err)
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrProviderUnavailable), Ref{}, err)
	}
	if !info.IsDir() {
		return nil, newResolutionError(ErrProviderUnavailable, Ref{}, nil)
	}
	return &fileProvider{basePath: resolvedPath}, nil
}

func (p *fileProvider) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	secretDir, err := filepath.EvalSymlinks(filepath.Join(p.basePath, ref.Name))
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrNotFound), ref, err)
	}
	if !pathWithin(p.basePath, secretDir) {
		return nil, newResolutionError(ErrForbidden, ref, nil)
	}
	info, err := os.Stat(secretDir)
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrNotFound), ref, err)
	}
	if !info.IsDir() {
		return nil, newResolutionError(ErrNotFound, ref, nil)
	}

	keyPath, err := filepath.EvalSymlinks(filepath.Join(secretDir, ref.Key))
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrMissingKey), ref, err)
	}
	if !pathWithin(p.basePath, keyPath) {
		return nil, newResolutionError(ErrForbidden, ref, nil)
	}
	info, err = os.Stat(keyPath)
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrMissingKey), ref, err)
	}
	if !info.Mode().IsRegular() {
		return nil, newResolutionError(ErrMissingKey, ref, nil)
	}

	value, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, newResolutionError(classifyFilesystemError(err, ErrProviderUnavailable), ref, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}

func pathWithin(basePath, candidate string) bool {
	relative, err := filepath.Rel(basePath, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func classifyFilesystemError(err error, fallback error) error {
	if errors.Is(err, os.ErrPermission) {
		return ErrForbidden
	}
	if errors.Is(err, os.ErrNotExist) {
		return fallback
	}
	return ErrProviderUnavailable
}

type environmentProvider struct {
	prefix string
}

func newEnvironmentProvider(prefix string) (*environmentProvider, error) {
	if !envPrefixPattern.MatchString(prefix) {
		return nil, fmt.Errorf("secret: environment bootstrap provider requires uppercase env_prefix")
	}
	return &environmentProvider{prefix: prefix}, nil
}

func (p *environmentProvider) Resolve(ctx context.Context, ref Ref) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, ok := os.LookupEnv(EnvironmentVariableName(p.prefix, ref))
	if !ok {
		return nil, newResolutionError(ErrNotFound, ref, nil)
	}
	if value == "" {
		return nil, newResolutionError(ErrMissingKey, ref, nil)
	}
	return []byte(value), nil
}

// EnvironmentVariableName returns the deterministic environment variable for
// an environment-provider reference. Its reversible component escaping avoids
// collisions between dots, dashes, and underscores in logical identifiers.
func EnvironmentVariableName(prefix string, ref Ref) string {
	return prefix + environmentComponent(ref.Name) + "__" + environmentComponent(ref.Key)
}

func environmentComponent(value string) string {
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
			builder.WriteRune(unicodeToUpperASCII(char))
		}
	}
	return builder.String()
}

func unicodeToUpperASCII(char rune) rune {
	if char >= 'a' && char <= 'z' {
		return char - ('a' - 'A')
	}
	return char
}
