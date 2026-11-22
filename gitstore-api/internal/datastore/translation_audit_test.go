// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type targetSourceAuditFixture struct {
	page          TranslationTargetSourceAuditPage
	calls         int
	onRecover     func(int) error
	epoch         string
	coverageEpoch string
	clockErr      error
	cursor        string
}

func (s *targetSourceAuditFixture) ReadTranslationTargetCoverageEpoch(context.Context, string) (string, error) {
	if s.coverageEpoch != "" {
		return s.coverageEpoch, s.clockErr
	}
	return s.epoch, s.clockErr
}

func (s *targetSourceAuditFixture) VerifyTranslationTargetSourceCoverage(ctx context.Context, hint TranslationTargetSourceAuditHint) error {
	return s.RecoverTranslationTargetSourceAudit(ctx, hint)
}

func (s *targetSourceAuditFixture) ReadTranslationTargetMutationEpoch(context.Context, string) (string, error) {
	return s.epoch, s.clockErr
}

func (s *targetSourceAuditFixture) ListTranslationTargetSourceAudit(_ context.Context, _, cursor string, _ int) (TranslationTargetSourceAuditPage, error) {
	s.cursor = cursor
	return s.page, nil
}

func (s *targetSourceAuditFixture) RecoverTranslationTargetSourceAudit(_ context.Context, _ TranslationTargetSourceAuditHint) error {
	s.calls++
	if s.onRecover != nil {
		return s.onRecover(s.calls)
	}
	return nil
}

func newTargetSourceAuditFixture() (*targetSourceAuditFixture, string) {
	namespace := uuid.NewString()
	store := &targetSourceAuditFixture{epoch: uuid.NewString()}
	for range 2 {
		store.page.Items = append(store.page.Items, TranslationTargetSourceAuditHint{
			Target: TranslationTargetKey{
				NamespaceUID: namespace, APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Name: uuid.NewString(),
			},
			Namespace: "shop", UID: uuid.NewString(), ResourceVersion: "1",
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		})
	}
	store.page.Scanned, store.page.Next = 2, "next"
	return store, namespace
}

type targetAuditProgressFixture struct {
	*targetSourceAuditFixture
	progress TranslationAuditProgress
}

func (s *targetAuditProgressFixture) GetTranslationTargetAuditProgress(context.Context, string) (TranslationAuditProgress, error) {
	return s.progress, nil
}

func (s *targetAuditProgressFixture) RepairTranslationTargetAuditTurn(context.Context, string, int) (TranslationAuditProgress, error) {
	return TranslationAuditProgress{}, ErrProjectionNotReady
}

func TestTranslationTargetAuditRestartsVerificationAfterBetweenPageMutation(t *testing.T) {
	store, namespace := newTargetSourceAuditFixture()
	repairing, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, TranslationAuditProgress{}, 2)
	require.NoError(t, err)
	store.page.Next = ""
	verifying, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, repairing, 2)
	require.NoError(t, err)
	require.Equal(t, "Verifying", verifying.Phase)
	store.page.Next = "next-verification-page"
	partial, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, verifying, 2)
	require.NoError(t, err)
	store.epoch = uuid.NewString()
	restarted, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, partial, 2)
	require.NoError(t, err)
	require.Empty(t, store.cursor, "a later epoch must restart at the first authoritative layout")
	require.NotEqual(t, partial.RunID, restarted.RunID)
	require.Equal(t, "Repairing", restarted.Phase)
	require.Empty(t, restarted.TargetMutationEpoch)
	store.page.Next = ""
	verifying, err = PlanTranslationTargetAuditTurn(t.Context(), store, namespace, restarted, 2)
	require.NoError(t, err)
	verified, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, verifying, 2)
	require.NoError(t, err)
	require.Equal(t, "Verified", verified.Phase)
	fixture := &targetAuditProgressFixture{targetSourceAuditFixture: store, progress: verified}
	epoch, err := ReadTranslationTargetAuditCoverage(t.Context(), fixture, namespace)
	require.NoError(t, err)
	require.Equal(t, store.epoch, epoch)
	store.epoch = uuid.NewString()
	epoch, err = ReadTranslationTargetAuditCoverage(t.Context(), fixture, namespace)
	require.ErrorIs(t, err, ErrConflict)
	require.Empty(t, epoch, "stored Verified alone never authorizes current coverage")
}

func TestTranslationTargetAuditRejectsIncompleteAndFailedCoverage(t *testing.T) {
	store, namespace := newTargetSourceAuditFixture()
	current, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, TranslationAuditProgress{}, 2)
	require.NoError(t, err)
	fixture := &targetAuditProgressFixture{targetSourceAuditFixture: store, progress: current}
	_, err = ReadTranslationTargetAuditCoverage(t.Context(), fixture, namespace)
	require.ErrorIs(t, err, ErrRepairRequired)
	store.clockErr = ErrConflict
	next, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, current, 2)
	require.ErrorIs(t, err, ErrConflict)
	require.Empty(t, next)
	store.clockErr = nil
	current.NamespaceUID = uuid.NewString()
	next, err = PlanTranslationTargetAuditTurn(t.Context(), store, namespace, current, 2)
	require.ErrorIs(t, err, ErrRepairRequired)
	require.Empty(t, next)
}

