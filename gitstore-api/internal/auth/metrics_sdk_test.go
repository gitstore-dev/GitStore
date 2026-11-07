// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package auth_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

var sdkTestMetrics prometheus.Gatherer

func TestMain(m *testing.M) {
	_, shutdown, err := observability.InitTelemetry(context.Background(), observability.OTLPConfig{}, observability.APIService, "test", zap.NewNop())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	sdkTestMetrics = observabilitytest.HTTPGatherer(observability.MetricsHandler())
	code := m.Run()
	_ = shutdown(context.Background())
	os.Exit(code)
}
