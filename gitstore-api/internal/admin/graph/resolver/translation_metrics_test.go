// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/catalog"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestTranslationGuardMetricsDoNotConfuseMasksWithCurrentEvidence(t *testing.T) {
	for _, scenario := range []string{"forbidden", "unavailable", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root, ctx, source, _ := newTranslationStatusRouteFixture(t)
			registry := prometheus.NewRegistry()
			root.translationMetrics = newTranslationReadMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/read"))
			switch scenario {
			case "forbidden":
				root.registry = auth.NewProviderRegistry(nil, &translationControllerAuthorizer{
					AuthZProvider: root.registry.AuthZ(), denyTarget: true,
				}, nil)
			case "unavailable":
				root.store = translationUnavailableProofStore{
					datastore.NewInstrumentedDatastore(root.store, "contract", zap.NewNop()).(*datastore.InstrumentedDatastore),
				}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, observed, err := root.guardTranslationRead(ctx, source)
			if scenario == "canceled" {
				require.ErrorIs(t, err, context.Canceled)
				require.Nil(t, observed)
			} else {
				require.NoError(t, err)
				require.Nil(t, observed.Resolved)
			}
			families, err := registry.Gather()
			require.NoError(t, err)
			requests, resources := map[string]float64{}, map[string]float64{}
			for _, family := range families {
				for _, sample := range family.Metric {
					for _, label := range sample.Label {
						if label.GetName() != "outcome" {
							continue
						}
						switch family.GetName() {
						case "gitstore_translation_guard_requests_total":
							requests[label.GetValue()] += sample.GetCounter().GetValue()
						case "gitstore_translation_guard_resources_total":
							resources[label.GetValue()] += sample.GetCounter().GetValue()
						}
					}
				}
			}
			if scenario == "canceled" {
				require.Equal(t, map[string]float64{"canceled": 1}, requests)
				require.Empty(t, resources)
			} else {
				require.Equal(t, map[string]float64{"completed": 1}, requests)
				require.Equal(t, map[string]float64{scenario: 1}, resources)
			}
		})
	}
}

func TestTranslationGuardMetricsCoverRealSingleAndPageReads(t *testing.T) {
	root, ctx, source, target := newTranslationStatusRouteFixture(t)
	registry := prometheus.NewRegistry()
	root.translationMetrics = newTranslationReadMetrics(observabilitytest.Meter(t, registry, "gitstore/translation/read"))
	_, current, err := root.guardTranslationRead(ctx, source)
	require.NoError(t, err)
	require.NotNil(t, current.Resolved)

	target.Spec = json.RawMessage(`{"title":"Private replacement"}`)
	target.Generation++
	expected := target.ResourceVersion
	target.ResourceVersion = nextTranslationRouteVersion(t, expected)
	require.NoError(t, root.store.UpdateProduct(ctx, target, expected))
	page, err := root.guardTranslationStatusPage(ctx, []*datastore.Translation{source, source})
	require.NoError(t, err)
	require.Len(t, page, 2)
	for _, item := range page {
		requireGuardCondition(t, item.status, catalog.ConditionFalse, "StaleEvidence")
		require.Nil(t, item.status.Resolved)
	}
	stored, err := root.store.(datastore.TranslationCapable).TranslationStore().GetTranslation(ctx, source.UID)
	require.NoError(t, err)
	require.Equal(t, source.Status, stored.Status)

	families, err := registry.Gather()
	require.NoError(t, err)
	requests, durations, resources := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, family := range families {
		for _, sample := range family.Metric {
			labels := map[string]string{}
			for _, label := range sample.Label {
				labels[label.GetName()] = label.GetValue()
			}
			switch family.GetName() {
			case "gitstore_translation_guard_requests_total":
				require.Len(t, labels, 2)
				requests[labels["route"]+"/"+labels["outcome"]] += sample.GetCounter().GetValue()
			case "gitstore_translation_guard_duration_seconds":
				require.Len(t, labels, 2)
				durations[labels["route"]+"/"+labels["outcome"]] += float64(sample.GetHistogram().GetSampleCount())
			case "gitstore_translation_guard_resources_total":
				require.Len(t, labels, 1)
				resources[labels["outcome"]] += sample.GetCounter().GetValue()
			}
		}
	}
	require.Equal(t, map[string]float64{"single/completed": 1, "page/completed": 1}, requests)
	require.Equal(t, requests, durations)
	require.Equal(t, map[string]float64{"current": 1, "stale": 2}, resources)
}
