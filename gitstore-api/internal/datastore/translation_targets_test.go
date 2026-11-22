// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type targetAbsenceCoverage struct {
	progress datastore.TranslationAuditProgress
	epoch    string
	err      error
	reads    int
	change   bool
}

func (s *targetAbsenceCoverage) GetTranslationTargetAuditProgress(context.Context, string) (datastore.TranslationAuditProgress, error) {
	return s.progress, s.err
}

func (s *targetAbsenceCoverage) ReadTranslationTargetCoverageEpoch(context.Context, string) (string, error) {
	s.reads++
	if s.change && s.reads == 2 {
		s.epoch = uuid.NewString()
	}
	return s.epoch, s.err
}

func TestTargetAbsenceRequiresCurrentCoverageAndUnchangedVacancy(t *testing.T) {
	for _, mode := range []string{"missing", "vacant", "unverified", "stale-coverage", "changed-coverage",
		"born", "changed-token", "aliased-head", "positive", "pending", "failed-head", "failed-coverage", "malformed-head", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			positive, _ := targetReadFixture()
			target := positive.Target
			store := &targetAbsenceCoverage{epoch: uuid.NewString()}
			store.progress = datastore.TranslationAuditProgress{
				NamespaceUID: target.NamespaceUID, RunID: uuid.NewString(), ResourceVersion: "1",
				Phase: "Verified", DescriptorDigest: translation.CapabilityDigest(), TargetMutationEpoch: store.epoch,
			}
			head := datastore.CloneTranslationTargetFence(positive)
			head.Evidence = nil
			want := datastore.ErrConflict
			ctx := t.Context()
			switch mode {
			case "missing":
				head = nil
			case "unverified":
				store.progress.Phase, store.progress.TargetMutationEpoch = "Repairing", ""
				want = datastore.ErrRepairRequired
			case "stale-coverage":
				store.epoch = uuid.NewString()
			case "changed-coverage":
				store.change = true
			case "positive":
				head = positive
			case "pending":
				var err error
				head, err = datastore.PlanTranslationTargetWrite(positive, target, datastore.TranslationTargetWriteIntent{
					ID: uuid.NewString(), PreviousUID: positive.Evidence.UID, NextUID: positive.Evidence.UID,
					ExpectedResourceVersion: "1", SourceDigest: "next", CreationTimestamp: positive.CreationTimestamp,
				}, positive.Token)
				require.NoError(t, err)
			case "failed-head", "failed-coverage":
				want = datastore.ErrProjectionNotReady
				if mode == "failed-coverage" {
					store.err = want
				}
			case "malformed-head":
				head.Token = ""
				want = datastore.ErrRepairRequired
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				want = context.Canceled
			}
			reads := 0
			err := datastore.ReadTranslationTargetAbsence(ctx, target, store,
				func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
					reads++
					if mode == "failed-head" {
						return nil, datastore.ErrProjectionNotReady
					}
					if reads == 2 {
						switch mode {
						case "born":
							head = positive
						case "changed-token":
							head = datastore.CloneTranslationTargetFence(head)
							head.Token = uuid.NewString()
						case "aliased-head":
							head.Token = uuid.NewString()
						}
					}
					if head == nil {
						return nil, datastore.ErrNotFound
					}
					return head, nil
				})
			if mode == "missing" || mode == "vacant" {
				require.NoError(t, err)
				require.Equal(t, 2, reads)
			} else {
				require.ErrorIs(t, err, want)
			}
			if mode == "canceled" {
				require.Zero(t, reads)
				require.Zero(t, store.reads)
			}
		})
	}
}

func targetReadFixture() (*datastore.TranslationTargetFence, *datastore.TranslationTargetMetadata) {
	target := datastore.TranslationTargetKey{
		NamespaceUID: uuid.NewString(), APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: "target",
	}
	fence := &datastore.TranslationTargetFence{
		Target: target, Token: uuid.NewString(), Phase: datastore.TranslationTargetStable,
		CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		Evidence: &catalog.TranslationTargetEvidence{
			APIVersion: target.APIVersion, Kind: target.Kind, Namespace: "content", Name: target.Name,
			UID: uuid.NewString(), Generation: 1, ResourceVersion: "1", Revision: "accepted", ContentDigest: "material",
		},
	}
	return fence, &datastore.TranslationTargetMetadata{
		Evidence: *fence.Evidence, CreationTimestamp: fence.CreationTimestamp, Active: true,
		SourceIdentity: datastore.TranslationTargetSourceIdentity{
			NamespaceUID: target.NamespaceUID, WriteToken: fence.Token, ContentDigest: fence.Evidence.ContentDigest,
		},
	}
}

