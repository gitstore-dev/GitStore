// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace_test

import (
	"errors"
	"testing"

	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/stretchr/testify/require"
)

func TestValidateIdentifier(t *testing.T) {
	require.NoError(t, namespaceadmission.ValidateIdentifier("acme-store"))
	require.ErrorIs(t, namespaceadmission.ValidateIdentifier("bad_name"), namespaceadmission.ErrInvalidIdentifier)
	require.ErrorIs(t, namespaceadmission.ValidateIdentifier("admin"), namespaceadmission.ErrReservedIdentifier)
	require.NoError(t, namespaceadmission.ValidateIdentifier("default"))
	require.NoError(t, namespaceadmission.ValidateIdentifier("gitstore-system"))
	require.False(t, errors.Is(namespaceadmission.ValidateIdentifier("default"), namespaceadmission.ErrReservedIdentifier))
}
