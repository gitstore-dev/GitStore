// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runCapacityPreflight(t *testing.T, profile, mode string, overrides map[string]string) ([]byte, string, error) {
	t.Helper()
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	evidence := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join(root, "tests/capacity/preflight", profile+".sh"), evidence)
	command.Dir = root
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "CAPACITY_") && !strings.HasPrefix(entry, "MODE=") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "MODE="+mode)
	for key, value := range overrides {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	return output, filepath.Join(evidence, "preflight-environment.json"), err
}

func TestCapacityPreflightRejectsUnknownMode(t *testing.T) {
	for _, profile := range []string{"api-readiness", "api-endpoint-split", "namespace-admission", "product-lifecycle", "category-hierarchy"} {
		t.Run(profile, func(t *testing.T) {
			output, evidence, err := runCapacityPreflight(t, profile, "unsupported", nil)
			require.Error(t, err)
			require.Contains(t, string(output), "MODE must be diagnostic, alpha, or production")
			_, err = os.Stat(evidence)
			require.True(t, os.IsNotExist(err), "invalid mode must not produce evidence")
		})
	}
}

func TestCapacityPreflightRetainsControllerScenarioGates(t *testing.T) {
	for _, profile := range []string{"product-lifecycle", "category-hierarchy"} {
		t.Run(profile, func(t *testing.T) {
			environment := map[string]string{
				"CAPACITY_CONFIG_MANIFEST":        "owned-config.json",
				"CAPACITY_ENVIRONMENT_MANIFEST":   "owned-environment.json",
				"CAPACITY_API_A":                  "http://api-a.invalid",
				"CAPACITY_API_B":                  "http://api-b.invalid",
				"CAPACITY_CONTROLLER_A":           "http://controller-a.invalid",
				"CAPACITY_CONTROLLER_B":           "http://controller-b.invalid",
				"CAPACITY_API_REPLICAS":           "2",
				"CAPACITY_CONTROLLER_REPLICAS":    "2",
				"CAPACITY_API_BUILD":              "release",
				"CAPACITY_CONTROLLER_BUILD":       "release",
				"CAPACITY_GIT_SERVICE_BUILD":      "release",
				"CAPACITY_SCYLLA_NODES":           "3",
				"CAPACITY_SCYLLA_SMP":             "1",
				"CAPACITY_CATEGORY_PUSH_EVIDENCE": "owned-push.json",
			}
			for key, invalid := range map[string]string{
				"CAPACITY_CONFIG_MANIFEST":      "",
				"CAPACITY_ENVIRONMENT_MANIFEST": "",
				"CAPACITY_API_A":                "",
				"CAPACITY_CONTROLLER_B":         "",
				"CAPACITY_API_REPLICAS":         "1",
				"CAPACITY_CONTROLLER_REPLICAS":  "1",
				"CAPACITY_API_BUILD":            "debug",
				"CAPACITY_CONTROLLER_BUILD":     "debug",
				"CAPACITY_GIT_SERVICE_BUILD":    "debug",
				"CAPACITY_SCYLLA_NODES":         "2",
				"CAPACITY_SCYLLA_SMP":           "0",
			} {
				t.Run(key, func(t *testing.T) {
					original := environment[key]
					environment[key] = invalid
					defer func() { environment[key] = original }()
					output, evidence, err := runCapacityPreflight(t, profile, "alpha", environment)
					require.Error(t, err, "%s", output)
					_, err = os.Stat(evidence)
					require.True(t, os.IsNotExist(err))
				})
			}
			output, evidence, err := runCapacityPreflight(t, profile, "alpha", environment)
			require.NoError(t, err, "%s", output)
			data, err := os.ReadFile(evidence)
			require.NoError(t, err)
			var receipt struct {
				Mode     string            `json:"mode"`
				Topology map[string]int    `json:"topology"`
				Builds   map[string]string `json:"builds"`
			}
			require.NoError(t, json.Unmarshal(data, &receipt))
			require.Equal(t, "alpha", receipt.Mode)
			require.Equal(t, map[string]int{"apiReplicas": 2, "controllerReplicas": 2, "scyllaNodes": 3, "scyllaSmpPerNode": 1}, receipt.Topology)
			require.Equal(t, map[string]string{"api": "release", "controller": "release", "gitService": "release"}, receipt.Builds)
			if profile == "category-hierarchy" {
				for key, invalid := range map[string]string{
					"CAPACITY_CATEGORY_SUBTREE_SIZE":     "9999",
					"CAPACITY_RATE":                      "19",
					"CAPACITY_CATEGORY_DURATION_SECONDS": "299",
					"CAPACITY_CATEGORY_PUSH_EVIDENCE":    "",
				} {
					t.Run(key, func(t *testing.T) {
						original, existed := environment[key]
						environment[key] = invalid
						defer func() {
							if existed {
								environment[key] = original
							} else {
								delete(environment, key)
							}
						}()
						output, _, err := runCapacityPreflight(t, profile, "alpha", environment)
						require.Error(t, err, "%s", output)
					})
				}
			}
		})
	}
}
