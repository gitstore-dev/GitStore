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

// Honest scope note (spec 051 T040): this repository has no dual-binary or
// dual-struct-version harness — there is no compiled pre-051 gitstore-api
// anywhere to actually invoke, so nothing here literally runs "old code"
// against "new" data or vice versa. Every test below runs the *current*
// build. What each test actually establishes is:
//  1. TestAdmitResources_FileAdmittedAlongsideKindUnknownToThisReplica
//     exercises the current build's generic, kind-agnostic "unrecognized
//     kind → log and skip, don't fail the push" mechanism (validator.go's
//     default case; loadParsedEntries' continue-on-parse-error path). That
//     mechanism is not File-specific — it is the exact mechanism that would
//     have made a pre-051 replica (one with no "File" case at all) safely
//     ignore a `kind: File` push before this spec shipped, and is the same
//     mechanism a current replica relies on to ignore any future kind it
//     doesn't know about yet. Exercising it with a stand-in unknown kind
//     is a faithful proxy for that cross-version behavior, not a literal
//     old-replica invocation.
//  2. TestAdmitResources_FileUpdateAcceptsRecordWrittenBeforeOptionalFieldsExisted
//     and TestAdmitResources_FileContentTypeImmutabilityEnforcedAgainstLegacyShapeSpec
//     construct a File row with the exact on-disk JSON shape a record
//     predating the optional `processing`/`resolved` keys would have —
//     the same technique this repo's accepted worked example for this
//     pattern already uses (memdb/owner_references_test.go's
//     TestOwnerReferenceProjectionCapsProductPagesAndIgnoresLegacyRecords,
//     cited by docs/runbooks/production-readiness-testing.md's Pattern 3)
//     — and prove the *current* admission code reads and updates that
//     shape correctly. This is additive-schema-evolution coverage, not a
//     test of an actual old binary's behavior.
//
// If a real two-version harness (e.g. a vendored older struct definition,
// or a second binary) is ever added to this repository, these tests should
// be revisited to run against it directly.

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