func TestTargetEvidenceReadUsesCurrentStatusVersionWithoutChangingMaterial(t *testing.T) {
	for _, version := range []string{"1", "3", "100000000000000000000000000000000000000"} {
		t.Run(version, func(t *testing.T) {
			fence, metadata := targetReadFixture()
			metadata.Evidence.ResourceVersion = version
			var calls []string
			evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), fence.Target,
				func(_ context.Context, target datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
					require.Equal(t, fence.Target, target)
					calls = append(calls, "fence")
					return fence, nil
				},
				func(_ context.Context, frozen *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
					require.Equal(t, fence, frozen)
					calls = append(calls, "metadata")
					return metadata, nil
				})
			require.NoError(t, err)
			require.Equal(t, []string{"fence", "metadata", "fence"}, calls)
			require.Equal(t, &metadata.Evidence, evidence)
			evidence.ContentDigest = "caller mutation"
			require.Equal(t, "material", metadata.Evidence.ContentDigest)
			require.Equal(t, "1", fence.Evidence.ResourceVersion)
		})
	}
}

func TestTargetEvidenceReadRejectsUnprovedAndContradictoryMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*datastore.TranslationTargetMetadata){
		"namespace lifetime":    func(m *datastore.TranslationTargetMetadata) { m.SourceIdentity.NamespaceUID = uuid.NewString() },
		"namespace name":        func(m *datastore.TranslationTargetMetadata) { m.Evidence.Namespace = "other" },
		"source token":          func(m *datastore.TranslationTargetMetadata) { m.SourceIdentity.WriteToken = uuid.NewString() },
		"missing source token":  func(m *datastore.TranslationTargetMetadata) { m.SourceIdentity.WriteToken = "" },
		"source digest":         func(m *datastore.TranslationTargetMetadata) { m.SourceIdentity.ContentDigest = "other" },
		"missing source digest": func(m *datastore.TranslationTargetMetadata) { m.SourceIdentity.ContentDigest = "" },
		"incarnation":           func(m *datastore.TranslationTargetMetadata) { m.Evidence.UID = uuid.NewString() },
		"physical lifetime": func(m *datastore.TranslationTargetMetadata) {
			m.CreationTimestamp = m.CreationTimestamp.Add(time.Millisecond)
		},
		"name":                      func(m *datastore.TranslationTargetMetadata) { m.Evidence.Name = "other" },
		"kind":                      func(m *datastore.TranslationTargetMetadata) { m.Evidence.Kind = "Collection" },
		"apiVersion":                func(m *datastore.TranslationTargetMetadata) { m.Evidence.APIVersion = "catalog.gitstore.dev/v2" },
		"generation":                func(m *datastore.TranslationTargetMetadata) { m.Evidence.Generation++ },
		"revision":                  func(m *datastore.TranslationTargetMetadata) { m.Evidence.Revision = "other" },
		"content":                   func(m *datastore.TranslationTargetMetadata) { m.Evidence.ContentDigest = "other" },
		"regressing version":        func(m *datastore.TranslationTargetMetadata) { m.Evidence.ResourceVersion = "0" },
		"malformed version":         func(m *datastore.TranslationTargetMetadata) { m.Evidence.ResourceVersion = "opaque" },
		"unproved active lifecycle": func(m *datastore.TranslationTargetMetadata) { m.Active = false },
	} {
		t.Run(name, func(t *testing.T) {
			fence, metadata := targetReadFixture()
			mutate(metadata)
			evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), fence.Target,
				func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
					return fence, nil
				},
				func(context.Context, *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
					return metadata, nil
				})
			require.Error(t, err)
			require.Nil(t, evidence)
		})
	}
}

func TestTargetEvidenceReadFencesMidReadChangesAndAliasedSnapshots(t *testing.T) {
	for name, mutate := range map[string]func(*datastore.TranslationTargetFence){
		"new token":    func(f *datastore.TranslationTargetFence) { f.Token = uuid.NewString() },
		"changed head": func(f *datastore.TranslationTargetFence) { f.Evidence.ContentDigest = "replacement" },
		"new lifetime": func(f *datastore.TranslationTargetFence) {
			f.CreationTimestamp = f.CreationTimestamp.Add(time.Millisecond)
		},
		"empty proof":   func(f *datastore.TranslationTargetFence) { f.Evidence = nil },
		"changed phase": func(f *datastore.TranslationTargetFence) { f.Phase = datastore.TranslationTargetUpdating },
		"other target":  func(f *datastore.TranslationTargetFence) { f.Target.NamespaceUID = uuid.NewString() },
	} {
		t.Run(name, func(t *testing.T) {
			fence, metadata := targetReadFixture()
			fenceCalls := 0
			evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), fence.Target,
				func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
					fenceCalls++
					return fence, nil
				},
				func(_ context.Context, frozen *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
					mutate(fence)
					// Even a callback mutating its argument must not change the
					// snapshot against which the second fence is checked.
					mutate(frozen)
					return metadata, nil
				})
			require.Error(t, err)
			require.Nil(t, evidence)
			require.Equal(t, 2, fenceCalls)
		})
	}
}

