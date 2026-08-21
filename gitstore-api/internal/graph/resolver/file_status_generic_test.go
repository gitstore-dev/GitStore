package resolver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestUpdateResourceStatusFileResolvedAndConflict(t *testing.T) {
	store, err := memdb.New()
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.CreateFile(context.Background(), &datastore.File{
		UID: uuid.New().String(), Namespace: "ns", Name: "hero", Kind: "File",
		APIVersion: "storage.gitstore.dev/v1beta1", ResourceVersion: "1",
		Status: json.RawMessage(`{"conditions":[]}`),
	}))
	r, err := NewResolver(ResolverDeps{Store: store, Logger: zap.NewNop()})
	require.NoError(t, err)
	mr := &mutationResolver{Resolver: r}
	resolved := map[string]any{"resolvedVariants": []any{map[string]any{"name": "thumb", "url": "https://cdn/thumb"}}}
	got, err := mr.UpdateResourceStatus(context.Background(), model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "ns", Name: "hero", ResourceVersion: "1", Resolved: resolved,
	})
	require.NoError(t, err)
	require.NotNil(t, got.Object)
	current, err := store.GetFileByName(context.Background(), "ns", "hero")
	require.NoError(t, err)
	require.Equal(t, "2", current.ResourceVersion)
	require.Contains(t, string(current.Status), "thumb")

	conflict, err := mr.UpdateResourceStatus(context.Background(), model.UpdateResourceStatusInput{
		Kind: "File", Namespace: "ns", Name: "hero", ResourceVersion: "1",
	})
	require.NoError(t, err)
	require.NotNil(t, conflict.Conflict)
	require.Equal(t, "2", conflict.Conflict.CurrentResourceVersion)
}
