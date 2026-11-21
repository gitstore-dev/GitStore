// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This profile reuses the repository watch/revision/recovery oracle without
// weakening the separate repository/lifecycle scale or duration gates.
func validateEndpointSplitConnectedScale(cfg repositoryCapacityConfig) error {
	for name, value := range map[string]string{"API A": cfg.apiA, "API B": cfg.apiB, "controller A": cfg.controllerA, "controller B": cfg.controllerB, "token": cfg.token, "replacement trigger": cfg.trigger} {
		if value == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if cfg.apiA == cfg.apiB || cfg.controllerA == cfg.controllerB || cfg.replacement != cfg.apiB || cfg.skipReplacement {
		return fmt.Errorf("two replicas and owned API B replacement are required")
	}
	if cfg.mode != capacityModeAlpha && cfg.mode != capacityModeProduction {
		return fmt.Errorf("connected acceptance requires alpha or production mode")
	}
	if cfg.duration < 36*time.Minute || cfg.subscribers != 20 || cfg.replayEvents < 20 || cfg.replaySamples < 5 || cfg.resourcePool < 20 || cfg.transitionInterval != 500*time.Millisecond || cfg.burstSize != 0 || cfg.mutationWorkers != 2 {
		return fmt.Errorf("connected profile requires 36 minutes, 20 observers, two authoring operations/s without bursts and bounded replay/pool/workers")
	}
	if cfg.burstInterval <= 0 || cfg.overflowBackpressureWait <= 0 {
		return fmt.Errorf("finite positive timing bounds are required")
	}
	if _, err := os.Stat(cfg.trigger); !os.IsNotExist(err) {
		return fmt.Errorf("replacement trigger must be fresh")
	}
	return nil
}

func TestEndpointSplitConnectedEvidenceGate(t *testing.T) {
	if os.Getenv("API_ENDPOINT_SPLIT_CAPACITY_RUN") != "1" {
		t.Skip("owned endpoint-split acceptance workload only")
	}
	cfg := loadRepositoryCapacityConfig(t)
	cfg.endpointSplit = true
	cfg.duration = 36 * time.Minute
	cfg.subscribers = 20
	cfg.replayEvents = 20
	cfg.replaySamples = 5
	cfg.resourcePool = 20
	cfg.mutationWorkers = 2
	cfg.transitionInterval = 500 * time.Millisecond
	cfg.burstSize = 0
	cfg.burstInterval = time.Minute
	cfg.overflowTransitions = 0
	cfg.baselineStabilization = time.Minute
	cfg.postLoadStabilization = time.Minute
	cfg.replacement = cfg.apiB
	cfg.replacementDelay = 18 * time.Minute
	require.NotEmpty(t, os.Getenv("CAPACITY_CONNECTED_READY_FILE"))
	for _, name := range []string{"CAPACITY_SOURCE_STATE_SHA256", "CAPACITY_SOURCE_REVISION", "CAPACITY_CONFIG_DIGEST", "CAPACITY_ENVIRONMENT_DIGEST"} {
		require.NotEmpty(t, os.Getenv(name), name)
	}
	for _, name := range []string{"CAPACITY_LOAD_START_FILE", "CAPACITY_CONNECTED_STARTED_FILE"} {
		require.NotEmpty(t, os.Getenv(name), name)
	}
	require.NoError(t, validateEndpointSplitConnectedScale(cfg))
	runRepositoryCapacityWorkload(t, cfg)
}

func awaitEndpointLoadStart(parent context.Context) error {
	path := os.Getenv("CAPACITY_LOAD_START_FILE")
	if !filepath.IsAbs(path) {
		return fmt.Errorf("an absolute owned load barrier is required")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := os.ReadFile(path)
		if err == nil {
			var receipt struct {
				RunID string `json:"runId"`
			}
			if json.Unmarshal(body, &receipt) != nil || receipt.RunID == "" || receipt.RunID != os.Getenv("CAPACITY_RUN_ID") {
				return fmt.Errorf("load barrier belongs to another run or is malformed")
			}
			return nil
		}
		if !os.IsNotExist(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func TestEndpointLoadBarrierRequiresTheOwnedRunAndSupportsCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "start.json")
	t.Setenv("CAPACITY_LOAD_START_FILE", path)
	t.Setenv("CAPACITY_RUN_ID", "owned")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, awaitEndpointLoadStart(ctx), context.Canceled)
	require.NoError(t, os.WriteFile(path, []byte(`{"runId":"another"}`), 0600))
	require.Error(t, awaitEndpointLoadStart(t.Context()))
	require.NoError(t, os.WriteFile(path, []byte(`{"runId":"owned"}`), 0600))
	require.NoError(t, awaitEndpointLoadStart(t.Context()))
	require.ErrorIs(t, awaitEndpointLoadStart(ctx), context.Canceled)
}
