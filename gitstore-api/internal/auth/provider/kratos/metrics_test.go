// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestDirectorySaturationIsMeasuredWithoutWaitingOrSendingAnotherRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer server.Close()
	cfg := directoryConfig(server.URL)
	cfg.MaxConcurrent = 1
	p, err := New(cfg)
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	p.metrics = newMetrics(observabilitytest.Meter(t, registry, "gitstore/userdir-saturation"))
	first := make(chan error, 1)
	go func() { _, err := p.GetBySubject(t.Context(), "private-first"); first <- err }()
	<-entered
	_, err = p.GetBySubject(t.Context(), "private-second")
	require.Error(t, err)
	close(release)
	require.NoError(t, <-first)
	require.Equal(t, float64(1), observabilitytest.Value(t, registry, "gitstore_userdir_saturation_total", nil))
}

func TestDirectoryMetricsUseSDKAndNoIdentityLabels(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(identityFixture())
	}))
	defer server.Close()
	p, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	p.metrics = newMetrics(observabilitytest.Meter(t, registry, "gitstore/userdir"))
	_, err = p.GetBySubject(t.Context(), "private-identity")
	require.NoError(t, err)
	_, err = p.GetBySubject(t.Context(), "private-identity")
	require.NoError(t, err)
	status = http.StatusServiceUnavailable
	_, err = p.GetBySubject(t.Context(), "another-private-identity")
	require.Error(t, err)
	require.Equal(t, float64(1), observabilitytest.Value(t, registry,
		"gitstore_userdir_requests_total", map[string]string{"method": "GET", "outcome": "unavailable"}))
	require.Equal(t, float64(1), observabilitytest.Value(t, registry,
		"gitstore_userdir_cache_total", map[string]string{"outcome": "hit"}))
	families, err := registry.Gather()
	require.NoError(t, err)
	resolutions := map[string]uint64{}
	for _, family := range families {
		for _, sample := range family.Metric {
			labels := map[string]string{}
			for _, label := range sample.Label {
				require.Contains(t, []string{"method", "outcome", "cache"}, label.GetName())
				require.NotContains(t, label.GetValue(), "private")
				labels[label.GetName()] = label.GetValue()
			}
			if family.GetName() == "gitstore_userdir_resolution_duration_seconds" {
				resolutions[labels["cache"]+"/"+labels["outcome"]] = sample.GetHistogram().GetSampleCount()
			}
		}
	}
	require.Equal(t, map[string]uint64{"hit/success": 1, "miss/success": 1, "miss/unavailable": 1}, resolutions,
		"resolution overhead must include complete cached and uncached profile work, including failure")
}
