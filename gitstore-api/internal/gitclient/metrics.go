// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Per-RPC Prometheus metrics registration for the gRPC client.
// go-grpc-prometheus registers grpc_client_* metrics automatically via its
// interceptors wired in grpc_client.go. This file exposes the Init function
// so callers can pre-initialise metric vectors at startup if desired.

package gitclient

import (
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

// RegisterClientMetrics pre-registers the go-grpc-prometheus client metrics
// on the given registry. Call this once at startup before any RPCs are made.
func RegisterClientMetrics(reg prometheus.Registerer) {
	reg.MustRegister(grpcprom.DefaultClientMetrics)
}