func TestTranslationTargetAuditProgressesDuringProvedMaterialWrites(t *testing.T) {
	store, namespace := newTargetSourceAuditFixture()
	store.coverageEpoch = uuid.NewString()
	store.onRecover = func(int) error {
		store.epoch = uuid.NewString()
		return nil
	}
	repairing, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, TranslationAuditProgress{}, 2)
	require.NoError(t, err)
	store.page.Next = ""
	verifying, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, repairing, 2)
	require.NoError(t, err)
	require.Equal(t, "Verifying", verifying.Phase)
	verified, err := PlanTranslationTargetAuditTurn(t.Context(), store, namespace, verifying, 2)
	require.NoError(t, err)
	require.Equal(t, "Verified", verified.Phase)
	require.Equal(t, store.coverageEpoch, verified.TargetMutationEpoch)
	fixture := &targetAuditProgressFixture{targetSourceAuditFixture: store, progress: verified}
	_, err = ReadTranslationTargetAuditCoverage(t.Context(), fixture, namespace)
	require.NoError(t, err)
}

func TestRecoverTranslationTargetSourceAuditPageRejectsNamespaceMutation(t *testing.T) {
	for _, mode := range []string{"changed-epoch", "pending-grant", "failed-clock", "empty-epoch"} {
		t.Run(mode, func(t *testing.T) {
			store, namespace := newTargetSourceAuditFixture()
			want := ErrConflict
			store.onRecover = func(call int) error {
				if call == 4 {
					switch mode {
					case "changed-epoch":
						store.epoch = uuid.NewString()
					case "pending-grant":
						store.clockErr = ErrConflict
					case "failed-clock":
						store.clockErr = ErrProjectionNotReady
					case "empty-epoch":
						store.epoch = ""
					}
				}
				return nil
			}
			switch mode {
			case "failed-clock":
				want = ErrProjectionNotReady
			case "empty-epoch":
				want = ErrRepairRequired
			}
			page, err := RecoverTranslationTargetSourceAuditPage(t.Context(), store, namespace, "", 2)
			require.ErrorIs(t, err, want)
			require.Empty(t, page, "unchanged page members cannot prove that no other source changed")
		})
	}
}

func TestRecoverTranslationTargetSourceAuditPageRechecksEveryObservation(t *testing.T) {
	store, namespace := newTargetSourceAuditFixture()
	result, err := RecoverTranslationTargetSourceAuditPage(t.Context(), store, namespace, "", 2)
	require.NoError(t, err)
	require.Equal(t, store.page, result)
	require.Equal(t, 4, store.calls)
	store.page.Items, store.page.Scanned = nil, 1
	result, err = RecoverTranslationTargetSourceAuditPage(t.Context(), store, namespace, "prior", 2)
	require.NoError(t, err)
	require.Equal(t, "next", result.Next, "sparse pages do not certify completeness")
	store, namespace = newTargetSourceAuditFixture()
	store.page.Items[1].UID = store.page.Items[0].UID
	store.page.Items[1].Target.Kind = "File"
	store.page.Items[1].Target.APIVersion = "storage.gitstore.dev/v1beta1"
	_, err = RecoverTranslationTargetSourceAuditPage(t.Context(), store, namespace, "", 2)
	require.NoError(t, err, "UID lifetimes are kind-scoped")
}

func TestRecoverTranslationTargetSourceAuditPageRejectsFailedOrRacedTurns(t *testing.T) {
	for _, mode := range []string{"foreign", "duplicate", "bad-lifetime", "bad-uid", "oversized", "stuck", "failed", "raced", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			store, namespace := newTargetSourceAuditFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrRepairRequired
			switch mode {
			case "foreign":
				store.page.Items[1].Target.NamespaceUID = uuid.NewString()
			case "duplicate":
				store.page.Items[1] = store.page.Items[0]
			case "bad-lifetime":
				store.page.Items[1].CreationTimestamp = time.Time{}
			case "bad-uid":
				store.page.Items[1].UID = "invalid"
			case "oversized":
				store.page.Scanned = 3
			case "stuck":
				store.page.Next = "prior"
			case "failed":
				store.onRecover = func(int) error { return ErrNotFound }
				want = ErrNotFound
			case "raced":
				store.onRecover = func(call int) error {
					if call == 4 {
						return ErrConflict
					}
					return nil
				}
				want = ErrConflict
			case "canceled":
				store.onRecover = func(int) error { cancel(); return nil }
				want = context.Canceled
			}
			result, err := RecoverTranslationTargetSourceAuditPage(ctx, store, namespace, "prior", 2)
			require.ErrorIs(t, err, want)
			require.Empty(t, result, "failed work cannot return an accepted cursor")
			if want == ErrRepairRequired {
				require.Zero(t, store.calls, "validate the entire page before repairing its first entry")
			}
		})
	}
}

