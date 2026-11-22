// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type translationProjectionBackend interface{ datastore.TranslationStore }

type translationProjectionStore struct {
	datastore.Datastore
	translationProjectionBackend
	loseCreateReply bool
	loseUpdateReply bool
	loseDeleteReply bool
	beforeUpdate    func()
	beforeDelete    func()
}

func (s *translationProjectionStore) TranslationStore() datastore.TranslationStore { return s }

func (s *translationProjectionStore) CreateTranslation(ctx context.Context, source *datastore.Translation) error {
	if err := s.translationProjectionBackend.CreateTranslation(ctx, source); err != nil {
		return err
	}
	if s.loseCreateReply {
		s.loseCreateReply = false
		return errors.New("injected lost create reply")
	}
	return nil
}

func (s *translationProjectionStore) UpdateTranslation(ctx context.Context, source *datastore.Translation, expected string) error {
	if s.beforeUpdate != nil {
		before := s.beforeUpdate
		s.beforeUpdate = nil
		before()
	}
	if err := s.translationProjectionBackend.UpdateTranslation(ctx, source, expected); err != nil {
		return err
	}
	if s.loseUpdateReply {
		s.loseUpdateReply = false
		return errors.New("injected lost update reply")
	}
	return nil
}

func (s *translationProjectionStore) DeleteTranslation(ctx context.Context, uid, expected string) error {
	if s.beforeDelete != nil {
		before := s.beforeDelete
		s.beforeDelete = nil
		before()
	}
	if err := s.translationProjectionBackend.DeleteTranslation(ctx, uid, expected); err != nil {
		return err
	}
	if s.loseDeleteReply {
		s.loseDeleteReply = false
		return errors.New("injected lost delete reply")
	}
	return nil
}

type translationProjectionGit struct {
	GitReader
	tip        string
	trees      map[string]map[string][]byte
	beforeRead func()
}

func (g *translationProjectionGit) ResolveRef(context.Context, string, string) (string, error) {
	return g.tip, nil
}

func (g *translationProjectionGit) ReadFile(_ context.Context, _, path, ref string) ([]byte, error) {
	if g.beforeRead != nil {
		before := g.beforeRead
		g.beforeRead = nil
		before()
	}
	content, ok := g.trees[ref][path]
	if !ok {
		return nil, status.Error(codes.NotFound, "fixture path absent")
	}
	return slices.Clone(content), nil
}

func TestTranslationAcceptedCallbackReplay(t *testing.T) {
	f := newTranslationCallbackFixture(t)
	invoke := func(old, next string) error { return f.callback(t, old, next, f.entry.path) }
	zero := strings.Repeat("0", 40)
	firstCommit := f.admCtx.CommitSHA
	f.store.loseCreateReply = true
	require.Equal(t, codes.Internal, status.Code(invoke(zero, firstCommit)))
	first := f.source(t)
	require.Equal(t, f.entry.parsed.RawSource, first.RawSource)
	require.Equal(t, string(f.entry.body), first.Body)
	require.Equal(t, "alice", first.CreationActor)
	require.NoError(t, invoke(zero, firstCommit))
	require.Equal(t, first, f.source(t))

	newerCommit := strings.Repeat("b", 40)
	newer := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "New accepted callback", 1))
	f.git.trees[newerCommit] = map[string][]byte{f.entry.path: newer.parsed.RawSource}
	f.git.tip = newerCommit
	f.store.loseUpdateReply = true
	require.Equal(t, codes.Internal, status.Code(invoke(firstCommit, newerCommit)))
	current := f.source(t)
	require.Equal(t, first.UID, current.UID)
	require.EqualValues(t, 2, current.Generation)
	require.Equal(t, newer.parsed.RawSource, current.RawSource)
	require.Equal(t, newerCommit, current.GitCommitSHA)
	require.NoError(t, invoke(zero, firstCommit))
	require.NoError(t, invoke(firstCommit, newerCommit))
	require.Equal(t, current, f.source(t), "delayed signed callbacks cannot overwrite a newer accepted source")

	deletedCommit := strings.Repeat("c", 40)
	f.git.tip = deletedCommit
	f.git.trees[deletedCommit] = map[string][]byte{}
	f.store.loseDeleteReply = true
	require.Equal(t, codes.Internal, status.Code(invoke(newerCommit, deletedCommit)))
	require.NoError(t, invoke(newerCommit, deletedCommit))
	require.NoError(t, invoke(firstCommit, newerCommit))
	_, err := f.store.GetTranslation(f.ctx, current.UID)
	require.ErrorIs(t, err, datastore.ErrNotFound, "delayed callbacks cannot resurrect a deleted source")

	recreatedCommit := strings.Repeat("d", 40)
	f.git.tip = recreatedCommit
	f.git.trees[recreatedCommit] = f.git.trees[firstCommit]
	require.NoError(t, invoke(deletedCommit, recreatedCommit))
	recreated := f.source(t)
	require.NotEqual(t, current.UID, recreated.UID)
	require.EqualValues(t, 1, recreated.Generation)
	require.NoError(t, invoke(zero, firstCommit))
	require.NoError(t, invoke(newerCommit, deletedCommit))
	require.Equal(t, recreated, f.source(t), "replays cannot overwrite or delete a new incarnation")
}

