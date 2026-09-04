// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace_test

import (
	"testing"
	"time"

	namespaceadmission "github.com/gitstore-dev/gitstore/api/internal/namespace"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNamespaceMetricsUseOnlyBoundedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := namespaceadmission.NewMetrics(registry)

	metrics.ObserveRejection(namespaceadmission.PhaseStructural, namespaceadmission.ReasonInvalidIdentifier)
	metrics.ObserveDeletionBlocked(namespaceadmission.ReasonNamespaceNotEmpty)
	metrics.ObserveDeletionOutcome(namespaceadmission.DeletionOutcomeAlreadyTerminating)
	metrics.ObserveValidationDuration(namespaceadmission.PhasePolicy, 25*time.Millisecond)
	metrics.ObserveAdmissionStage("git_commit", 100*time.Millisecond)

	families, err := registry.Gather()
	require.NoError(t, err)
	assert.Equal(t, []string{"phase", "reason"}, labelNames(metricFamily(t, families, "gitstore_namespace_validation_rejections_total")))
	assert.Equal(t, []string{"reason"}, labelNames(metricFamily(t, families, "gitstore_namespace_deletion_rejections_total")))
	assert.Equal(t, []string{"outcome"}, labelNames(metricFamily(t, families, "gitstore_namespace_deletion_outcomes_total")))
	assert.Equal(t, []string{"phase"}, labelNames(metricFamily(t, families, "gitstore_namespace_validation_duration_seconds")))
	assert.Equal(t, []string{"stage"}, labelNames(metricFamily(t, families, "gitstore_namespace_admission_stage_duration_seconds")))
}

func metricFamily(t *testing.T, families []*dto.MetricFamily, name string) *dto.MetricFamily {
	t.Helper()
	for _, family := range families {
		if family.GetName() == name {
			return family
		}
	}
	t.Fatalf("metric family %q not found", name)
	return nil
}

func labelNames(family *dto.MetricFamily) []string {
	if family == nil || len(family.Metric) == 0 {
		return nil
	}
	names := make([]string, 0, len(family.Metric[0].Label))
	for _, label := range family.Metric[0].Label {
		names = append(names, label.GetName())
	}
	return names
}
