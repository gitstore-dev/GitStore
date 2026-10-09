// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// denyCategoryTitlePolicy denies any CategoryTaxonomy whose title is "Denied".
type denyCategoryTitlePolicy struct{}

func (denyCategoryTitlePolicy) Name() string { return "test-deny-category-title" }

func (denyCategoryTitlePolicy) Validate(_ context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if req.Kind != "CategoryTaxonomy" {
		return admission.DecisionAllow()
	}
	if res, ok := req.Object.(*catalog.CategoryTaxonomyResource); ok && res.Spec.Title == "Denied" {
		return admission.DecisionDeny("title Denied is not allowed", "spec.title")
	}
	return admission.DecisionAllow()
}

type failingCategoryWriteStore struct {
	datastore.Datastore
}

func (failingCategoryWriteStore) CreateCategoryTaxonomy(context.Context, *datastore.CategoryTaxonomy) error {
	return errors.New("store unavailable")
}

func categoryManifest(name, title string) string {
	return "---\n" +
		"apiVersion: catalog.gitstore.dev/v1beta1\n" +
		"kind: CategoryTaxonomy\n" +
		"metadata:\n" +
		"  name: " + name + "\n" +
		"  namespace: gitstore\n" +
		"spec:\n" +
		"  title: " + title + "\n" +
		"---\n"
}

func newDecisionServer(t *testing.T, store datastore.Datastore) *Server {
	t.Helper()
	srv, err := NewServer(ServerDeps{
		Store:                   store,
		Logger:                  zap.NewNop(),
		ExtraValidatingPolicies: []admission.ValidatingAdmissionPolicy{denyCategoryTitlePolicy{}},
	})
	require.NoError(t, err)
	return srv
}

func admitCategoryForTest(t *testing.T, srv *Server, content string) admission.EntryDecision {
	t.Helper()
	ctx := context.Background()
	parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader([]byte(content)))
	require.NoError(t, err)
	category := parsed.CategoryTaxonomy
	existing, lookupErr := srv.store.GetCategoryTaxonomyByName(ctx, "gitstore", category.Metadata.Name)
	var raw any
	op := admission.OperationCreate
	if lookupErr == nil {
		raw, op = existing, admission.OperationUpdate
	}
	admCtx := AdmissionContext{
		RepositoryID: "00000000-0000-0000-0000-000000000001",
		Namespace:    "gitstore",
		ActorSubject: "alice",
		CommitSHA:    strings.Repeat("a", 40),
		RefName:      "refs/heads/main",
		Revision:     "main@sha1:" + strings.Repeat("a", 40),
		Now:          time.Now().UTC(),
	}
	return srv.admitCategoryTaxonomyWithContext(ctx, category, body, admCtx, "categories/"+category.Metadata.Name+".md", op, raw, map[string]string{}, nil)
}

func TestAdmitCategoryTaxonomyReturnsEntryDecision(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryAccepted, decision.Outcome)
		assert.Equal(t, "CategoryTaxonomy", decision.Kind)
		assert.Equal(t, "gitstore", decision.Namespace)
		assert.Equal(t, "electronics", decision.Name)
		assert.Equal(t, "categories/electronics.md", decision.Path)
	})

	t.Run("no-op on identical re-admission", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		require.Equal(t, admission.EntryAccepted, admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics")).Outcome)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryNoOp, decision.Outcome)
	})

	t.Run("denied with diagnostics", func(t *testing.T) {
		store, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, store)
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Denied"))
		assert.Equal(t, admission.EntryDenied, decision.Outcome)
		require.Len(t, decision.Diagnostics, 1)
		assert.Equal(t, "POLICY_DENIED", decision.Diagnostics[0].Reason)
		assert.Equal(t, "title Denied is not allowed", decision.Diagnostics[0].Message)
		assert.Equal(t, "categories/electronics.md", decision.Diagnostics[0].File)
		assert.Equal(t, "spec.title", decision.Diagnostics[0].Field)
		assert.Equal(t, admission.LevelFailure, decision.Diagnostics[0].Level)
		_, err = store.GetCategoryTaxonomyByName(context.Background(), "gitstore", "electronics")
		assert.ErrorIs(t, err, datastore.ErrNotFound, "a denied create leaves no record")
	})

	t.Run("failed when the store write fails", func(t *testing.T) {
		base, err := memdb.New()
		require.NoError(t, err)
		srv := newDecisionServer(t, failingCategoryWriteStore{Datastore: base})
		decision := admitCategoryForTest(t, srv, categoryManifest("electronics", "Electronics"))
		assert.Equal(t, admission.EntryFailed, decision.Outcome)
		assert.ErrorContains(t, decision.Err, "store unavailable")
	})
}
