// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestActorPaginationUsesNativePageInfoAndReturnsEmptyTerminalPageWithoutFollowingLinks(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("page_token") != "" {
			require.Equal(t, id, r.URL.Query().Get("page_token"))
			json.NewEncoder(w).Encode([]any{})
			return
		}
		w.Header().Set("Link", `<https://untrusted.invalid/never-follow>; rel="next"`)
		record := identityFixture()
		record["id"] = id
		json.NewEncoder(w).Encode([]any{record})
	}))
	defer server.Close()
	p, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	first := 1
	page, err := p.ListActors(t.Context(), auth.ActorPageRequest{First: &first})
	require.NoError(t, err)
	require.True(t, page.HasNextPage)
	require.False(t, page.HasPreviousPage)
	require.NotNil(t, page.EndCursor)
	terminal, err := p.ListActors(t.Context(), auth.ActorPageRequest{First: &first, After: page.EndCursor})
	require.NoError(t, err)
	require.Empty(t, terminal.Edges)
	require.Nil(t, terminal.StartCursor)
	require.Nil(t, terminal.EndCursor)
	require.False(t, terminal.HasNextPage)
	require.True(t, terminal.HasPreviousPage)
	require.Equal(t, 2, calls)
}

func TestActorPagesResumeFromEveryEdgeAndRejectForeignCursors(t *testing.T) {
	const firstID = "00000000-0000-0000-0000-000000000001"
	const secondID = "00000000-0000-0000-0000-000000000002"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.Equal(t, "2", r.URL.Query().Get("page_size"))
		require.Empty(t, r.URL.Query().Get("page"))
		require.Empty(t, r.URL.Query().Get("include_credential"))
		ids := []string{firstID, secondID}
		if token := r.URL.Query().Get("page_token"); token != "" {
			require.Equal(t, firstID, token)
			ids = ids[1:]
		}
		records := make([]map[string]any, 0, len(ids))
		for _, id := range ids {
			record := identityFixture()
			record["id"] = id
			records = append(records, record)
		}
		json.NewEncoder(w).Encode(records)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	first := 2
	page, err := provider.ListActors(t.Context(), auth.ActorPageRequest{First: &first})
	require.NoError(t, err)
	require.Len(t, page.Edges, 2)
	require.NotEqual(t, page.Edges[0].Cursor, page.Edges[1].Cursor)
	next, err := provider.ListActors(t.Context(), auth.ActorPageRequest{First: &first, After: &page.Edges[0].Cursor})
	require.NoError(t, err)
	require.Len(t, next.Edges, 1)
	require.Equal(t, secondID, next.Edges[0].Profile.Subject)
	require.True(t, next.HasPreviousPage)
	require.False(t, next.HasNextPage)
	foreign, err := New(directoryConfig(server.URL + "/other"))
	require.NoError(t, err)
	_, err = foreign.ListActors(t.Context(), auth.ActorPageRequest{After: page.StartCursor})
	require.ErrorIs(t, err, ErrInvalidInput)
	_, err = provider.ListActors(t.Context(), auth.ActorPageRequest{Last: &first})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	_, err = provider.ListActors(t.Context(), auth.ActorPageRequest{Before: page.StartCursor})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	bad := "invalid"
	_, err = provider.ListActors(t.Context(), auth.ActorPageRequest{After: &bad})
	require.ErrorIs(t, err, ErrInvalidInput)
	require.Equal(t, 2, calls, "invalid and unsupported requests must not call Kratos")
}
