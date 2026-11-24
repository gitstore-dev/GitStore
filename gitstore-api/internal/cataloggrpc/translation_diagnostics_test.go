// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestTranslationManifestNegativeMatrix(t *testing.T) {
	testutil.RunTranslationNegativeMatrix(t, "testdata/translation/corpus.json",
		func(t *testing.T, content []byte, fields map[string]any) []*catalogv1.ValidationError {
			parser := validate.NewParser()
			parsed, _, err := parser.ParseResource(bytes.NewReader(content))
			var target *validate.TranslationTargetSnapshot
			if err == nil {
				ref := parsed.Translation.Spec.TargetRef
				target = &validate.TranslationTargetSnapshot{
					APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: "acme-store", Name: ref.Name,
					UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "7", Spec: fields,
				}
			}
			accepted, body, result, err := validateTranslationManifestSnapshot(t.Context(), parser, content, "acme-store", target)
			require.Error(t, err)
			require.Nil(t, accepted)
			require.Nil(t, body)
			require.Nil(t, result)
			findings, typed := validate.TranslationValidationErrors("translations/de.md", err)
			require.True(t, typed)
			return findings
		})
}

func TestTranslationCommonPreflightNegativeMatrix(t *testing.T) {
	testutil.RunTranslationNegativeMatrix(t, "testdata/translation/corpus.json",
		func(t *testing.T, content []byte, fields map[string]any) []*catalogv1.ValidationError {
			f := newTranslationCallbackFixture(t)
			f.server.store = f.store.Datastore
			content = bytes.ReplaceAll(content, []byte("acme-store"), []byte("gitstore"))
			parsed, body, standalone := validate.NewParser().ParseResource(bytes.NewReader(content))
			var target *datastore.Product
			if standalone == nil {
				ref := parsed.Translation.Spec.TargetRef
				require.Equal(t, "Product", ref.Kind, "extend the stored fixture when adding semantic rows for another kind")
				spec, err := json.Marshal(fields)
				require.NoError(t, err)
				uid := "00000000-0000-4000-8000-000000000001"
				require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
					APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: "gitstore", Name: ref.Name,
					UID: uid, ResourceVersion: "7", Generation: 1, CreationTimestamp: f.admCtx.Now, Spec: spec,
				}))
				target, err = f.store.GetProduct(f.ctx, uid)
				require.NoError(t, err)
				_, standalone = validate.ValidateTranslationSnapshot(f.ctx, parsed.Translation, body, "gitstore", &validate.TranslationTargetSnapshot{
					APIVersion: ref.APIVersion, Kind: ref.Kind, Namespace: "gitstore", Name: ref.Name,
					UID: uid, ResourceVersion: target.ResourceVersion, Spec: fields,
				})
			}
			expected, typed := validate.TranslationValidationErrors("translations/de.md", standalone)
			require.True(t, typed)
			response, err := f.server.validateResources(f.ctx, &catalogv1.ValidateResourcesRequest{
				RepositoryId: f.admCtx.RepositoryID, Trees: []*catalogv1.ResourceValidationTree{{
					ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "translations/de.md", Content: content}},
				}},
			})
			require.NoError(t, err)
			require.False(t, response.GetAccepted())
			require.Equal(t, expected, response.GetErrors())
			diagnostics, err := f.server.ValidateManifest(f.ctx, admission.ManifestValidationRequest{
				RepositoryID: f.admCtx.RepositoryID, Path: "translations/de.md", NewContent: content,
			})
			require.NoError(t, err)
			require.Equal(t, admission.FromValidationErrors(expected), diagnostics)
			if target != nil {
				after, err := f.store.GetProduct(f.ctx, target.UID)
				require.NoError(t, err)
				require.Equal(t, target, after)
			}
			return response.GetErrors()
		})
}