type sourceAuditRecoveryFixture struct {
	page       TranslationSourceAuditPage
	states     map[string]TranslationMaintenanceIntent
	recovered  int
	reads      int
	onRecover  func(int) error
	beforeRead func(int)
}

func (s *sourceAuditRecoveryFixture) ListTranslationSourceAudit(context.Context, string, string, int) (TranslationSourceAuditPage, error) {
	return s.page, nil
}

func (s *sourceAuditRecoveryFixture) RecoverTranslationSourceAudit(_ context.Context, _ TranslationMaintenanceIntent) error {
	s.recovered++
	if s.onRecover != nil {
		return s.onRecover(s.recovered)
	}
	return nil
}

func (s *sourceAuditRecoveryFixture) GetTranslationMaintenance(_ context.Context, uid string) (*TranslationMaintenanceIntent, error) {
	s.reads++
	if s.beforeRead != nil {
		s.beforeRead(s.reads)
	}
	state := s.states[uid]
	return &state, nil
}

func (s *sourceAuditRecoveryFixture) GetTranslationSourceAuditWatermark(ctx context.Context, uid string) (*TranslationMaintenanceIntent, error) {
	return s.GetTranslationMaintenance(ctx, uid)
}

func sourceAuditRecoveryTestFixture() (*sourceAuditRecoveryFixture, string) {
	namespace := uuid.NewString()
	store := &sourceAuditRecoveryFixture{states: map[string]TranslationMaintenanceIntent{}}
	for _, phase := range []string{"Stable", "Retired"} {
		uid := uuid.NewString()
		state := TranslationMaintenanceIntent{
			ID: uid + "/1", NamespaceUID: namespace, TranslationUID: uid,
			SourceVersion: "1", ResourceVersion: "1", Phase: phase,
			CreationTimestamp: time.Now().UTC().Truncate(time.Millisecond),
		}
		store.page.Items = append(store.page.Items, state)
		store.states[uid] = state
	}
	store.page.Scanned, store.page.Next = 2, "next-discovery-page"
	return store, namespace
}

func TestRecoverTranslationSourceAuditPageReturnsRecheckedAuthority(t *testing.T) {
	store, namespace := sourceAuditRecoveryTestFixture()
	result, err := RecoverTranslationSourceAuditPage(t.Context(), store, namespace, "", 2)
	require.NoError(t, err)
	require.Equal(t, store.page, result)
	require.Equal(t, 2, store.recovered)
	require.Equal(t, 4, store.reads, "each recovered source needs a final authority recheck")
	store.page.Items, store.page.Scanned = nil, 1
	result, err = RecoverTranslationSourceAuditPage(t.Context(), store, namespace, "prior", 2)
	require.NoError(t, err)
	require.Empty(t, result.Items)
	require.Equal(t, "next-discovery-page", result.Next, "an empty scan page is not proof of completion")
}

func TestRecoverTranslationSourceAuditPageNeverAdvancesFailedWork(t *testing.T) {
	for _, mode := range []string{"foreign-scope", "duplicate", "oversized-page", "stuck-cursor", "repair-failure", "source-race", "unsettled", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			store, namespace := sourceAuditRecoveryTestFixture()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := ErrRepairRequired
			switch mode {
			case "foreign-scope":
				store.page.Items[1].NamespaceUID = uuid.NewString()
			case "duplicate":
				store.page.Items[1] = store.page.Items[0]
			case "oversized-page":
				store.page.Scanned = 3
			case "stuck-cursor":
				store.page.Next = "prior"
			case "repair-failure":
				want = ErrConflict
				store.onRecover = func(call int) error {
					if call == 2 {
						return ErrConflict
					}
					return nil
				}
			case "source-race":
				want = ErrConflict
				store.beforeRead = func(call int) {
					if call == 3 {
						uid := store.page.Items[0].TranslationUID
						state := store.states[uid]
						state.ResourceVersion, state.ID = "2", uid+"/2"
						store.states[uid] = state
					}
				}
			case "unsettled":
				uid := store.page.Items[0].TranslationUID
				state := store.states[uid]
				state.Phase = "Updating"
				store.states[uid] = state
			case "canceled":
				want = context.Canceled
				store.onRecover = func(int) error {
					cancel()
					return nil
				}
			}
			result, err := RecoverTranslationSourceAuditPage(ctx, store, namespace, "prior", 2)
			require.ErrorIs(t, err, want)
			require.Empty(t, result.Next)
			require.Empty(t, result.Items, "partial repair cannot publish an accepted page or authority set")
			if mode == "foreign-scope" || mode == "duplicate" || mode == "oversized-page" || mode == "stuck-cursor" {
				require.Zero(t, store.recovered, "validate the complete discovery page before any repair")
			}
			require.LessOrEqual(t, store.reads, 4)
		})
	}
}
