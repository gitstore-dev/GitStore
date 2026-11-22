// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTranslationRollingWritersRequireCompatibleActivation(t *testing.T) {
	first := newTranslationCallbackFixture(t)
	makeReplica := func(activation TranslationActivation) (*translationProjectionFixture, error) {
		server, err := NewServer(ServerDeps{
			Store: first.store, GitReader: first.git, Registry: first.server.registry,
			ServiceSecret: first.server.serviceSecret, Logger: first.server.log,
			TranslationActivation: activation,
		})
		if err != nil {
			return nil, err
		}
		replica := *first
		replica.server = server
		return &replica, nil
	}
	disabled, err := makeReplica(TranslationActivation{})
	require.NoError(t, err)
	staged, err := makeReplica(TranslationActivation{DescriptorDigest: translation.CapabilityDigest()})
	require.NoError(t, err)
	zero, commit := strings.Repeat("0", 40), first.admCtx.CommitSHA
	for _, replica := range []*translationProjectionFixture{disabled, staged} {
		err := replica.callback(t, zero, commit, first.entry.path)
		require.Equal(t, codes.Unavailable, status.Code(err))
		_, err = first.store.GetTranslationByName(first.ctx, first.entry.identity.Namespace, first.entry.identity.Name)
		require.ErrorIs(t, err, datastore.ErrNotFound)
	}
	for _, activation := range []TranslationActivation{
		{Enabled: true},
		{DescriptorDigest: "incompatible-build"},
		{Enabled: true, DescriptorDigest: "incompatible-build"},
	} {
		replica, err := makeReplica(activation)
		require.Error(t, err)
		require.Nil(t, replica)
	}

	require.NoError(t, first.callback(t, zero, commit, first.entry.path))
	accepted := first.source(t)
	for _, replica := range []*translationProjectionFixture{disabled, staged} {
		err := replica.callback(t, zero, commit, first.entry.path)
		require.Equal(t, codes.Unavailable, status.Code(err), "disabled replay consumers are not a post-activation rollback")
		require.Equal(t, accepted, first.source(t), "a rejected incapable replay must preserve accepted source and identity")
	}
	replacement := newTranslationCallbackReplica(t, first)
	require.NoError(t, replacement.callback(t, zero, commit, first.entry.path))
	require.Equal(t, accepted, replacement.source(t), "a capable rolling replacement must replay idempotently")
}