func TestTranslationActivationPreservesOtherResourceValidation(t *testing.T) {
	data, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Targets map[string]string }
	require.NoError(t, json.Unmarshal(data, &corpus))
	for kind, document := range corpus.Targets {
		t.Run(kind, func(t *testing.T) {
			original := []byte(strings.ReplaceAll(document, "acme-store", "gitstore"))
			parsed, body, err := validate.NewParser().ParseResource(bytes.NewReader(original))
			require.NoError(t, err)
			before, err := json.Marshal(parsed)
			require.NoError(t, err)
			baselineBody := bytes.Clone(body)
			var baseline *catalogv1.ValidateResourcesResponse
			for _, enabled := range []bool{false, true} {
				f := newTranslationCallbackFixture(t)
				f.server.store = f.store.Datastore
				f.server.translationActivation.Enabled = enabled
				content := bytes.Clone(original)
				response, err := f.server.validateResources(f.ctx, &catalogv1.ValidateResourcesRequest{
					RepositoryId: f.admCtx.RepositoryID, Trees: []*catalogv1.ResourceValidationTree{{
						ProposedBlobs: []*catalogv1.ResourceBlob{{Path: "resource.md", Content: content}},
					}},
				})
				require.NoError(t, err)
				require.True(t, response.GetAccepted(), "%v", response.GetErrors())
				if baseline == nil {
					baseline = response
				} else {
					require.Equal(t, baseline, response)
				}
				after, actualBody, err := validate.NewParser().ParseResource(bytes.NewReader(content))
				require.NoError(t, err)
				encoded, err := json.Marshal(after)
				require.NoError(t, err)
				require.Equal(t, before, encoded, "Translation validation must not rewrite another kind's authored enums or source")
				require.Equal(t, baselineBody, actualBody)
				require.Equal(t, original, content)
			}
		})
	}
}

func TestTranslationStructuralDiagnosticsMatchStandaloneValidation(t *testing.T) {
	data, err := os.ReadFile("testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Valid    map[string]string
		Negative []struct{ Name, Base, Old, New, Field string }
	}
	require.NoError(t, json.Unmarshal(data, &corpus))
	server := &Server{parser: validate.NewParser(), log: zap.NewNop()}
	for _, change := range corpus.Negative {
		if change.Name == "missing-selector" {
			continue
		}
		t.Run(change.Name, func(t *testing.T) {
			content := []byte(strings.Replace(corpus.Valid[change.Base], change.Old, change.New, 1))
			before := bytes.Clone(content)
			_, _, standalone := validate.NewParser().ParseResource(bytes.NewReader(content))
			require.Error(t, standalone)
			expected, ok := validate.TranslationValidationErrors("translations/de.md", standalone)
			require.True(t, ok)
			actual := server.validateResourceBlobs(t.Context(), "", []*catalogv1.ResourceBlob{{
				Path: "translations/de.md", Content: content,
			}})
			require.Equal(t, expected, actual)
			require.Equal(t, before, content)
			require.NotContains(t, admission.FormatRejection(admission.FromValidationErrors(actual)), "Tragbarer Computer")
		})
	}
}

