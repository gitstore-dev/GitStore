// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type FileProvider struct {
	root   *os.Root
	format Format
}

func NewFileProvider(basePath string, format Format) (*FileProvider, error) {
	if basePath == "" {
		return nil, failure(ErrInvalidRef, "base-path", nil)
	}
	if format != FormatRaw && format != FormatJSONRecord {
		return nil, failure(ErrUnsupportedType, "format", nil)
	}
	path, err := filepath.EvalSymlinks(basePath)
	if err != nil {
		return nil, fileFailure(err, ErrProviderUnavailable)
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fileFailure(err, ErrProviderUnavailable)
	}
	return &FileProvider{root: root, format: format}, nil
}

func (p *FileProvider) Category() ProviderCategory { return ProviderFile }
func (p *FileProvider) Format() Format             { return p.format }

func (p *FileProvider) Close() error {
	if err := p.root.Close(); err != nil {
		return fileFailure(err, ErrProviderUnavailable)
	}
	return nil
}

func validateScope(scope Scope, ref SecretRef) error {
	switch scope.Tier {
	case TierBootstrap:
		if scope.Environment != "" || scope.Namespace != "" {
			return failure(ErrForbidden, "scope", nil)
		}
	case TierRuntime:
		if !validName(scope.Environment) || !validName(scope.Namespace) {
			return failure(ErrInvalidRef, "scope", nil)
		}
	default:
		return failure(ErrForbidden, "tier", nil)
	}
	return ValidateSecretRef(ref, scope.Namespace)
}

func (p *FileProvider) Read(ctx context.Context, ref SecretRef, scope Scope) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, failure(ErrProviderUnavailable, "context", err)
	}
	if err := validateScope(scope, ref); err != nil {
		return nil, err
	}
	prefix := ""
	if scope.Tier == TierRuntime {
		prefix = filepath.Join(scope.Environment, scope.Namespace)
	}
	path := filepath.Join(prefix, ref.Name+".json")
	missing := ErrNotFound
	if p.format == FormatRaw {
		if ref.Key == nil {
			return nil, failure(ErrInvalidRef, "key", nil)
		}
		dir := filepath.Join(prefix, ref.Name)
		info, err := p.root.Stat(dir)
		if err != nil {
			return nil, fileFailure(err, ErrNotFound)
		}
		if !info.IsDir() {
			return nil, failure(ErrNotFound, "record", nil)
		}
		path, missing = filepath.Join(dir, *ref.Key), ErrMissingKey
	}
	// Root pins containment across path replacement. Nonblocking open prevents
	// a swapped-in FIFO from blocking before the descriptor's type is checked.
	f, err := p.root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fileFailure(err, missing)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fileFailure(err, ErrProviderUnavailable)
	}
	if !info.Mode().IsRegular() {
		return nil, failure(ErrForbidden, "file-type", nil)
	}
	if info.Size() > MaxEncodedBytes {
		return nil, failure(ErrValueTooLarge, "encoded-bytes", nil)
	}
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: f}, MaxEncodedBytes+1))
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		clear(data)
		return nil, failure(ErrProviderUnavailable, "read", err)
	}
	if len(data) > MaxEncodedBytes {
		clear(data)
		return nil, failure(ErrValueTooLarge, "encoded-bytes", nil)
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

func fileFailure(err error, missing error) error {
	class := ErrProviderUnavailable
	switch {
	case errors.Is(err, os.ErrNotExist):
		class = missing
	case errors.Is(err, os.ErrPermission), errors.Is(err, syscall.ELOOP):
		class = ErrForbidden
	default:
		// Root's traversal denials are not syscall errors and have no exported
		// sentinel. Keep them closed without matching unstable error strings.
		var errno syscall.Errno
		if !errors.As(err, &errno) && !errors.Is(err, os.ErrClosed) {
			class = ErrForbidden
		}
	}
	return failure(class, "file", err)
}
