// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"io"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/cataloggrpc"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/rbac"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTranslationValidationFailsClosedBeforeActivation(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	for _, deletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "upsert", true: "deletion"}[deletion], func(t *testing.T) {
			server := newCatalogServer(t, nil, nil)
			blob := &catalogv1.ResourceBlob{Path: "translations/laptop.md", Content: []byte(corpus.Valid["product-field"])}
			tree := &catalogv1.ResourceValidationTree{}
			if deletion {
				tree.OldBlobs = []*catalogv1.ResourceBlob{blob}
			} else {
				tree.ProposedBlobs = []*catalogv1.ResourceBlob{blob}
			}
			response, err := server.ValidateResources(t.Context(), &catalogv1.ValidateResourcesRequest{
				RepositoryId: testRepoID,
				Trees:        []*catalogv1.ResourceValidationTree{tree},
			})
			require.Nil(t, response)
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}

func TestTranslationManifestValidationFailsClosedBeforeActivation(t *testing.T) {
	server := newCatalogServer(t, nil, nil)
	corpus := loadTranslationFixtures(t)
	_, err := server.ValidateManifest(t.Context(), admission.ManifestValidationRequest{
		RepositoryID: testRepoID,
		Path:         "translations/laptop.md",
		NewContent:   []byte(corpus.Valid["product-field"]),
	})
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestTranslationDeletionValidationFailsClosedBeforeActivation(t *testing.T) {
	server := newCatalogServer(t, nil, nil)
	corpus := loadTranslationFixtures(t)
	response, err := server.ValidateResourceDeletions(t.Context(), &catalogv1.ValidateResourceDeletionsRequest{
		RepositoryId: testRepoID,
		Trees: []*catalogv1.ResourceValidationTree{{
			OldBlobs: []*catalogv1.ResourceBlob{{Path: "translations/laptop.md", Content: []byte(corpus.Valid["product-field"])}},
		}},
	})
	require.Nil(t, response)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

func TestTranslationCommittedManifestFailsClosedBeforeActivation(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	for _, operation := range []admission.Operation{admission.OperationCreate, admission.OperationUpdate, admission.OperationDelete} {
		t.Run(string(operation), func(t *testing.T) {
			server := newCatalogServer(t, nil, nil)
			var content []byte
			if operation != admission.OperationDelete {
				content = []byte(corpus.Valid["product-field"])
			}
			result, err := server.AdmitCommittedManifest(t.Context(), admission.CommittedManifestRequest{
				RepositoryID: testRepoID, ActorSubject: "alice", RefName: "refs/heads/main",
				CommitSHA: strings.Repeat("a", 40), Path: "translations/laptop.md",
				Kind: "Translation", Namespace: "gitstore", Name: "laptop-title-de", Content: content, Operation: operation,
			})
			require.Nil(t, result)
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}

func TestTranslationMixedReplayDoesNotPartiallyWrite(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	store := newTestDatastore(t)
	commit := strings.Repeat("a", 40)
	server := newCatalogServer(t, store, &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"00-product.md", "zz-translation.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			source := corpus.Valid["product-field"]
			if path == "00-product.md" {
				source = corpus.Targets["Product"]
			}
			return []byte(strings.ReplaceAll(source, "acme-store", "gitstore")), nil
		},
		resolveRefFunc: func(context.Context, string, string) (string, error) { return commit, nil },
	})
	response, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{RepositoryId: testRepoID, NewCommitSha: commit})
	require.Nil(t, response)
	require.Equal(t, codes.Unavailable, status.Code(err))
	_, err = store.GetProductByName(t.Context(), "gitstore", "laptop")
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestTranslationMixedReplayDoesNotProjectRBAC(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	f := newRBACProjectionFixture(t, nil)
	old := f.tip
	f.tip = strings.Repeat("b", 40)
	rolePath := "rbac/clusterroles/reader.yaml"
	f.trees[f.tip] = map[string][]byte{
		rolePath:                 rbacProjectionRole("reader", "read"),
		"translations/laptop.md": []byte(corpus.Valid["product-field"]),
	}
	err := f.receive(t, old, rolePath, "translations/laptop.md")
	require.Equal(t, codes.Unavailable, status.Code(err))
	_, err = f.store.GetRBAC(t.Context(), rbac.ResourceKey{TierID: rbac.ClusterTierID, Kind: rbac.ClusterRole, Name: "reader"})
	require.ErrorIs(t, err, datastore.ErrNotFound, "activation must be checked before the first projection writer")
}

func TestTranslationSupersededReplayStillFailsClosed(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	requested, current := strings.Repeat("a", 40), strings.Repeat("c", 40)
	server := newCatalogServer(t, nil, &mockGitReader{
		readFileFunc: func(_ context.Context, _, _, ref string) ([]byte, error) {
			if ref == requested {
				return []byte(corpus.Valid["product-field"]), nil
			}
			return nil, status.Error(codes.NotFound, "fixture absent")
		},
		resolveRefFunc: func(context.Context, string, string) (string, error) { return current, nil },
	})
	response, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, NewCommitSha: requested, ChangedPaths: []string{"translations/laptop.md"},
	})
	require.Nil(t, response)
	require.Equal(t, codes.Unavailable, status.Code(err))
}

type unsupportedTranslationParser struct{}

func (unsupportedTranslationParser) ParseResource(io.Reader) (*validate.ParsedResource, []byte, error) {
	return nil, nil, nil
}

func TestTranslationGateDoesNotDependOnParserCapability(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	for _, parser := range []cataloggrpc.ResourceParser{unsupportedTranslationParser{}, validate.NewParser()} {
		server := newCatalogServer(t, nil, nil, func(deps *cataloggrpc.ServerDeps) {
			deps.Parser = parser
		})
		response, err := server.ValidateResources(t.Context(), &catalogv1.ValidateResourcesRequest{
			RepositoryId: testRepoID,
			Trees: []*catalogv1.ResourceValidationTree{{
				ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "translations/laptop.md", Content: []byte(corpus.Valid["product-field"])}},
			}},
		})
		require.Nil(t, response)
		require.Equal(t, codes.Unavailable, status.Code(err))
	}
}

