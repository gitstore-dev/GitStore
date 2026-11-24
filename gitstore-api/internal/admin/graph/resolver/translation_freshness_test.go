// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func freshnessObservation(t *testing.T) (*datastore.Translation, *catalog.TranslationTargetEvidence) {
	t.Helper()
	target := &catalog.TranslationTargetEvidence{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme", Name: "laptop",
		UID: uuid.NewString(), Generation: 1, ResourceVersion: "7", Revision: "target-revision", ContentDigest: "material",
	}
	descriptor, err := translation.DescriptorFor(target.APIVersion, target.Kind)
	require.NoError(t, err)
	source := &datastore.Translation{
		APIVersion: translation.APIVersion, Kind: "Translation", Namespace: "acme", NamespaceID: uuid.NewString(),
		Name: "title", UID: uuid.NewString(), ResourceVersion: "4", Generation: 2, Revision: "source-revision",
		SourceDigest: "source-digest", CreationTimestamp: time.Now().UTC(),
		Spec: json.RawMessage(`{"locale":"fr","targetRef":{"apiVersion":"catalog.gitstore.dev/v1beta1","kind":"Product","name":"laptop"},"patches":[{"fieldPath":"spec.title","translation":"Titre"}]}`),
	}
	observation := catalog.TranslationStatus{
		ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision,
		Conditions: []catalog.Condition{{Type: catalog.ConditionReady, Status: catalog.ConditionTrue,
			ObservedGeneration: source.Generation, LastTransitionTime: source.CreationTimestamp}},
		Resolved: &catalog.ResolvedTranslation{CanonicalLocale: "fr", Target: target, ValidatedFieldPaths: []string{"spec.title"},
			SourceDigest: source.SourceDigest, DescriptorDigest: descriptor.Digest},
	}
	source.Status, err = json.Marshal(observation)
	require.NoError(t, err)
	return source, target
}

func requireGuardCondition(t *testing.T, status *catalog.TranslationStatus, value, reason string) {
	t.Helper()
	require.NotNil(t, status)
	for _, condition := range status.Conditions {
		if condition.Type == catalog.ConditionReady {
			require.Equal(t, value, condition.Status)
			if reason != "" {
				require.Equal(t, reason, condition.Reason)
			}
			return
		}
	}
	t.Fatal("guard omitted Ready")
}

func TestTranslationFreshnessPageDeduplicatesOnlyWithinThePage(t *testing.T) {
	source, target := freshnessObservation(t)
	sources := make([]*datastore.Translation, datastore.MaxTranslationPageSize)
	for i := range sources {
		copy := *source
		copy.UID, copy.Name = uuid.NewString(), fmt.Sprintf("title-%03d", i)
		sources[i] = &copy
	}
	var targetReads, sourceReads atomic.Int64
	readSource := func(_ context.Context, source *datastore.Translation) (*datastore.Translation, error) {
		sourceReads.Add(1)
		return source, nil
	}
	readTarget := func(context.Context, string, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
		targetReads.Add(1)
		return target, nil
	}
	for page := 1; page <= 2; page++ {
		result, err := guardTranslationPage(t.Context(), sources, readSource, readTarget, nil)
		require.NoError(t, err)
		require.Len(t, result, len(sources))
		for i, item := range result {
			require.Equal(t, sources[i].UID, item.source.UID)
			requireGuardCondition(t, item.status, catalog.ConditionTrue, "")
		}
		require.EqualValues(t, page, targetReads.Load(), "proof must not survive its source page")
		require.EqualValues(t, page*len(sources), sourceReads.Load(), "every source is independently rechecked")
	}
}

