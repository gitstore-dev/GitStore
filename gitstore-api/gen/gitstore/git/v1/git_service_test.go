// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitv1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
)

// T009: ReceivePackRequest with push_context on first chunk encodes/decodes correctly.
// Subsequent chunks must NOT carry push_context (proto3 zero-value = omitted on wire).
func TestReceivePackRequest_PushContextFirstChunkOnly(t *testing.T) {
	pushCtx := &gitv1.PushContext{
		Namespace:             "gitstore-test",
		RepositoryName:        "catalog",
		RepositoryId:          "01960000-0000-7000-8000-000000000001",
		ConfigResourceVersion: "rv-42",
		Actor: &gitv1.AuthContext{
			Subject:    "admin",
			Issuer:     "static-admin",
			AuthMethod: "basic",
			Roles:      []string{"admin"},
		},
		Policy: &gitv1.PushPolicy{
			MaxPackSizeBytes: 0, // unlimited
			MaxFileSizeBytes: 0, // unlimited
		},
	}

	// First chunk: carries repository_id, ref_commands, pack_data, and push_context.
	first := &gitv1.ReceivePackRequest{
		RepositoryId: "01960000-0000-7000-8000-000000000001",
		RefCommands: []*gitv1.RefCommand{
			{
				OldOid:  "0000000000000000000000000000000000000000",
				NewOid:  "abc123def456abc123def456abc123def456abc1",
				RefName: "refs/heads/main",
			},
		},
		PackData:    []byte("PACK"),
		IsLast:      false,
		PushContext: pushCtx,
	}

	// Encode first chunk.
	raw, err := proto.Marshal(first)
	require.NoError(t, err)

	// Decode and verify round-trip.
	var decoded gitv1.ReceivePackRequest
	require.NoError(t, proto.Unmarshal(raw, &decoded))

	assert.Equal(t, first.RepositoryId, decoded.RepositoryId)
	require.NotNil(t, decoded.PushContext)
	assert.Equal(t, "admin", decoded.PushContext.Actor.Subject)
	assert.Equal(t, "gitstore-test", decoded.PushContext.Namespace)
	assert.Equal(t, int64(0), decoded.PushContext.Policy.MaxPackSizeBytes)
	assert.Equal(t, int64(0), decoded.PushContext.Policy.MaxFileSizeBytes)

	// Subsequent chunk: only pack_data and is_last — no push_context.
	subsequent := &gitv1.ReceivePackRequest{
		PackData: []byte("more pack bytes"),
		IsLast:   true,
	}

	raw2, err := proto.Marshal(subsequent)
	require.NoError(t, err)

	var decoded2 gitv1.ReceivePackRequest
	require.NoError(t, proto.Unmarshal(raw2, &decoded2))

	assert.Nil(t, decoded2.PushContext, "subsequent chunk must not carry push_context")
	assert.True(t, decoded2.IsLast)
}

// T009: PushContext with non-zero policy limits encodes/decodes correctly.
func TestPushContext_PolicyLimitsRoundTrip(t *testing.T) {
	pc := &gitv1.PushContext{
		RepositoryId: "01960000-0000-7000-8000-000000000001",
		Policy: &gitv1.PushPolicy{
			MaxPackSizeBytes: 52428800, // 50 MiB
			MaxFileSizeBytes: 10485760, // 10 MiB
		},
	}

	raw, err := proto.Marshal(pc)
	require.NoError(t, err)

	var decoded gitv1.PushContext
	require.NoError(t, proto.Unmarshal(raw, &decoded))

	require.NotNil(t, decoded.Policy)
	assert.Equal(t, int64(52428800), decoded.Policy.MaxPackSizeBytes)
	assert.Equal(t, int64(10485760), decoded.Policy.MaxFileSizeBytes)
}