func TestTranslationActivationRejectsInconsistentConfiguration(t *testing.T) {
	for _, activation := range []cataloggrpc.TranslationActivation{
		{Enabled: true},
		{DescriptorDigest: "different-binary"},
		{Enabled: true, DescriptorDigest: "different-binary"},
	} {
		server, err := cataloggrpc.NewServer(cataloggrpc.ServerDeps{
			Store: newTestDatastore(t), Logger: zap.NewNop(), TranslationActivation: activation,
		})
		require.Error(t, err, "enabled writers require an exact semantic capability pin")
		require.Nil(t, server)
	}
	newCatalogServer(t, nil, nil, func(deps *cataloggrpc.ServerDeps) {
		deps.TranslationActivation.DescriptorDigest = translation.CapabilityDigest()
	})
}

func TestTranslationReplayFailsClosedBeforeActivation(t *testing.T) {
	corpus := loadTranslationFixtures(t)
	commit := strings.Repeat("a", 40)
	for _, deletion := range []bool{false, true} {
		t.Run(map[bool]string{false: "upsert", true: "deletion"}[deletion], func(t *testing.T) {
			git := &mockGitReader{
				listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
					return []string{"translations/laptop.md"}, nil
				},
				readFileFunc: func(_ context.Context, _, _, ref string) ([]byte, error) {
					if (ref == commit) == deletion {
						return nil, status.Error(codes.NotFound, "fixture absent")
					}
					return []byte(corpus.Valid["product-field"]), nil
				},
				resolveRefFunc: func(context.Context, string, string) (string, error) {
					return commit, nil
				},
			}
			server := newCatalogServer(t, nil, git)
			response, err := server.AdmitResources(t.Context(), &catalogv1.AdmitResourcesRequest{
				RepositoryId: testRepoID,
				RefName:      "refs/heads/main",
				OldCommitSha: strings.Repeat("b", 40),
				NewCommitSha: commit,
				ChangedPaths: []string{"translations/laptop.md"},
			})
			require.Nil(t, response, "an unsupported committed resource must not be silently acknowledged")
			require.Equal(t, codes.Unavailable, status.Code(err))
		})
	}
}
