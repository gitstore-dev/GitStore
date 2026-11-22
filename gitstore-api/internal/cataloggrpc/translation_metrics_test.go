// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/testutil"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func translationMetricSamples(t *testing.T, registry *prometheus.Registry, operation string) map[string]float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	counts, durations := map[string]float64{}, map[string]float64{}
	for _, family := range families {
		if family.GetName() != "gitstore_translation_"+operation+"_total" &&
			family.GetName() != "gitstore_translation_"+operation+"_duration_seconds" {
			continue
		}
		for _, sample := range family.Metric {
			labels := map[string]string{}
			for _, label := range sample.Label {
				labels[label.GetName()] = label.GetValue()
			}
			key := labels["outcome"]
			if operation == "validation" {
				require.Len(t, labels, 2)
				key = labels["stage"] + "/" + key
			} else {
				require.Len(t, labels, 1, "resource identity, locale and content must never become metric dimensions")
			}
			if family.GetType() == dto.MetricType_COUNTER {
				counts[key] += sample.GetCounter().GetValue()
			} else {
				durations[key] += float64(sample.GetHistogram().GetSampleCount())
			}
		}
	}
	require.NotEmpty(t, counts)
	require.Equal(t, counts, durations, "every operation count must have a matching SDK duration")
	return counts
}

func TestTranslationSourceValidationMetricsFollowActualParserOutcomes(t *testing.T) {
	registry := prometheus.NewRegistry()
	server := &Server{
		parser: validate.NewParser(), log: zap.NewNop(),
		translationMetrics: newTranslationMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/catalog")),
	}
	content := translationLimitSource(t)
	for _, raw := range []string{
		content,
		strings.Replace(content, "locale: de-DE", "locale: private_invalid_locale", 1),
		productDoc("unrelated-product", "Unrelated content", ""),
	} {
		server.validateResourceBlobs(t.Context(), "", []*catalogv1.ResourceBlob{{Path: "private-name.md", Content: []byte(raw)}})
	}
	require.Equal(t, map[string]float64{"source/accepted": 1, "source/rejected": 1}, translationMetricSamples(t, registry, "validation"))
}

func TestTranslationTargetValidationMetricsDistinguishAbsenceAndFailure(t *testing.T) {
	for _, mode := range []string{"accepted", "rejected", "unresolved", "forbidden", "unavailable", "work-unavailable", "canceled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			store := &translationCandidateStore{product: &datastore.Product{
				APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "acme-store", Name: "laptop",
				UID: "00000000-0000-4000-8000-000000000001", ResourceVersion: "1",
				Spec: []byte(`{"Title":"Original"}`),
			}}
			server := &Server{
				store: store, registry: auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil),
				translationMetrics: newTranslationMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/catalog")),
			}
			ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "owner", AuthMethod: "static-users"})
			switch mode {
			case "rejected":
				store.product.Spec = []byte(`{"Title":null}`)
			case "unresolved":
				store.product = nil
			case "forbidden":
				server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
			case "unavailable":
				store.err = errors.New("private target, namespace, locale and provider failure")
			case "work-unavailable":
				store.product.Spec = []byte(`{"Title":"Original","extra":[` + strings.Repeat("null,", 65_536) + `null]}`)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "deadline":
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
			}
			parsed, body, err := validate.NewParser().ParseResource(strings.NewReader(translationLimitSource(t)))
			require.NoError(t, err)
			result, err := server.validateTranslationManifestTarget(ctx, parsed, body, "acme-store", nil)
			if mode == "accepted" || mode == "unresolved" {
				require.NoError(t, err)
				require.Equal(t, mode == "accepted", result.TargetResolved)
			} else {
				require.Error(t, err)
				require.Nil(t, result)
			}
			outcome := mode
			if mode == "work-unavailable" {
				outcome = "overloaded"
			}
			require.Equal(t, map[string]float64{"target/" + outcome: 1}, translationMetricSamples(t, registry, "validation"))
		})
	}
}

func TestTranslationAdmissionMetricsIncludeAuthorizationConflictAndReplay(t *testing.T) {
	f := newTranslationProjectionFixture(t)
	registry := prometheus.NewRegistry()
	f.server.translationMetrics = newTranslationMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/catalog"))
	f.store.loseCreateReply = true
	_, err := f.project(f.entry, f.admCtx)
	require.Error(t, err)
	first := f.source(t)
	decisions, err := f.project(f.entry, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryNoOp, decisions[0].Outcome)
	require.Equal(t, first, f.source(t))

	changed := mustParsedEntry(t, f.entry.path, strings.Replace(string(f.entry.parsed.RawSource), "Tragbarer Computer", "Metrics replacement", 1))
	decisions, err = f.project(changed, f.admCtx)
	require.NoError(t, err)
	require.Equal(t, admission.EntryAccepted, decisions[0].Outcome)

	f.server.registry = auth.NewProviderRegistry(nil, testutil.NewDenyAllAuthZ(t), nil)
	_, err = f.project(f.entry, f.admCtx)
	require.Error(t, err)
	f.server.registry = auth.NewProviderRegistry(nil, testutil.NewAllowAllAuthZ(), nil)
	f.store.beforeUpdate = func() {
		current := f.source(t)
		previous := current.ResourceVersion
		current.ResourceVersion = nextResourceVersion(previous)
		current.UpdateActor = "concurrent-private-actor"
		require.NoError(t, f.store.translationProjectionBackend.UpdateTranslation(f.ctx, current, previous))
	}
	_, err = f.project(f.entry, f.admCtx)
	require.ErrorIs(t, err, datastore.ErrConflict)
	require.Equal(t, map[string]float64{
		"unavailable": 1, "no_op": 1, "accepted": 1, "forbidden": 1, "conflict": 1,
	}, translationMetricSamples(t, registry, "admission"))
}