func TestTranslationFreshnessPageRetryCannotReuseAnOlderTargetProof(t *testing.T) {
	source, target := freshnessObservation(t)
	second := *source
	second.UID, second.Name = uuid.NewString(), "second"
	var targetReads atomic.Int64
	result, err := guardTranslationPage(t.Context(), []*datastore.Translation{source, &second},
		func(_ context.Context, current *datastore.Translation) (*datastore.Translation, error) {
			after := *current
			if current.UID == source.UID {
				after.ResourceVersion = "5"
			}
			return &after, nil
		},
		func(context.Context, string, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
			after := *target
			if targetReads.Add(1) > 1 {
				after.ContentDigest = "target-changed-after-the-new-source-version"
			}
			return &after, nil
		}, nil)
	require.NoError(t, err)
	require.EqualValues(t, 2, targetReads.Load())
	require.Equal(t, "5", result[0].source.ResourceVersion)
	requireGuardCondition(t, result[0].status, catalog.ConditionFalse, "StaleEvidence")
	requireGuardCondition(t, result[1].status, catalog.ConditionTrue, "")
}

func TestTranslationFreshnessPageKeepsDeniedAndUnavailableTargetsIsolated(t *testing.T) {
	for _, forbidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("forbidden=%t", forbidden), func(t *testing.T) {
			source, target := freshnessObservation(t)
			sibling := *source
			sibling.UID, sibling.Name = uuid.NewString(), "same-target"
			independent := *source
			independent.UID, independent.NamespaceID, independent.Name = uuid.NewString(), uuid.NewString(), "other-target"
			var reads atomic.Int64
			result, err := guardTranslationPage(t.Context(), []*datastore.Translation{source, &sibling, &independent},
				func(_ context.Context, source *datastore.Translation) (*datastore.Translation, error) {
					return source, nil
				},
				func(_ context.Context, _ string, key datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
					reads.Add(1)
					if key.NamespaceUID == independent.NamespaceID {
						return target, nil
					}
					if forbidden {
						return nil, admission.NewError(admission.CodeForbidden, "TARGET_DENIED", "private target").ToGQLError()
					}
					return nil, errors.New("private storage details")
				}, nil)
			require.NoError(t, err)
			reason := "Unavailable"
			if forbidden {
				reason = "Forbidden"
			}
			for _, item := range result[:2] {
				requireGuardCondition(t, item.status, catalog.ConditionUnknown, reason)
				require.Nil(t, item.status.Resolved)
			}
			requireGuardCondition(t, result[2].status, catalog.ConditionTrue, "")
			require.EqualValues(t, 2, reads.Load())
		})
	}
}

func TestTranslationFreshnessPageBoundsWorkersAndCancels(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancel), func(t *testing.T) {
			sources := make([]*datastore.Translation, 32)
			targets := make(map[string]*catalog.TranslationTargetEvidence, len(sources))
			for i := range sources {
				source, target := freshnessObservation(t)
				sources[i], targets[source.NamespaceID] = source, target
			}
			ctx, stop := context.WithTimeout(t.Context(), 5*time.Second)
			defer stop()
			started, release := make(chan struct{}, len(sources)), make(chan struct{})
			var active, maximum atomic.Int64
			done := make(chan error, 1)
			go func() {
				_, err := guardTranslationPage(ctx, sources,
					func(_ context.Context, source *datastore.Translation) (*datastore.Translation, error) {
						return source, nil
					},
					func(ctx context.Context, _ string, key datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
						count := active.Add(1)
						defer active.Add(-1)
						for old := maximum.Load(); count > old && !maximum.CompareAndSwap(old, count); old = maximum.Load() {
						}
						started <- struct{}{}
						select {
						case <-ctx.Done():
							return nil, ctx.Err()
						case <-release:
							return targets[key.NamespaceUID], nil
						}
					}, nil)
				done <- err
			}()
			for range translationGuardPageWorkers {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("bounded workers did not start")
				}
			}
			if cancel {
				stop()
			} else {
				close(release)
			}
			select {
			case err := <-done:
				if cancel {
					require.ErrorIs(t, err, context.Canceled)
				} else {
					require.NoError(t, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("page workers did not stop")
			}
			require.LessOrEqual(t, maximum.Load(), int64(translationGuardPageWorkers))
			require.Zero(t, active.Load())
		})
	}
}

