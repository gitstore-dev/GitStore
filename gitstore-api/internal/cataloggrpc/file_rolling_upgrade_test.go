// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeFutureKindDocument builds a frontmatter document identifying as a kind
// this build of gitstore-api does not recognize — standing in for a resource
// kind introduced by a newer replica during a rolling upgrade (or, viewed
// from the other direction, a document an older replica left behind that a
// newer replica must still tolerate gracefully). AdmitResources must ignore
// it without failing the rest of the push.
func makeFutureKindDocument(name string) []byte {
	return []byte("---\napiVersion: storage.gitstore.dev/v1beta1\nkind: MediaAsset\nmetadata:\n  name: " + name + "\nspec:\n  contentType: image/jpeg\n---\n")
}

// TestAdmitResources_FileAdmittedAlongsideKindUnknownToThisReplica verifies
// that a push mixing a valid File document with a document of a kind this
// build does not recognize (e.g. the out-of-scope, future `MediaAsset` kind
// referenced by spec 051's FR-020) is handled safely: the unrecognized
// document is logged and skipped, and the File document alongside it still
// admits normally. During a rolling upgrade an old replica must be able to
// ignore resource kinds it doesn't understand yet without corrupting or
// blocking admission of the kinds it does (spec 051 T040).
func TestAdmitResources_FileAdmittedAlongsideKindUnknownToThisReplica(t *testing.T) {
	store := newTestDatastore(t)
	commit := "1111111111111111111111111111111111111111"
	git := &mockGitReader{
		listFilesFunc: func(context.Context, string, string, string) ([]string, error) {
			return []string{"files/hero.md", "media/future.md"}, nil
		},
		readFileFunc: func(_ context.Context, _, path, _ string) ([]byte, error) {
			if path == "files/hero.md" {
				return makeFile("hero"), nil
			}
			return makeFutureKindDocument("future"), nil
		},
	}
	srv := newCatalogServer(t, store, git)

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, CommitSha: commit, RefName: "refs/heads/main",
	})
	require.NoError(t, err, "an unrecognized kind elsewhere in the push must never fail admission of the rest")

	file, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, "Alt text", file.Body)
}