func newTranslationCallbackFixture(t *testing.T) *translationProjectionFixture {
	t.Helper()
	f := newTranslationProjectionFixture(t)
	// Startup still rejects enabled configurations; exercise the dormant dispatcher.
	f.server.translationActivation.Enabled = true
	f.server.serviceSecret = "translation-callback-fixture"
	f.git.trees = map[string]map[string][]byte{
		f.admCtx.CommitSHA: {f.entry.path: f.entry.parsed.RawSource},
	}
	return f
}

func (f *translationProjectionFixture) callback(t *testing.T, old, next string, paths ...string) error {
	t.Helper()
	request := &catalogv1.AdmitResourcesRequest{
		RepositoryId: f.admCtx.RepositoryID, RefName: f.admCtx.RefName,
		OldCommitSha: old, NewCommitSha: next, ChangedPaths: paths,
		Authorization: &catalogv1.CallbackAuthorization{
			RepositoryId: f.admCtx.RepositoryID, ReceiveId: "accepted-" + next,
			Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"},
			Refs:  []*catalogv1.ReceiveRef{{RefName: f.admCtx.RefName, OldCommitSha: old, NewCommitSha: next}},
		},
	}
	signature, err := auth.SignRPC(f.server.serviceSecret, "gitstore-api", catalogv1.CatalogService_AdmitResources_FullMethodName, request)
	require.NoError(t, err)
	ctx := metadata.NewIncomingContext(f.ctx, metadata.Pairs(auth.RPCSignatureMetadata, signature))
	_, err = f.server.AdmitResources(ctx, request)
	return err
}

func TestTranslationAcceptedCallbackRejectsWholeInvalidDocument(t *testing.T) {
	f := newTranslationCallbackFixture(t)
	firstCommit := f.admCtx.CommitSHA
	require.NoError(t, f.callback(t, strings.Repeat("0", 40), firstCommit, f.entry.path))
	first := f.source(t)
	invalidCommit := strings.Repeat("b", 40)
	otherPath := "translations/aa-good.md"
	other := strings.Replace(string(f.entry.parsed.RawSource), f.entry.identity.Name, "another-translation", 1)
	f.git.trees[invalidCommit] = map[string][]byte{
		otherPath:    []byte(other),
		f.entry.path: []byte(strings.Replace(string(f.entry.parsed.RawSource), "spec.options{color}.title", "spec.price", 1)),
	}
	f.git.tip = invalidCommit
	require.Equal(t, codes.Unavailable, status.Code(f.callback(t, firstCommit, invalidCommit, otherPath, f.entry.path)))
	require.Equal(t, first, f.source(t), "an invalid patch cannot become a deletion or partial body/spec update")
	_, err := f.store.GetTranslationByName(f.ctx, "gitstore", "another-translation")
	require.ErrorIs(t, err, datastore.ErrNotFound, "load the complete snapshot before writing any entry")
}

