// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secret

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/secretmaterial"
)

func (r *BootstrapResolver) SigningKey(ctx context.Context) ([]byte, string, error) {
	material, err := r.Resolve(ctx)
	if err != nil {
		return nil, "", err
	}
	defer material.Clear()
	if material.RecordFormat() != "serviceaccount-signing-key/v1" {
		return nil, "", secretmaterial.ErrUnsupportedType
	}
	key := material.Value("privateKey")
	id := material.Value("keyID")
	defer clear(id)
	if len(key) == 0 || len(id) == 0 {
		clear(key)
		return nil, "", secretmaterial.ErrMissingKey
	}
	keyID := string(id)
	if !utf8.Valid(id) || strings.TrimSpace(keyID) == "" {
		clear(key)
		return nil, "", secretmaterial.ErrInvalidRef
	}
	return key, keyID, nil
}
