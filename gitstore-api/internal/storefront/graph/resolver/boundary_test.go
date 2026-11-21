// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestStorefrontDecoderHasClosedKinds(t *testing.T) {
	for _, kind := range []string{"Product", "Repository", "StorefrontProduct", "Unknown"} {
		t.Run(kind, func(t *testing.T) {
			id := base64.StdEncoding.EncodeToString([]byte("gid://GitStore/" + kind + "/stable-uid"))
			uid, err := DecodeNodeID(id)
			if kind == "StorefrontProduct" {
				require.NoError(t, err)
				require.Equal(t, "stable-uid", uid)
			} else {
				var structured *gqlerror.Error
				require.ErrorAs(t, err, &structured)
				require.Equal(t, "NOT_FOUND", structured.Extensions["code"])
			}
		})
	}
	_, err := DecodeNodeID("malformed")
	var structured *gqlerror.Error
	require.ErrorAs(t, err, &structured)
	require.Equal(t, "BAD_USER_INPUT", structured.Extensions["code"])
	r := (&Resolver{}).Query()
	value, err := r.Node(context.Background(), base64.StdEncoding.EncodeToString([]byte("gid://GitStore/StorefrontProduct/stable-uid")))
	require.Nil(t, value)
	require.Error(t, err, "identity-only schema cannot produce a fake publication read")
}