func TestTranslationAcceptedCallbackPreservesSourceAndTargets(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid, Targets map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	for name, content := range corpus.Valid {
		t.Run(name, func(t *testing.T) {
			f := newTranslationCallbackFixture(t)
			f.entry = mustParsedEntry(t, f.entry.path, strings.ReplaceAll(content, "acme-store", "gitstore"))
			f.git.trees[f.git.tip][f.entry.path] = f.entry.parsed.RawSource
			targetKind := f.entry.parsed.Translation.Spec.TargetRef.Kind
			target := mustParsedEntry(t, "targets/"+targetKind+".md", strings.ReplaceAll(corpus.Targets[targetKind], "acme-store", "gitstore"))
			targets := []*parsedEntry{target}
			if targetKind == "ProductVariant" {
				product := mustParsedEntry(t, "targets/Product.md", strings.ReplaceAll(corpus.Targets["Product"], "acme-store", "gitstore"))
				targets = append([]*parsedEntry{product}, targets...)
			}
			_, err := f.server.admitParsedEntries(f.ctx, targets, f.admCtx, nil)
			require.NoError(t, err)
			before := make([][]byte, len(targets))
			for i, entry := range targets {
				resource, err := f.server.lookupResourceByIdentity(f.ctx, entry.identity)
				require.NoError(t, err)
				before[i], err = json.Marshal(resource)
				require.NoError(t, err)
			}
			require.NoError(t, f.callback(t, strings.Repeat("0", 40), f.git.tip, f.entry.path))
			source := f.source(t)
			require.Equal(t, f.entry.parsed.RawSource, source.RawSource)
			require.Equal(t, string(f.entry.body), source.Body)
			spec, err := json.Marshal(f.entry.parsed.Translation.Spec)
			require.NoError(t, err)
			require.JSONEq(t, string(spec), string(source.Spec))
			require.Equal(t, f.admCtx.Revision, source.Revision)
			require.Equal(t, f.admCtx.CommitSHA, source.GitCommitSHA)
			require.NotEmpty(t, source.SourceBlobOID)
			require.NotEmpty(t, source.UID)
			require.EqualValues(t, 1, source.Generation)
			require.Empty(t, source.Status)
			require.NoError(t, f.callback(t, strings.Repeat("0", 40), f.git.tip, f.entry.path))
			require.Equal(t, source, f.source(t))
			for i, entry := range targets {
				resource, err := f.server.lookupResourceByIdentity(f.ctx, entry.identity)
				require.NoError(t, err)
				after, err := json.Marshal(resource)
				require.NoError(t, err)
				require.Equal(t, before[i], after, "target source, metadata and resolved content must remain unchanged")
			}
		})
	}
}

type translationProjectionFixture struct {
	server *Server
	store  *translationProjectionStore
	git    *translationProjectionGit
	ctx    context.Context
	admCtx AdmissionContext
	entry  *parsedEntry
}

func newTranslationProjectionFixture(t *testing.T) *translationProjectionFixture {
	t.Helper()
	backend, err := memdb.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, backend.Close()) })
	capable, ok := backend.(datastore.TranslationCapable)
	require.True(t, ok)
	store := &translationProjectionStore{Datastore: backend, translationProjectionBackend: capable.TranslationStore()}
	server := newDecisionServer(t, store)
	server.registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
	git := &translationProjectionGit{tip: strings.Repeat("a", 40)}
	server.git = git
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	content := strings.ReplaceAll(corpus.Valid["product-combined"], "acme-store", "gitstore")
	entry := mustParsedEntry(t, "translations/product.de.md", content)
	admCtx := AdmissionContext{
		RepositoryID: "00000000-0000-0000-0000-000000000001", Namespace: "gitstore",
		ActorSubject: "alice", CommitSHA: git.tip, RefName: "refs/heads/main",
		Revision: "main@sha1:" + git.tip, Now: time.Now().UTC().Truncate(time.Millisecond),
	}
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "alice", AuthMethod: "static-users"})
	return &translationProjectionFixture{server: server, store: store, git: git, ctx: ctx, admCtx: admCtx, entry: entry}
}

func (f *translationProjectionFixture) project(entry *parsedEntry, admCtx AdmissionContext) ([]admission.EntryDecision, error) {
	return f.server.admitParsedEntries(f.ctx, []*parsedEntry{entry}, admCtx, nil)
}

func (f *translationProjectionFixture) source(t *testing.T) *datastore.Translation {
	t.Helper()
	source, err := f.store.GetTranslationByName(f.ctx, f.entry.identity.Namespace, f.entry.identity.Name)
	require.NoError(t, err)
	return source
}

