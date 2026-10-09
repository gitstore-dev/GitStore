// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/graph/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// secondReplica builds another API replica over the same datastore and Git
// service. Replicas share no process state.
func (e *categoryLifecycleEnv) secondReplica(t *testing.T) *Service {
	t.Helper()
	catalogServer, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{Store: e.store, GitReader: e.git, Logger: zap.NewNop()})
	require.NoError(t, err)
	service, err := NewService(ServiceDeps{Store: e.store, GitWriter: e.git, Logger: zap.NewNop(), CommittedManifestAdmitter: catalogServer})
	require.NoError(t, err)
	return service
}

// Two replicas creating the same category concurrently: at most one success,
// and the loser reports ALREADY_EXISTS or CONFLICT, never another writer's
// record as its own.
func TestConcurrentCategoryCreateOnTwoReplicas(t *testing.T) {
	for run := range 25 {
		t.Run(fmt.Sprint(run), func(t *testing.T) {
			env := newCategoryLifecycleEnv(t)
			replicas := []*Service{env.service, env.secondReplica(t)}
			results := make([]*datastore.CategoryTaxonomy, len(replicas))
			errs := make([]error, len(replicas))
			var wg sync.WaitGroup
			for i, replica := range replicas {
				wg.Add(1)
				go func() {
					defer wg.Done()
					results[i], errs[i] = replica.CommitCategoryManifest(context.Background(),
						categoryInput("electronics", fmt.Sprintf("Electronics %d", i), ""), "alice", true)
				}()
			}
			wg.Wait()

			successes := 0
			for i, err := range errs {
				if err == nil {
					successes++
					assert.Equal(t, fmt.Sprintf("Electronics %d", i), mustCategoryTitle(t, results[i]), "a success returns its own content")
					continue
				}
				var admissionErr *admission.Error
				require.True(t, errors.As(err, &admissionErr), "got %v", err)
				assert.Contains(t, []admission.Code{admission.CodeAlreadyExists, admission.CodeConflict}, admissionErr.Code, "%s %+v", admissionErr.Phase, admissionErr.Diagnostics)
			}
			assert.LessOrEqual(t, successes, 1)
		})
	}
}

func mustCategoryTitle(t *testing.T, c *datastore.CategoryTaxonomy) string {
	t.Helper()
	converted := DatastoreCategoryTaxonomyToGraphQL(c)
	require.NotNil(t, converted.Spec)
	return converted.Spec.Title
}

// Status writes for one category from two replicas: the optimistic version
// check admits one writer per resource version, and the ancestor index
// always follows the last accepted write.
func TestConcurrentCategoryStatusWritesConvergeTheIndex(t *testing.T) {
	env := newCategoryLifecycleEnv(t)
	ctx := context.Background()
	env.pushCategory(t, lifecycleSystemRepoID, "categories/electronics.md", "electronics", "Electronics", "", "")
	env.pushCategory(t, lifecycleSystemRepoID, "categories/office.md", "office", "Office", "", "")
	laptops := env.pushCategory(t, lifecycleSystemRepoID, "categories/laptops.md", "laptops", "Laptops", "", "")

	write := func(rv string, path ...string) error {
		_, err := env.store.UpdateCategoryTaxonomyStatus(ctx, lifecycleNamespace, "laptops", datastore.CategoryTaxonomyStatusPatch{
			ResourceVersion: rv, Resolved: &catalog.ResolvedCategoryTaxonomy{Path: path, Depth: int8(len(path) - 1)},
		})
		return err
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, parent := range []string{"electronics", "office"} {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = write(laptops.ResourceVersion, parent, "laptops") }()
	}
	wg.Wait()
	conflicts := 0
	for _, err := range errs {
		if errors.Is(err, datastore.ErrConflict) {
			conflicts++
		} else {
			require.NoError(t, err)
		}
	}
	require.Equal(t, 1, conflicts, "exactly one writer wins a resource version")

	current, err := env.store.GetCategoryTaxonomyByName(ctx, lifecycleNamespace, "laptops")
	require.NoError(t, err)
	winner := "electronics"
	if !containsString(mustResolvedPath(t, current), "electronics") {
		winner = "office"
	}
	loser := map[string]string{"electronics": "office", "office": "electronics"}[winner]
	assertSubtree(t, env, winner, []string{"laptops"})
	assertSubtree(t, env, loser, nil)

	// The losing replica re-reads and retries at the current version.
	require.NoError(t, write(current.ResourceVersion, loser, "laptops"))
	assertSubtree(t, env, loser, []string{"laptops"})
	assertSubtree(t, env, winner, nil)
}

func mustResolvedPath(t *testing.T, c *datastore.CategoryTaxonomy) []string {
	t.Helper()
	path, err := datastore.CategoryResolvedPath(string(c.Status))
	require.NoError(t, err)
	return path
}

func assertSubtree(t *testing.T, env *categoryLifecycleEnv, ancestor string, want []string) {
	t.Helper()
	conn, err := env.service.listCategoryDescendants(context.Background(), lifecycleNamespace, &model.CategoryFilterInput{DescendantOf: ancestor}, datastore.PageParams{})
	require.NoError(t, err)
	got := categoryNames(conn)
	if len(want) == 0 {
		assert.Empty(t, got, "subtree of %s", ancestor)
		return
	}
	assert.Equal(t, want, got, "subtree of %s", ancestor)
}