func TestTranslationFreshnessMaskedReadsRequestWorkWithoutPersistingMask(t *testing.T) {
	for _, page := range []bool{false, true} {
		t.Run(fmt.Sprintf("page=%t", page), func(t *testing.T) {
			root, ctx, source := newTranslationReadFixture(t)
			store := root.store.(datastore.TranslationCapable).TranslationStore()
			before, err := store.GetTranslation(ctx, source.UID)
			require.NoError(t, err)
			var observed *catalog.TranslationStatus
			if page {
				result, err := root.guardTranslationStatusPage(ctx, []*datastore.Translation{source})
				require.NoError(t, err)
				require.Len(t, result, 1)
				observed = result[0].status
			} else {
				_, observed, err = root.guardTranslationRead(ctx, source)
				require.NoError(t, err)
			}
			requireGuardCondition(t, observed, catalog.ConditionFalse, "StaleEvidence")
			after, err := store.GetTranslation(ctx, source.UID)
			require.NoError(t, err)
			require.Equal(t, before, after)
			queued, err := store.RequestTranslationRevalidation(ctx, source)
			require.NoError(t, err)
			require.False(t, queued, "masked response must already have persisted its coalesced work hint")
		})
	}
}

func TestTranslationFreshnessMatchesOnlyMaterialUnlessVersionPinned(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "status-only", true: "explicit-pin"}[pinned], func(t *testing.T) {
			source, target := freshnessObservation(t)
			if pinned {
				var spec translation.Spec
				require.NoError(t, json.Unmarshal(source.Spec, &spec))
				spec.TargetRef.ResourceVersion = target.ResourceVersion
				source.Spec, _ = json.Marshal(spec)
			}
			original := append([]byte(nil), source.Status...)
			current := *target
			current.ResourceVersion = "8"
			sourceReads, targetReads := 0, 0
			_, result, err := guardTranslationObservation(t.Context(), source,
				func(context.Context) (*datastore.Translation, error) { sourceReads++; return source, nil },
				func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
					targetReads++
					return &current, nil
				})
			require.NoError(t, err)
			if pinned {
				requireGuardCondition(t, result, catalog.ConditionFalse, "StaleEvidence")
				require.Nil(t, result.Resolved)
			} else {
				requireGuardCondition(t, result, catalog.ConditionTrue, "")
				require.Equal(t, "7", result.Resolved.Target.ResourceVersion, "do not relabel the controller's observation")
				result.Resolved.Target.UID = "caller-change"
			}
			require.LessOrEqual(t, sourceReads, 1)
			require.Equal(t, 1, targetReads)
			require.Equal(t, original, []byte(source.Status), "masking must never persist or alias stored observations")
		})
	}
}

func TestTranslationFreshnessRejectsStoredEnumAliasesBeforeGraphQLConversion(t *testing.T) {
	for _, status := range []string{"TRUE", "true", " True ", "tRuE"} {
		t.Run(status, func(t *testing.T) {
			source, target := freshnessObservation(t)
			var observed catalog.TranslationStatus
			require.NoError(t, json.Unmarshal(source.Status, &observed))
			observed.Conditions[0].Status = status
			source.Status, _ = json.Marshal(observed)
			_, guarded, err := guardTranslationObservation(t.Context(), source,
				func(context.Context) (*datastore.Translation, error) { return source, nil },
				func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
					current := *target
					current.ContentDigest = "not-the-recorded-material"
					return &current, nil
				})
			require.NoError(t, err)
			value, err := translationGuardedToGraphQL(source, guarded)
			require.NoError(t, err)
			require.NotEqual(t, "TRUE", string(value.Status.Conditions[0].Status),
				"a permissive common enum converter cannot upgrade an unchecked observation to current Ready")
			requireGuardCondition(t, guarded, catalog.ConditionUnknown, "Unavailable")
		})
	}
}