func TestTranslationAcceptedSourceProjection(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
		UID: uuid.NewString(), Namespace: "gitstore", Name: f.entry.parsed.Translation.Spec.TargetRef.Name,
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Generation: 1, ResourceVersion: "1",
		CreationTimestamp: f.admCtx.Now, Spec: []byte(`{"Title":"Original"}`), Body: "Original target body",
		Status: []byte(`{"observedGeneration":1,"lastAppliedRevision":"target-revision","resolved":{"sentinel":"untouched"}}`),
	}))
	targetBefore, err := f.store.GetProductByName(f.ctx, "gitstore", f.entry.parsed.Translation.Spec.TargetRef.Name)
	require.NoError(t, err)
	decisions, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Len(t, decisions, 1)
	require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)
	first := f.source(t)
	require.Equal(t, f.entry.parsed.RawSource, first.RawSource)
	require.Equal(t, string(f.entry.body), first.Body)
	require.Equal(t, f.admCtx.CommitSHA, first.GitCommitSHA)
	require.Equal(t, f.admCtx.Revision, first.Revision)
	require.NotEmpty(t, first.SourceBlobOID)
	require.NotEmpty(t, first.SourceDigest)
	require.EqualValues(t, 1, first.Generation)
	require.Empty(t, first.Status, "projection must not invent a controller observation")

	observation, err := json.Marshal(catalog.TranslationStatus{
		ObservedGeneration: first.Generation, LastAppliedRevision: first.Revision, Conditions: []catalog.Condition{},
	})
	require.NoError(t, err)
	observed, err := f.store.UpdateTranslationStatus(f.ctx, first.UID, first.ResourceVersion, observation)
	require.NoError(t, err)
	decisions, err = f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryNoOp, decisions[0].Outcome)
	require.Equal(t, observed, f.source(t), "duplicate callback must preserve observations and versions")

	f.git.tip = strings.Repeat("b", 40)
	nextCtx := f.admCtx
	nextCtx.CommitSHA, nextCtx.Revision = f.git.tip, "main@sha1:"+f.git.tip
	next := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Neuer Computer", 1))
	decisions, err = f.project(next, nextCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)
	current := f.source(t)
	require.Equal(t, first.UID, current.UID)
	require.Equal(t, first.CreationTimestamp, current.CreationTimestamp)
	require.EqualValues(t, 2, current.Generation)
	require.Equal(t, next.parsed.RawSource, current.RawSource)
	require.NotEqual(t, first.SourceDigest, current.SourceDigest)
	require.NotEqual(t, first.SourceBlobOID, current.SourceBlobOID)
	require.Equal(t, observed.Status, current.Status, "source changes must not rewrite controller observations")
	require.Equal(t, nextCtx.CommitSHA, current.GitCommitSHA)
	decisions, err = f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryNoOp, decisions[0].Outcome)
	require.Equal(t, current, f.source(t), "a delayed callback cannot restore older accepted content")
	targetAfter, err := f.store.GetProductByName(f.ctx, targetBefore.Namespace, targetBefore.Name)
	require.NoError(t, err)
	require.Equal(t, targetBefore, targetAfter, "Translation projection must not write target source, metadata or resolved content")
}

func TestTranslationSourceProjectionRetainsFormattingWithoutGenerationChange(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	before := f.source(t)
	f.git.tip = strings.Repeat("b", 40)
	nextCtx := f.admCtx
	nextCtx.CommitSHA, nextCtx.Revision = f.git.tip, "main@sha1:"+f.git.tip
	next := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "kind: Translation", "kind: Translation # exact accepted formatting", 1))
	decisions, err := f.project(next, nextCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)
	after := f.source(t)
	require.Equal(t, before.UID, after.UID)
	require.Equal(t, before.Generation, after.Generation)
	require.Equal(t, before.Spec, after.Spec)
	require.Equal(t, before.Body, after.Body)
	require.NotEqual(t, before.ResourceVersion, after.ResourceVersion)
	require.NotEqual(t, before.SourceDigest, after.SourceDigest)
	require.Equal(t, next.parsed.RawSource, after.RawSource)
	require.Equal(t, nextCtx.CommitSHA, after.GitCommitSHA)
}

func TestTranslationSourceProjectionUsesAcceptedGitObjectFormat(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	f.git.tip = strings.Repeat("b", 64)
	f.admCtx.CommitSHA, f.admCtx.Revision = f.git.tip, "main@sha256:"+f.git.tip
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	source := f.source(t)
	require.Len(t, source.SourceBlobOID, 64)
	require.NotEqual(t, source.SourceDigest, source.SourceBlobOID, "Git blob OIDs frame the original source bytes with their object header")
	require.Equal(t, f.entry.parsed.RawSource, source.RawSource)
}

type denyTranslationSourcePolicy struct{}

func (denyTranslationSourcePolicy) Name() string { return "deny-translation-source" }
func (denyTranslationSourcePolicy) Validate(_ context.Context, req admission.AdmissionRequest) admission.AdmissionDecision {
	if req.Kind == "Translation" {
		return admission.DecisionDeny("Translation source denied", "spec")
	}
	return admission.DecisionAllow()
}

