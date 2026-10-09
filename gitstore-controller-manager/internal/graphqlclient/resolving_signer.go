// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/secretmaterial"
)

type SigningKeyResolver interface {
	SigningKey(context.Context) ([]byte, string, error)
	io.Closer
}

type ResolvingTokenSigner struct {
	resolver SigningKeyResolver
	uid      string
}

func NewResolvingTokenSigner(ctx context.Context, resolver SigningKeyResolver, uid string) (*ResolvingTokenSigner, error) {
	if resolver == nil || strings.TrimSpace(uid) == "" {
		return nil, secretmaterial.ErrInvalidRef
	}
	s := &ResolvingTokenSigner{resolver: resolver, uid: uid}
	key, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	key.clear()
	return s, nil
}

func (s *ResolvingTokenSigner) load(ctx context.Context) (*PrivateKeyTokenSigner, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, id, err := s.resolver.SigningKey(ctx)
	if err != nil {
		clear(key)
		return nil, err
	}
	return NewPrivateKeyTokenSigner(key, id, s.uid)
}

func (s *ResolvingTokenSigner) SignAssertion(ctx context.Context, namespace, name string, ttl time.Duration, audience string) (string, error) {
	key, err := s.load(ctx)
	if err != nil {
		return "", err
	}
	defer key.clear()
	return key.SignAssertion(ctx, namespace, name, ttl, audience)
}

func (s *ResolvingTokenSigner) Close() error { return s.resolver.Close() }