func TestTranslationFreshnessRejectsStaleAndUnavailableObservations(t *testing.T) {
	for _, scenario := range []string{"material", "replacement", "source-digest", "descriptor", "generation",
		"condition-generation", "duplicate-ready", "revision", "scope", "uid-pin", "terminating-source", "missing-fence", "storage", "forbidden"} {
		t.Run(scenario, func(t *testing.T) {
			source, target := freshnessObservation(t)
			current := *target
			var targetErr error
			expected, reason := catalog.ConditionFalse, "StaleEvidence"
			switch scenario {
			case "material":
				current.ContentDigest = "changed-after-controller-read-or-status-CAS"
			case "replacement":
				current.UID = uuid.NewString()
			case "source-digest":
				source.SourceDigest = "changed"
			case "descriptor":
				var observation catalog.TranslationStatus
				require.NoError(t, json.Unmarshal(source.Status, &observation))
				observation.Resolved.DescriptorDigest = "old-descriptor"
				source.Status, _ = json.Marshal(observation)
			case "generation":
				source.Generation++
			case "condition-generation", "duplicate-ready":
				var observation catalog.TranslationStatus
				require.NoError(t, json.Unmarshal(source.Status, &observation))
				if scenario == "condition-generation" {
					observation.Conditions[0].ObservedGeneration--
				} else {
					observation.Conditions = append(observation.Conditions, observation.Conditions[0])
					expected, reason = catalog.ConditionUnknown, "Unavailable"
				}
				source.Status, _ = json.Marshal(observation)
			case "revision":
				source.Revision = "later-source"
			case "scope":
				current.Namespace = "other"
			case "uid-pin":
				var spec translation.Spec
				require.NoError(t, json.Unmarshal(source.Spec, &spec))
				spec.TargetRef.UID = uuid.NewString()
				source.Spec, _ = json.Marshal(spec)
			case "terminating-source":
				now := time.Now()
				source.DeletionTimestamp = &now
			case "missing-fence":
				targetErr = datastore.ErrRepairRequired
			case "storage":
				targetErr = errors.New("private storage details")
			case "forbidden":
				targetErr = admission.NewError(admission.CodeForbidden, "TARGET_DENIED", "private target").ToGQLError()
			}
			if targetErr != nil {
				expected, reason = catalog.ConditionUnknown, "Unavailable"
				if scenario == "forbidden" {
					reason = "Forbidden"
				}
			}
			_, result, err := guardTranslationObservation(t.Context(), source,
				func(context.Context) (*datastore.Translation, error) { return source, nil },
				func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
					return &current, targetErr
				})
			require.NoError(t, err)
			requireGuardCondition(t, result, expected, reason)
			for _, condition := range result.Conditions {
				if condition.Type == catalog.ConditionReady {
					require.NotEqual(t, catalog.ConditionTrue, condition.Status)
				}
			}
			require.Nil(t, result.Resolved, "failed target checks must redact target evidence")
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			require.NotContains(t, string(raw), "private")
		})
	}
}

func TestTranslationFreshnessRechecksSourceIdentityAndBoundsRetries(t *testing.T) {
	for _, scenario := range []string{"retry", "exhaustion", "same-version-name-reuse", "namespace-reuse", "source-io"} {
		t.Run(scenario, func(t *testing.T) {
			source, target := freshnessObservation(t)
			original := *source
			sourceReads, targetReads := 0, 0
			_, result, err := guardTranslationObservation(t.Context(), source,
				func(context.Context) (*datastore.Translation, error) {
					sourceReads++
					next := *source
					switch scenario {
					case "retry":
						next.ResourceVersion = "5"
					case "exhaustion":
						next.ResourceVersion = map[int]string{1: "5", 2: "6"}[sourceReads]
					case "same-version-name-reuse":
						next.UID = uuid.NewString()
					case "namespace-reuse":
						next.NamespaceID = uuid.NewString()
					case "source-io":
						return nil, errors.New("private source read failure")
					}
					return &next, nil
				},
				func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
					targetReads++
					return target, nil
				})
			require.NoError(t, err)
			if scenario == "retry" {
				requireGuardCondition(t, result, catalog.ConditionTrue, "")
				require.Equal(t, 2, targetReads)
			} else {
				requireGuardCondition(t, result, catalog.ConditionUnknown, "Unavailable")
				require.Nil(t, result.Resolved)
			}
			require.LessOrEqual(t, 1+sourceReads+3*targetReads, 10, "including initial source read and fence/metadata/fence")
			require.LessOrEqual(t, targetReads, 2)
			require.Equal(t, original, *source)
		})
	}
}