func TestTranslationSourceProjectionHonorsAdmissionPolicy(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	f.server.chain.RegisterValidatingPolicy(denyTranslationSourcePolicy{})
	decisions, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryDenied, decisions[0].Outcome)
	require.Equal(t, "POLICY_DENIED", decisions[0].Diagnostics[0].Reason)
	_, err = f.store.GetTranslationByName(f.ctx, f.entry.identity.Namespace, f.entry.identity.Name)
	require.ErrorIs(t, err, datastore.ErrNotFound)
}

func TestTranslationSourceProjectionUsesCreationActorAndOwnerTransfer(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	recorder := testutil.NewAllowAllAuthZ()
	f.server.registry = auth.NewProviderRegistry(nil, recorder, nil)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	recorder.Calls = nil
	_, err = f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Len(t, recorder.Calls, 1, "an unchanged Translation must not require spurious transfer authority")
	require.Equal(t, "translation.update", recorder.Calls[0].Action)
	content := strings.Replace(string(f.entry.parsed.RawSource), "\r\nspec:",
		"\r\n  annotations:\r\n    "+datastore.OwnerAnnotationKey+": system:group:editors\r\nspec:", 1)
	require.NotEqual(t, string(f.entry.parsed.RawSource), content)
	proposed := mustParsedEntry(t, f.entry.path, content)
	recorder.Calls = nil
	_, err = f.project(proposed, f.admCtx)
	require.NoError(t, err)
	require.Len(t, recorder.Calls, 1)
	require.Equal(t, "translation.transfer", recorder.Calls[0].Action)
	require.Equal(t, "alice", f.source(t).CreationActor)
	require.EqualValues(t, 1, f.source(t).Generation)
	recorder.Calls = nil
	_, err = f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Len(t, recorder.Calls, 1)
	require.Equal(t, "translation.transfer", recorder.Calls[0].Action)
	require.Equal(t, map[string]any{"kind": "User", "name": "alice"}, recorder.Calls[0].Resource.Attrs["targetOwnerRef"])
	require.Equal(t, "alice", f.source(t).EffectiveOwnerSub())
}

func TestTranslationSourceProjectionRecoversLostReply(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	f.store.loseCreateReply = true
	_, err := f.project(f.entry, f.admCtx)
	require.ErrorContains(t, err, "injected lost create reply")
	committed := f.source(t)
	decisions, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryNoOp, decisions[0].Outcome)
	require.Equal(t, committed, f.source(t))
}

func TestTranslationSourceProjectionPreservesConcurrentObservation(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	source := f.source(t)
	observation, err := json.Marshal(catalog.TranslationStatus{
		ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision, Conditions: []catalog.Condition{},
	})
	require.NoError(t, err)
	f.store.beforeUpdate = func() {
		_, err := f.store.UpdateTranslationStatus(f.ctx, source.UID, source.ResourceVersion, observation)
		require.NoError(t, err)
	}
	next := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Neuer Computer", 1))
	_, err = f.project(next, f.admCtx)
	require.ErrorIs(t, err, datastore.ErrConflict)
	preserved := f.source(t)
	require.Equal(t, json.RawMessage(observation), preserved.Status)
	require.Equal(t, source.RawSource, preserved.RawSource, "CAS failure cannot publish part of the new source")
	decisions, err := f.project(next, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)
	require.Equal(t, json.RawMessage(observation), f.source(t).Status)
}

func TestTranslationSourceProjectionCannotOverwriteNewerCallback(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	initial := f.source(t)
	older := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Older callback", 1))
	newer := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Newer callback", 1))
	olderContext, newerContext := f.admCtx, f.admCtx
	olderContext.CommitSHA = strings.Repeat("b", 40)
	olderContext.Revision = "main@sha1:" + olderContext.CommitSHA
	newerContext.CommitSHA = strings.Repeat("c", 40)
	newerContext.Revision = "main@sha1:" + newerContext.CommitSHA
	f.git.tip = olderContext.CommitSHA
	f.store.beforeUpdate = func() {
		f.git.tip = newerContext.CommitSHA
		decisions, err := f.project(newer, newerContext)
		require.NoError(t, err)
		require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)
	}
	_, err = f.project(older, olderContext)
	require.ErrorIs(t, err, datastore.ErrConflict)
	current := f.source(t)
	require.Equal(t, initial.UID, current.UID)
	require.EqualValues(t, 2, current.Generation)
	require.Equal(t, newer.parsed.RawSource, current.RawSource)
	require.Equal(t, newerContext.Revision, current.Revision)
	for _, replay := range []struct {
		entry *parsedEntry
		ctx   AdmissionContext
	}{{older, olderContext}, {newer, newerContext}, {f.entry, f.admCtx}} {
		decisions, err := f.project(replay.entry, replay.ctx)
		require.NoError(t, err)
		require.Equal(t, admission.EntryNoOp, decisions[0].Outcome)
		require.Equal(t, current, f.source(t), "replay must preserve the newer accepted source and version")
	}
}