// TestAdmitResources_FileUpdateAcceptsRecordWrittenBeforeOptionalFieldsExisted
// pre-seeds the datastore directly with a File row shaped exactly as an
// older gitstore-api replica would have written it: Spec JSON with no
// "processing" key at all (rather than an explicit null) and Status JSON
// with no "resolved" key. This is the File-shaped analogue of
// memdb/owner_references_test.go's "pre-ownerReferences record from a
// rolling upgrade remains readable" case. A newer replica admitting the next
// update for that File must read and update it without error (spec 051
// T040 — additive-only schema evolution across a rolling upgrade).
func TestAdmitResources_FileUpdateAcceptsRecordWrittenBeforeOptionalFieldsExisted(t *testing.T) {
	store := newTestDatastore(t)
	now := time.Now().UTC()
	// FileSpec has no `json` tags (only `yaml`), so admission's
	// json.Marshal(resource.Spec) always serializes it using the Go field
	// names verbatim (e.g. "ContentType", "Source"). This fixture matches
	// that real on-disk shape, just without the "Processing" key at all —
	// exactly what a row predating FileProcessingDefinition would look
	// like. FileStatus does carry explicit lowercase `json` tags, so its
	// legacy fixture below correctly omits only the "resolved" key.
	commitOld := "1111111111111111111111111111111111111111"
	commitNew := "2222222222222222222222222222222222222222"
	legacySpec := []byte(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`)
	legacyStatus := []byte(`{"observedGeneration":1,"lastAppliedRevision":"main@sha1:` + commitOld + `","conditions":[{"type":"AdmissionAccepted","status":"True"}]}`)
	require.NoError(t, store.CreateFile(context.Background(), &datastore.File{
		UID: "00000000-0000-0000-0000-000000000200", Namespace: "gitstore", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: now,
		RepositoryID: testRepoID, SourcePath: "files/hero.md",
		GitCommitSHA: commitOld, GitRef: "refs/heads/main",
		Spec: legacySpec, Body: "Alt text", Status: legacyStatus,
	}))

	// Sanity check the seeded row really predates the optional fields.
	var seededSpec map[string]any
	require.NoError(t, json.Unmarshal(legacySpec, &seededSpec))
	_, hasProcessing := seededSpec["Processing"]
	require.False(t, hasProcessing, "fixture must not include the optional Processing field")

	// The git reader must expose both the old and new commit trees so
	// admission's diff engine classifies this push as an Update against the
	// pre-seeded identity, not a fresh Create (which would collide with the
	// already-existing legacy row and silently no-op).
	current := commitOld
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		commitOld: {"files/hero.md": makeFile("hero")},
		commitNew: {"files/hero.md": makeFileWithBody("hero", "Upgraded alt text")},
	})
	current = commitNew
	srv := newCatalogServer(t, store, git)

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: commitOld, NewCommitSha: commitNew,
		CommitSha: commitNew, RefName: "refs/heads/main", ChangedPaths: []string{"files/hero.md"},
	})
	require.NoError(t, err, "admitting an update over a legacy-shaped record must not error")

	updated, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Generation)
	assert.Equal(t, "2", updated.ResourceVersion)
	assert.Equal(t, "Upgraded alt text", updated.Body)

	var status catalog.FileStatus
	require.NoError(t, json.Unmarshal(updated.Status, &status))
	require.Len(t, status.Conditions, 2)
	assert.Equal(t, catalog.ConditionAdmissionAccepted, status.Conditions[0].Type)
	assert.Nil(t, status.Resolved, "a legacy record's missing resolved block must not synthesize a non-nil value")
}

// TestAdmitResources_FileContentTypeImmutabilityEnforcedAgainstLegacyShapeSpec
// proves that the contentType-immutability guard (existingSpecContentType)
// correctly parses a legacy-shaped Spec JSON written before the optional
// "processing" field existed. A rolling-upgraded replica must still reject
// a contentType change against an old record exactly as it would against a
// current-shaped one (spec 051 T040).
func TestAdmitResources_FileContentTypeImmutabilityEnforcedAgainstLegacyShapeSpec(t *testing.T) {
	store := newTestDatastore(t)
	now := time.Now().UTC()
	commitOld := "4444444444444444444444444444444444444444"
	commitNew := "3333333333333333333333333333333333333333"
	legacySpec := []byte(`{"ContentType":"image/jpeg","Source":{"Type":"s3","URI":"s3://bucket/hero"}}`)
	require.NoError(t, store.CreateFile(context.Background(), &datastore.File{
		UID: "00000000-0000-0000-0000-000000000201", Namespace: "gitstore", Name: "hero",
		APIVersion: "storage.gitstore.dev/v1beta1", Kind: "File",
		Generation: 1, ResourceVersion: "1", CreationTimestamp: now,
		RepositoryID: testRepoID, SourcePath: "files/hero.md",
		GitCommitSHA: commitOld, GitRef: "refs/heads/main",
		Spec: legacySpec, Body: "Alt text",
		Status: []byte(`{"conditions":[{"type":"AdmissionAccepted","status":"True"}]}`),
	}))

	current := commitOld
	git := newTreeGitReader(&current, map[string]map[string][]byte{
		commitOld: {"files/hero.md": makeFile("hero")},
		commitNew: {"files/hero.md": makeFileWithContentType("hero", "image/png")},
	})
	current = commitNew
	srv := newCatalogServer(t, store, git)

	_, err := srv.AdmitResources(context.Background(), &catalogv1.AdmitResourcesRequest{
		RepositoryId: testRepoID, OldCommitSha: commitOld, NewCommitSha: commitNew,
		CommitSha: commitNew, RefName: "refs/heads/main", ChangedPaths: []string{"files/hero.md"},
	})
	require.NoError(t, err)

	unchanged, err := store.GetFileByName(context.Background(), "gitstore", "hero")
	require.NoError(t, err)
	assert.Equal(t, "1", unchanged.ResourceVersion, "the immutable contentType change must be rejected, leaving the legacy record untouched")
	assert.Contains(t, string(unchanged.Spec), `"ContentType":"image/jpeg"`)
}