type freshnessMetadataStore struct {
	datastore.Datastore
	datastore.TranslationCapable
	datastore.TranslationTargetWriteFenceStore
	datastore.TranslationTargetMetadataStore
	reads, projectionReads int
	staleProjection        *datastore.Product
}

func (s *freshnessMetadataStore) GetProduct(context.Context, string) (*datastore.Product, error) {
	s.projectionReads++
	return s.staleProjection, nil
}

func (s *freshnessMetadataStore) GetProductByName(context.Context, string, string) (*datastore.Product, error) {
	s.projectionReads++
	return s.staleProjection, nil
}

func (s *freshnessMetadataStore) GetTranslationTargetFence(ctx context.Context, key datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
	s.reads++
	return s.TranslationTargetWriteFenceStore.GetTranslationTargetFence(ctx, key)
}

func (s *freshnessMetadataStore) ReadTranslationTargetMetadata(ctx context.Context, fence *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
	s.reads++
	return s.TranslationTargetMetadataStore.ReadTranslationTargetMetadata(ctx, fence)
}

func TestTranslationFreshnessServiceUsesAuthorizedThinEvidence(t *testing.T) {
	for _, scenario := range []string{"matching", "denied", "denied-negative", "source-changed",
		"target-before-status-CAS", "target-after-status-CAS", "target-delete-recreate", "target-status-only"} {
		t.Run(scenario, func(t *testing.T) {
			root, ctx, source := newTranslationReadFixture(t)
			product := &datastore.Product{
				UID: uuid.NewString(), Namespace: source.Namespace, Name: "laptop",
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product",
				CreationActor: "target-owner", Generation: 1, ResourceVersion: "1",
				CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond), Spec: json.RawMessage(`{"title":"Laptop"}`),
			}
			require.NoError(t, root.store.CreateProduct(ctx, product))
			evidence, err := root.store.(datastore.TranslationTargetFenceStore).ReadTargetEvidence(ctx, datastore.TranslationTargetKey{
				NamespaceUID: source.NamespaceID, APIVersion: product.APIVersion, Kind: product.Kind, Name: product.Name,
			})
			require.NoError(t, err)
			store := root.store.(datastore.TranslationCapable).TranslationStore()
			expected := source.ResourceVersion
			source.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(expected)
			require.NoError(t, err)
			source.SourceDigest = "accepted-source-digest"
			require.NoError(t, store.UpdateTranslation(ctx, source, expected))
			descriptor, err := translation.DescriptorFor(product.APIVersion, product.Kind)
			require.NoError(t, err)
			condition := catalog.ConditionTrue
			if scenario == "denied-negative" {
				condition = catalog.ConditionFalse
			}
			raw, err := json.Marshal(catalog.TranslationStatus{
				ObservedGeneration: source.Generation, LastAppliedRevision: source.Revision,
				Conditions: []catalog.Condition{{Type: catalog.ConditionReady, Status: condition, ObservedGeneration: source.Generation}},
				Resolved:   &catalog.ResolvedTranslation{Target: evidence, SourceDigest: source.SourceDigest, DescriptorDigest: descriptor.Digest},
			})
			require.NoError(t, err)
			mutateTarget := func() {
				t.Helper()
				next := *product
				next.ResourceVersion, next.Generation = "2", 2
				next.Spec = json.RawMessage(`{"title":"Changed after validation"}`)
				require.NoError(t, root.store.UpdateProduct(ctx, &next, product.ResourceVersion))
			}
			if scenario == "target-before-status-CAS" {
				mutateTarget()
			}
			source, err = store.UpdateTranslationStatus(ctx, source.UID, source.ResourceVersion, raw)
			require.NoError(t, err)
			switch scenario {
			case "target-after-status-CAS":
				mutateTarget()
			case "target-delete-recreate":
				require.NoError(t, root.store.DeleteProduct(ctx, product.UID))
				replacement := *product
				replacement.UID = uuid.NewString()
				require.NoError(t, root.store.CreateProduct(ctx, &replacement))
			case "target-status-only":
				_, err := root.store.UpdateProductStatus(ctx, source.Namespace, product.Name, datastore.ProductStatusPatch{
					ResourceVersion: product.ResourceVersion,
				})
				require.NoError(t, err)
			}
			provider := &translationControllerAuthorizer{AuthZProvider: root.registry.AuthZ(),
				denyTarget: scenario == "denied" || scenario == "denied-negative"}
			root.registry = auth.NewProviderRegistry(nil, provider, nil)
			if scenario == "source-changed" {
				provider.onTarget = func() {
					after := *source
					after.ResourceVersion, err = datastore.AdvanceTranslationIndexVersion(source.ResourceVersion)
					require.NoError(t, err)
					after.Body, after.SourceDigest = "later authored body", "later-source-digest"
					after.Generation++
					require.NoError(t, store.UpdateTranslation(ctx, &after, source.ResourceVersion))
				}
			}
			counted := &freshnessMetadataStore{
				Datastore: root.store, TranslationCapable: root.store.(datastore.TranslationCapable),
				TranslationTargetWriteFenceStore: root.store.(datastore.TranslationTargetWriteFenceStore),
				TranslationTargetMetadataStore:   root.store.(datastore.TranslationTargetMetadataStore),
				staleProjection:                  product,
			}
			root.store = counted
			guarded, observation, err := root.guardTranslationStatus(ctx, source)
			require.NoError(t, err)
			require.Equal(t, "target-owner", provider.owner)
			require.Equal(t, 3, counted.reads, "authorization and freshness share one fence/metadata/fence observation")
			require.Zero(t, counted.projectionReads, "lagging name/UID projections and full DTOs cannot prove current identity")
			switch scenario {
			case "matching", "target-status-only":
				requireGuardCondition(t, observation, catalog.ConditionTrue, "")
				require.Equal(t, source.ResourceVersion, guarded.ResourceVersion)
			case "denied", "denied-negative":
				requireGuardCondition(t, observation, catalog.ConditionUnknown, "Forbidden")
				require.Nil(t, observation.Resolved)
			case "source-changed":
				requireGuardCondition(t, observation, catalog.ConditionFalse, "StaleEvidence")
				require.NotEqual(t, source.ResourceVersion, guarded.ResourceVersion)
				require.Equal(t, "later authored body", guarded.Body, "status and returned source must be from the same retry")
			default:
				requireGuardCondition(t, observation, catalog.ConditionFalse, "StaleEvidence")
				require.Nil(t, observation.Resolved)
			}
		})
	}
}

func TestTranslationFreshnessCancellationNeverReturnsSuccess(t *testing.T) {
	source, target := freshnessObservation(t)
	ctx, cancel := context.WithCancel(t.Context())
	_, observation, err := guardTranslationObservation(ctx, source,
		func(context.Context) (*datastore.Translation, error) { return source, nil },
		func(context.Context, datastore.TranslationTargetKey) (*catalog.TranslationTargetEvidence, error) {
			cancel()
			return target, nil
		})
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, observation)
}