func TestTranslationSourceProjectionRejectsPartialOrMismatchedSource(t *testing.T) {
	for _, mode := range []string{"invalid-patch", "mismatched-body", "mismatched-identity", "missing-source", "invalid-commit", "wrong-namespace"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationProjectionFixture(t)
			switch mode {
			case "invalid-patch":
				f.entry.parsed.RawSource = []byte(strings.Replace(string(f.entry.parsed.RawSource), "spec.options{color}.title", "spec.price", 1))
			case "mismatched-body":
				f.entry.body = []byte("not the accepted body")
			case "mismatched-identity":
				f.entry.identity.Name = "different"
			case "missing-source":
				f.entry.parsed.RawSource = nil
			case "invalid-commit":
				f.git.tip, f.admCtx.CommitSHA = "not-a-commit", "not-a-commit"
			case "wrong-namespace":
				f.admCtx.Namespace = "other"
			}
			_, err := f.project(f.entry, f.admCtx)
			require.Error(t, err)
			page, err := f.store.ListTranslations(f.ctx, "gitstore", datastore.PageParams{First: 10})
			require.NoError(t, err)
			require.Empty(t, page.Items)
		})
	}
}

func TestTranslationSourceRemovalUsesAcceptedSnapshot(t *testing.T) {
	for _, mode := range []string{"remove", "other-repository", "other-ref", "other-path", "stale-tip", "different-source"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationProjectionFixture(t)
			_, err := f.project(f.entry, f.admCtx)
			require.NoError(t, err)
			before := f.source(t)
			removal := f.admCtx
			f.git.tip = strings.Repeat("b", 40)
			removal.CommitSHA, removal.Revision = f.git.tip, "main@sha1:"+f.git.tip
			old := f.entry
			switch mode {
			case "other-repository":
				removal.RepositoryID = "00000000-0000-0000-0000-000000000003"
			case "other-ref":
				removal.RefName = "refs/heads/another"
			case "other-path":
				copy := *old
				copy.path = "translations/another.md"
				old = &copy
			case "stale-tip":
				f.git.tip = strings.Repeat("c", 40)
			case "different-source":
				old = mustParsedEntry(t, old.path, strings.Replace(string(old.parsed.RawSource), "Tragbarer Computer", "Other content", 1))
			}
			operation := resourceAdmissionOperation{operation: admission.OperationDelete, identity: old.identity, oldEntry: old}
			err = f.server.applyResourceOperations(f.ctx, []resourceAdmissionOperation{operation}, removal)
			if mode == "different-source" {
				require.ErrorIs(t, err, datastore.ErrConflict)
			} else {
				require.NoError(t, err)
			}
			if mode == "remove" {
				_, err := f.store.GetTranslation(f.ctx, before.UID)
				require.ErrorIs(t, err, datastore.ErrNotFound)
				require.NoError(t, f.server.applyResourceOperations(f.ctx, []resourceAdmissionOperation{operation}, removal), "repeated removal is idempotent")
			} else {
				require.Equal(t, before, f.source(t))
			}
		})
	}
}

func TestTranslationSourceRemovalCannotDeleteConcurrentUpdate(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	before := f.source(t)
	removal := f.admCtx
	f.git.tip = strings.Repeat("b", 40)
	removal.CommitSHA, removal.Revision = f.git.tip, "main@sha1:"+f.git.tip
	next := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Concurrent source", 1))
	nextCtx := f.admCtx
	nextCtx.CommitSHA, nextCtx.Revision = strings.Repeat("c", 40), "main@sha1:"+strings.Repeat("c", 40)
	f.store.beforeDelete = func() {
		f.git.tip = nextCtx.CommitSHA
		_, err := f.project(next, nextCtx)
		require.NoError(t, err)
	}
	operation := resourceAdmissionOperation{operation: admission.OperationDelete, identity: f.entry.identity, oldEntry: f.entry}
	require.ErrorIs(t, f.server.applyResourceOperations(f.ctx, []resourceAdmissionOperation{operation}, removal), datastore.ErrConflict)
	current := f.source(t)
	require.Equal(t, before.UID, current.UID)
	require.Equal(t, next.parsed.RawSource, current.RawSource)
	require.Equal(t, nextCtx.CommitSHA, current.GitCommitSHA)
}