func TestTargetEvidenceReadRequiresInitializedStableProof(t *testing.T) {
	for name, mutate := range map[string]func(*datastore.TranslationTargetFence) *datastore.TranslationTargetFence{
		"missing head": func(*datastore.TranslationTargetFence) *datastore.TranslationTargetFence { return nil },
		"unproved head": func(f *datastore.TranslationTargetFence) *datastore.TranslationTargetFence {
			f.Evidence = nil
			return f
		},
		"updating": func(f *datastore.TranslationTargetFence) *datastore.TranslationTargetFence {
			f.Phase = datastore.TranslationTargetUpdating
			return f
		},
		"unknown lifetime": func(f *datastore.TranslationTargetFence) *datastore.TranslationTargetFence {
			f.CreationTimestamp = time.Time{}
			return f
		},
		"wrong namespace": func(f *datastore.TranslationTargetFence) *datastore.TranslationTargetFence {
			f.Target.NamespaceUID = uuid.NewString()
			return f
		},
	} {
		t.Run(name, func(t *testing.T) {
			fence, _ := targetReadFixture()
			target := fence.Target
			fence = mutate(fence)
			evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), target,
				func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
					return fence, nil
				},
				func(context.Context, *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
					t.Fatal("an unproved head must not authorize a source read")
					return nil, nil
				})
			require.ErrorIs(t, err, datastore.ErrRepairRequired)
			require.Nil(t, evidence)
		})
	}
}

func TestTargetEvidenceReadRejectsMissingOrLaggingSource(t *testing.T) {
	for _, missing := range []bool{false, true} {
		fence, metadata := targetReadFixture()
		if missing {
			metadata = nil
		} else {
			fence.Evidence.ResourceVersion = "2"
		}
		fenceCalls := 0
		evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), fence.Target,
			func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
				fenceCalls++
				return fence, nil
			},
			func(context.Context, *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
				return metadata, nil
			})
		require.ErrorIs(t, err, datastore.ErrRepairRequired)
		require.Nil(t, evidence)
		require.Equal(t, 1, fenceCalls)
	}
}

func TestTargetEvidenceReadFailsClosedWithoutInventingAbsence(t *testing.T) {
	for _, step := range []int{1, 2, 3} {
		for _, failure := range []error{datastore.ErrNotFound, errors.New("metadata transport unavailable")} {
			t.Run(fmt.Sprintf("%d/%s", step, failure), func(t *testing.T) {
				fence, metadata := targetReadFixture()
				calls := 0
				nextError := func() error {
					calls++
					if calls == step {
						return failure
					}
					return nil
				}
				evidence, err := datastore.ReadTranslationTargetEvidence(t.Context(), fence.Target,
					func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
						return fence, nextError()
					},
					func(context.Context, *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
						return metadata, nextError()
					})
				if errors.Is(failure, datastore.ErrNotFound) {
					require.ErrorIs(t, err, datastore.ErrRepairRequired)
					require.NotErrorIs(t, err, datastore.ErrNotFound)
				} else {
					require.ErrorIs(t, err, failure)
				}
				require.Nil(t, evidence)
				require.Equal(t, step, calls, "a failed read must not retry or consume another metadata operation")
			})
		}
	}
}

func TestTargetEvidenceReadStopsAfterCancellation(t *testing.T) {
	for _, cancelAfter := range []int{0, 1, 2, 3} {
		fence, metadata := targetReadFixture()
		ctx, cancel := context.WithCancel(t.Context())
		calls := 0
		if cancelAfter == 0 {
			cancel()
		}
		check := func() {
			calls++
			if calls == cancelAfter {
				cancel()
			}
		}
		evidence, err := datastore.ReadTranslationTargetEvidence(ctx, fence.Target,
			func(context.Context, datastore.TranslationTargetKey) (*datastore.TranslationTargetFence, error) {
				check()
				return fence, nil
			},
			func(context.Context, *datastore.TranslationTargetFence) (*datastore.TranslationTargetMetadata, error) {
				check()
				return metadata, nil
			})
		cancel()
		require.ErrorIs(t, err, context.Canceled)
		require.Nil(t, evidence)
		require.Equal(t, cancelAfter, calls)
	}
}