func TestTranslationWholeDocumentRejectionPreservesAcceptedRevision(t *testing.T) {
	for _, mode := range []string{"structural", "target-field", "invalid-utf8"} {
		t.Run(mode, func(t *testing.T) {
			f := newTranslationCallbackFixture(t)
			f.server.store = f.store.Datastore
			require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "laptop",
				UID: uuid.NewString(), ResourceVersion: "1", Generation: 1, CreationTimestamp: f.admCtx.Now,
				Spec: []byte(`{"Title":"Original","Options":[]}`), Body: "Original target body",
				Status: []byte(`{"observedGeneration":1,"lastAppliedRevision":"target-revision","resolved":{"sentinel":"untouched"}}`),
			}))
			_, err := f.project(f.entry, f.admCtx)
			require.NoError(t, err)
			source := f.source(t)
			observation, err := json.Marshal(catalog.TranslationStatus{
				ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision, Conditions: []catalog.Condition{},
			})
			require.NoError(t, err)
			source, err = f.store.UpdateTranslationStatus(f.ctx, source.UID, source.ResourceVersion, observation)
			require.NoError(t, err)
			target, err := f.store.GetProductByName(f.ctx, "gitstore", "laptop")
			require.NoError(t, err)
			content := []byte(strings.Replace(string(source.RawSource), "Tragbarer Computer", "Anderer Computer", 1))
			switch mode {
			case "structural":
				content = bytes.Replace(content, []byte("spec.options{color}.title"), []byte("spec.price"), 1)
			case "invalid-utf8":
				content = append(content, 0xff)
			}
			original := bytes.Clone(content)
			parsed, body, standalone := validate.NewParser().ParseResource(bytes.NewReader(content))
			if standalone == nil {
				_, standalone = validate.ValidateTranslationSnapshot(f.ctx, parsed.Translation, body, "gitstore", &validate.TranslationTargetSnapshot{
					APIVersion: target.APIVersion, Kind: target.Kind, Namespace: target.Namespace, Name: target.Name,
					UID: target.UID, ResourceVersion: target.ResourceVersion,
					Spec: map[string]any{"title": "Original", "options": []any{}},
				})
			}
			expected, typed := validate.TranslationValidationErrors(f.entry.path, standalone)
			require.True(t, typed)
			require.NotEmpty(t, expected)
			ref := &catalogv1.ReceiveRef{
				RefName: f.admCtx.RefName, OldCommitSha: f.admCtx.CommitSHA, NewCommitSha: strings.Repeat("b", 40),
			}
			request := &catalogv1.ValidateResourcesRequest{
				RepositoryId: f.admCtx.RepositoryID,
				Trees: []*catalogv1.ResourceValidationTree{{
					Ref: ref, OldBlobs: []*catalogv1.ResourceBlob{{Path: f.entry.path, Content: source.RawSource}},
					ProposedBlobs: []*catalogv1.ResourceBlob{{Path: f.entry.path, Content: content}},
				}},
				Authorization: &catalogv1.CallbackAuthorization{
					RepositoryId: f.admCtx.RepositoryID, ReceiveId: "rejected-translation",
					Actor: &catalogv1.CallbackActor{Subject: "alice", AuthMethod: "static-users"}, Refs: []*catalogv1.ReceiveRef{ref},
				},
			}
			signature, err := auth.SignRPC(f.server.serviceSecret, "gitstore-api", catalogv1.CatalogService_ValidateResources_FullMethodName, request)
			require.NoError(t, err)
			ctx := metadata.NewIncomingContext(f.ctx, metadata.Pairs(auth.RPCSignatureMetadata, signature))
			response, err := f.server.ValidateResources(ctx, request)
			require.NoError(t, err)
			require.False(t, response.GetAccepted())
			require.Equal(t, expected, response.GetErrors())
			diagnostics, err := f.server.ValidateManifest(f.ctx, admission.ManifestValidationRequest{
				RepositoryID: f.admCtx.RepositoryID, Path: f.entry.path, OldContent: source.RawSource, NewContent: content,
			})
			require.NoError(t, err)
			require.Equal(t, admission.FromValidationErrors(expected), diagnostics)
			require.NotContains(t, admission.FormatRejection(diagnostics), "Anderer Computer")
			require.Equal(t, source, f.source(t), "rejection cannot advance source revision, generation or controller observation")
			after, err := f.store.GetProductByName(f.ctx, "gitstore", "laptop")
			require.NoError(t, err)
			require.Equal(t, target, after, "field/body rejection cannot modify target authored or resolved content")
			require.Equal(t, original, content)
		})
	}
}

type translationTargetReadDenied struct{ auth.AuthZProvider }

func (p translationTargetReadDenied) Authorize(ctx context.Context, principal *auth.Principal, action string, resource auth.ResourceContext) (auth.Decision, error) {
	if action == "product.read" {
		return auth.Decision{Outcome: auth.OutcomeDeny}, nil
	}
	return p.AuthZProvider.Authorize(ctx, principal, action, resource)
}

func TestTranslationManifestDeniedTargetExistenceIsOpaque(t *testing.T) {
	var denied string
	for _, present := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[present], func(t *testing.T) {
			f := newTranslationCallbackFixture(t)
			f.server.store = f.store.Datastore
			f.server.registry = auth.NewProviderRegistry(nil, translationTargetReadDenied{testutil.NewAllowAllAuthZ()}, nil)
			if present {
				require.NoError(t, f.store.CreateProduct(f.ctx, &datastore.Product{
					APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "laptop",
					UID: uuid.NewString(), ResourceVersion: "1", Generation: 1, CreationTimestamp: f.admCtx.Now,
					Spec: []byte(`{"Title":"Private target","Options":[]}`),
				}))
			}
			diagnostics, err := f.server.ValidateManifest(f.ctx, admission.ManifestValidationRequest{
				RepositoryID: f.admCtx.RepositoryID, Path: f.entry.path, NewContent: f.entry.parsed.RawSource,
			})
			require.Nil(t, diagnostics)
			require.Equal(t, codes.PermissionDenied, status.Code(err))
			require.NotContains(t, err.Error(), "Private target")
			if denied == "" {
				denied = err.Error()
			} else {
				require.Equal(t, denied, err.Error(), "target presence cannot change the denied response")
			}
		})
	}
}