func TestTranslationCommittedResultRequiresExactRawSource(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	_, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	source := f.source(t)
	request := admission.CommittedManifestRequest{RepositoryID: f.admCtx.RepositoryID, Namespace: f.admCtx.Namespace,
		RefName: f.admCtx.RefName, CommitSHA: f.admCtx.CommitSHA, Path: f.entry.path}
	require.True(t, committedResourceMatches(source, request, f.entry, false))
	source.RawSource = append([]byte("# different accepted envelope\n"), source.RawSource...)
	require.False(t, committedResourceMatches(source, request, f.entry, true), "matching spec/body alone cannot certify the accepted raw document")
}

func TestTranslationCommittedProjectionRecoversLostReplies(t *testing.T) {
	f := newTranslationCallbackFixture(t)
	request := admission.CommittedManifestRequest{
		RepositoryID: f.admCtx.RepositoryID, Namespace: f.admCtx.Namespace,
		RefName: f.admCtx.RefName, CommitSHA: f.admCtx.CommitSHA, Path: f.entry.path,
		Kind: "Translation", Name: f.entry.identity.Name, ActorSubject: "alice",
		Content: f.entry.parsed.RawSource, Operation: admission.OperationCreate,
	}
	f.store.loseCreateReply = true
	result, err := f.server.AdmitCommittedManifest(f.ctx, request)
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected lost create reply")
	created := f.source(t)
	require.Equal(t, request.Content, created.RawSource)
	require.EqualValues(t, 1, created.Generation)
	request.Operation = admission.OperationUpdate
	result, err = f.server.AdmitCommittedManifest(f.ctx, request)
	require.NoError(t, err)
	require.True(t, result.NoOp)
	require.Equal(t, created, f.source(t))

	f.git.tip = strings.Repeat("b", 40)
	request.CommitSHA = f.git.tip
	request.Content = []byte(strings.Replace(string(request.Content), "Tragbarer Computer", "Updated committed source", 1))
	f.git.trees[f.git.tip] = map[string][]byte{f.entry.path: request.Content}
	f.store.loseUpdateReply = true
	result, err = f.server.AdmitCommittedManifest(f.ctx, request)
	require.Nil(t, result)
	require.ErrorContains(t, err, "injected lost update reply")
	updated := f.source(t)
	require.Equal(t, created.UID, updated.UID)
	require.EqualValues(t, 2, updated.Generation)
	require.Equal(t, request.Content, updated.RawSource)
	require.Equal(t, request.CommitSHA, updated.GitCommitSHA)
	result, err = f.server.AdmitCommittedManifest(f.ctx, request)
	require.NoError(t, err)
	require.True(t, result.NoOp)
	require.Equal(t, updated, f.source(t), "synchronous repair must not increment generation or rewrite exact accepted attribution")
}

func TestTranslationCommittedRemovalRequiresExactLifetimeAndAbsence(t *testing.T) {
	for _, mode := range []string{"remove", "lost-reply", "manifest-present", "wrong-uid", "wrong-path", "superseded", "replacement-race"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationCallbackFixture(t)
			_, err := f.project(f.entry, f.admCtx)
			require.NoError(t, err)
			source := f.source(t)
			expected := source
			removal := strings.Repeat("b", 40)
			f.git.tip = removal
			f.git.trees[removal] = map[string][]byte{}
			request := admission.CommittedManifestRequest{
				RepositoryID: f.admCtx.RepositoryID, Namespace: source.Namespace, RefName: f.admCtx.RefName,
				CommitSHA: removal, Path: f.entry.path, Kind: "Translation", Name: source.Name,
				ExpectedUID: source.UID, ActorSubject: "alice", Operation: admission.OperationDelete,
			}
			switch mode {
			case "lost-reply":
				f.store.loseDeleteReply = true
			case "manifest-present":
				f.git.trees[removal][f.entry.path] = source.RawSource
			case "wrong-uid":
				request.ExpectedUID = uuid.NewString()
			case "wrong-path":
				request.Path = "translations/another.md"
			case "superseded":
				f.git.tip = strings.Repeat("c", 40)
			case "replacement-race":
				f.git.beforeRead = func() {
					require.NoError(t, f.store.DeleteTranslation(f.ctx, source.UID, source.ResourceVersion))
					replacement := *source
					replacement.UID = uuid.NewString()
					require.NoError(t, f.store.CreateTranslation(f.ctx, &replacement))
					expected = &replacement
				}
			}
			result, err := f.server.AdmitCommittedManifest(f.ctx, request)
			if mode != "remove" && mode != "lost-reply" {
				require.Nil(t, result)
				require.Error(t, err)
				require.Equal(t, expected, f.source(t))
				return
			}
			if mode == "lost-reply" {
				require.Nil(t, result)
				require.ErrorContains(t, err, "injected lost delete reply")
			} else {
				require.NoError(t, err)
				require.False(t, result.NoOp)
			}
			_, err = f.store.GetTranslation(f.ctx, source.UID)
			require.ErrorIs(t, err, datastore.ErrNotFound)
			result, err = f.server.AdmitCommittedManifest(f.ctx, request)
			require.NoError(t, err)
			require.True(t, result.NoOp, "a verified repeated removal is idempotent")
		})
	}
}

func TestTranslationManifestSuppliedSnapshotValidation(t *testing.T) {
	raw, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Valid map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	base := corpus.Valid["product-combined"]
	for _, name := range []string{"valid", "reordered", "missing-target", "missing-selector", "ambiguous-selector",
		"overlap", "mixed-valid-invalid", "title-limit", "deferred-inline", "reference-boundary"} {
		t.Run(name, func(t *testing.T) {
			content := []byte(base)
			target := &validate.TranslationTargetSnapshot{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
				UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "1",
				Spec: map[string]any{"title": "Original", "options": []any{
					map[string]any{"name": "color", "title": "Color", "values": []any{"silver", "black"}},
					map[string]any{"name": "size", "title": "Size", "values": []any{"m", "l"}},
				}},
			}
			switch name {
			case "reordered":
				slices.Reverse(target.Spec["options"].([]any))
			case "missing-target":
				target = nil
			case "missing-selector":
				content = []byte(strings.Replace(base, "spec.options{color}.title", "spec.options{absent}.title", 1))
			case "ambiguous-selector":
				options := target.Spec["options"].([]any)
				options[1].(map[string]any)["name"] = "color"
			case "overlap":
				content = []byte(strings.Replace(base, "spec.options{color}.title", "spec.title", 1))
			case "mixed-valid-invalid":
				target.Spec["options"].([]any)[0].(map[string]any)["title"] = nil
			case "title-limit":
				content = []byte(strings.Replace(base, "translation: Tragbarer Computer", "translation: "+strings.Repeat("x", 201), 1))
			case "deferred-inline":
				content = []byte(strings.Replace(base, "spec.options{color}.title", "spec.options{size}.inline.values{m}.title", 1))
			case "reference-boundary":
				content = []byte(strings.Replace(base, "spec.options{color}.title", "spec.options{size}.definitionRef.values{m}.title", 1))
			}
			beforeSource := slices.Clone(content)
			beforeTarget, err := json.Marshal(target)
			require.NoError(t, err)
			parsed, body, result, err := validateTranslationManifestSnapshot(t.Context(), validate.NewParser(), content, "acme-store", target)
			switch name {
			case "valid", "reordered":
				require.NoError(t, err)
				require.True(t, result.TargetResolved)
				require.Equal(t, []string{"spec.title", "spec.options{color}.title", "{body}"}, result.ValidatedPaths)
				require.Equal(t, "de-DE", parsed.Translation.Spec.Locale)
				_, expected, ok := strings.Cut(base, "\r\n---\r\n")
				require.True(t, ok)
				require.Equal(t, []byte(expected), body)
			case "missing-target":
				require.NoError(t, err)
				require.NotNil(t, parsed)
				require.False(t, result.TargetResolved)
				require.Equal(t, "Missing", result.Reason)
				require.Nil(t, result.ValidatedPaths)
			default:
				require.Error(t, err)
				require.Nil(t, parsed)
				require.Nil(t, body)
				require.Nil(t, result, "whole-document failure cannot expose validated patches")
				require.NotContains(t, err.Error(), "Tragbarer Computer")
			}
			afterTarget, marshalErr := json.Marshal(target)
			require.NoError(t, marshalErr)
			require.Equal(t, beforeTarget, afterTarget)
			require.Equal(t, beforeSource, content)
		})
	}
}
