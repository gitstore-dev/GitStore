// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEndpointGitReuseRequiresIdenticalBuildInputs(t *testing.T) {
	preflight, err := os.ReadFile("../../tests/capacity/preflight/api-endpoint-split.sh")
	require.NoError(t, err)
	block := strings.SplitN(string(preflight), "<<'PY'\n", 2)
	require.Len(t, block, 2)
	code := strings.SplitN(block[1], "\nPY\n", 2)[0]
	for _, scenario := range []string{"same Git sources", "changed dependency", "changed native source"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			git := func(args ...string) string {
				command := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "user.name=fixture", "-c", "user.email=fixture@localhost", "-C", root}, args...)...)
				output, err := command.CombinedOutput()
				require.NoError(t, err, string(output))
				return strings.TrimSpace(string(output))
			}
			write := func(path, value string) {
				name := filepath.Join(root, path)
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0700))
				require.NoError(t, os.WriteFile(name, []byte(value), 0600))
			}
			git("init", "-q")
			lock := "[[package]]\nname = \"git-service\"\nversion = \"0.1.0-alpha.7\"\n\n[[package]]\nname = \"dependency\"\nversion = \"1\"\n"
			write("gitstore-git-service/Cargo.lock", lock)
			write("gitstore-git-service/src/main.rs", "fn main() {}\n")
			git("add", ".")
			git("commit", "-qm", "baseline")
			revision := git("rev-parse", "HEAD")
			write("gitstore-api/main.go", "package main\n")
			lock = strings.Replace(lock, "0.1.0-alpha.7", "0.1.0-alpha.8", 1)
			if scenario == "changed dependency" {
				lock = strings.Replace(lock, "version = \"1\"", "version = \"2\"", 1)
			}
			write("gitstore-git-service/Cargo.lock", lock)
			if scenario == "changed native source" {
				write("gitstore-git-service/src/main.rs", "fn main() { panic!(); }\n")
			}
			git("add", ".")
			git("commit", "-qm", "API change")
			receipt := filepath.Join(t.TempDir(), "processes.json")
			body, err := json.Marshal([]any{map[string]any{"Config": map[string]any{"Image": "git@sha256:" + strings.Repeat("a", 64), "Labels": map[string]string{"org.opencontainers.image.revision": revision}}}})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(receipt, body, 0600))
			command := exec.Command("python3", "-", root, receipt)
			command.Stdin = strings.NewReader(code)
			output, err := command.CombinedOutput()
			if scenario == "same Git sources" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err, string(output))
			}
		})
	}
}

func TestEndpointConnectedProfileKeepsItsOwnScaleContract(t *testing.T) {
	cfg := repositoryCapacityConfig{apiA: "http://api-a", apiB: "http://api-b", controllerA: "http://controller-a", controllerB: "http://controller-b", token: "fixture", replacement: "http://api-b", trigger: filepath.Join(t.TempDir(), "replace"), mode: capacityModeProduction, duration: 36 * time.Minute, subscribers: 20, replayEvents: 20, replaySamples: 5, resourcePool: 20, transitionInterval: 500 * time.Millisecond, burstInterval: time.Minute, mutationWorkers: 2, overflowBackpressureWait: repositoryCapacityMinimumOverflowWait}
	require.NoError(t, validateEndpointSplitConnectedScale(cfg))
	require.Error(t, validateRepositoryCapacityScale(cfg), "the repository profile must keep its larger existing gates")
	cfg.subscribers = 19
	require.Error(t, validateEndpointSplitConnectedScale(cfg))
	cfg.subscribers = 20
	cfg.duration = time.Minute
	require.Error(t, validateEndpointSplitConnectedScale(cfg))
}

func TestEndpointEvidenceNeverPromotesUnavailableOrFixtureReads(t *testing.T) {
	for _, tc := range []struct {
		name     string
		admin    int
		wantPass bool
	}{{"503 only", 0, false}, {"boundary", 10, true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input := map[string]any{"metrics": map[string]any{
				"endpoint_public_unsafe_responses": map[string]any{"count": 0}, "endpoint_public_unavailable": map[string]any{"count": 100}, "endpoint_admin_completed": map[string]any{"count": tc.admin},
				"endpoint_admin_unexpected_errors": map[string]any{"value": 0}, "endpoint_public_unexpected_errors": map[string]any{"value": 0}, "endpoint_unexpected_errors": map[string]any{"value": 0}, "checks": map[string]any{"value": 1},
				"endpoint_admin_duration_ms": map[string]any{"p(95)": 1},
			}, "syntheticPublishedReads": 5000000}
			data, err := json.Marshal(input)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), data, 0600))
			command := exec.Command("bash", "../../tests/capacity/verifiers/api-endpoint-split.sh", dir)
			command.Env = append(os.Environ(), "MODE=diagnostic")
			out, err := command.CombinedOutput()
			if tc.wantPass {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err)
			}
			data, err = os.ReadFile(filepath.Join(dir, "domain-verifier.json"))
			require.NoError(t, err)
			var result map[string]any
			require.NoError(t, json.Unmarshal(data, &result))
			require.Equal(t, "blocked", result["publishedCatalog"])
			require.Equal(t, "blocked", result["publicationFixture"])
			if tc.wantPass {
				require.EqualValues(t, 0, result["publicReadSuccesses"])
			}
		})
	}
}

func TestEndpointAcceptanceVerifierChecksSourceAndMeasuredBounds(t *testing.T) {
	for _, scenario := range []string{"valid boundary", "stale source", "resource overflow", "HTTP-only push receipt", "hidden Admin errors", "public data disclosure", "missing resource samples", "failed recovery", "push setup counted as load"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.Mkdir(filepath.Join(dir, "prometheus"), 0700))
			connected := filepath.Join(dir, "connected")
			require.NoError(t, os.Mkdir(connected, 0700))
			write := func(path string, value any) {
				body, err := json.Marshal(value)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(path, body, 0600))
			}
			metadata := map[string]any{"gitRevision": "revision", "sourceStateSha256": "source", "configDigest": "config", "environmentDigest": "environment", "loadStartedAt": "2026-01-01T00:00:00Z", "loadCompletedAt": "2026-01-01T00:35:30Z"}
			write(filepath.Join(dir, "metadata.json"), metadata)
			metrics := map[string]any{
				"endpoint_public_unsafe_responses":     map[string]any{"count": 0},
				"endpoint_public_unavailable":          map[string]any{"count": 100},
				"endpoint_admin_completed":             map[string]any{"count": 10},
				"endpoint_admin_unexpected_errors":     map[string]any{"value": 0},
				"endpoint_public_unexpected_errors":    map[string]any{"value": 0},
				"endpoint_unexpected_errors":           map[string]any{"value": 0},
				"checks":                               map[string]any{"value": 1},
				"endpoint_admin_duration_ms":           map[string]any{"p(95)": 1},
				"endpoint_recovery_admin_duration_ms":  map[string]any{"p(95)": 1},
				"endpoint_recovery_admin_success":      map[string]any{"value": 1},
				"endpoint_recovery_public_unavailable": map[string]any{"value": 1},
			}
			switch scenario {
			case "hidden Admin errors":
				metrics["endpoint_admin_unexpected_errors"] = map[string]any{"value": 0.01}
			case "public data disclosure":
				metrics["endpoint_public_unsafe_responses"] = map[string]any{"count": 1}
			case "failed recovery":
				metrics["endpoint_recovery_admin_duration_ms"] = map[string]any{"p(95)": 2001}
			}
			write(filepath.Join(dir, "summary.json"), map[string]any{"metrics": metrics})
			write(filepath.Join(dir, "publication-fixture.json"), map[string]any{"component": "storefront-identity-fixture/v1", "passed": true, "evidenceClass": "publication-fixture", "publishedCatalog": "blocked", "sourceStateSha256": "source"})
			preflight := map[string]any{"topology": map[string]any{"liveReplicas": []map[string]string{{"instanceID": "proc-a"}, {"instanceID": "proc-b"}}}}
			write(filepath.Join(dir, "preflight-environment.json"), preflight)
			write(filepath.Join(dir, "preflight-environment-before.json"), preflight)
			var rows []any
			point := func(name, instance, endpoint, reason, value string) {
				metric := map[string]string{"__name__": name, "instance": instance}
				if endpoint != "" {
					metric["endpoint"] = endpoint
				}
				if reason != "" {
					metric["reason"] = reason
				}
				rows = append(rows, map[string]any{"metric": metric, "values": [][]any{{1, value}}})
			}
			for _, suffix := range []string{"a", "b"} {
				instance := "api-" + suffix
				rows = append(rows, map[string]any{"metric": map[string]string{"__name__": "gitstore_api_process_instance_info", "instance": instance, "instance_id": "proc-" + suffix}, "values": [][]any{{1, "1"}}})
				point("process_cpu_seconds_total", instance, "", "", "1")
				point("process_resident_memory_bytes", instance, "", "", "1000")
				for _, endpoint := range []string{"admin", "storefront"} {
					for reason, value := range map[string]string{"http": "128", "websocket": "1000", "limiter_keys": "10000"} {
						point("gitstore_api_endpoint_limits", instance, endpoint, reason, value)
					}
				}
				value := "1"
				if scenario == "resource overflow" {
					value = "129"
				}
				point("gitstore_api_endpoint_in_flight", instance, "admin", "http", value)
				if scenario != "missing resource samples" {
					point("gitstore_api_endpoint_in_flight", instance, "storefront", "http", "1")
				}
				point("gitstore_api_endpoint_limiter_keys", instance, "admin", "ip", "1")
				point("gitstore_api_endpoint_limiter_keys", instance, "admin", "principal", "1")
				point("gitstore_api_endpoint_limiter_keys", instance, "storefront", "ip", "1")
				point("gitstore_api_endpoint_connections", instance, "admin", "", "11")
			}
			write(filepath.Join(dir, "prometheus", "endpoint-series.json"), map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": rows}})
			result := map[string]any{"component": "endpoint-split-connected/v1", "passed": true, "subscribers": 20, "durationSeconds": 2160, "failed": 0, "dropped": 0, "offered": 4320, "admitted": 4320, "adminAuthoringP95Ms": 1, "apiRecoveryMs": 1, "gitRevision": "revision", "sourceStateSha256": "source", "configDigest": "config", "environmentDigest": "environment", "startedAt": "2026-01-01T00:00:00Z", "completedAt": "2026-01-01T00:36:00Z"}
			if scenario == "stale source" {
				result["sourceStateSha256"] = "other"
			}
			write(filepath.Join(connected, "connected-result.json"), result)
			push := filepath.Join(dir, "push.json")
			pushReceipt := map[string]any{"runId": "run", "startedAt": "2026-01-01T00:00:00Z", "completedAt": "2026-01-01T00:36:00Z", "pushes": 2160, "failures": 0, "dropped": 0, "intervalMs": 1000, "offered": 2160, "oracleContract": "admin-and-durable-watch/v1", "oracleVerified": 2160, "oracleFailed": 0, "oracleDropped": 0}
			if scenario == "push setup counted as load" {
				pushReceipt["startedAt"] = "2026-01-01T00:00:01Z"
			}
			if scenario == "HTTP-only push receipt" {
				delete(pushReceipt, "oracleContract")
			}
			write(push, pushReceipt)
			command := exec.Command("bash", "../../tests/capacity/verifiers/api-endpoint-split.sh", dir)
			command.Env = append(os.Environ(), "MODE=alpha", "CAPACITY_RUN_ID=run", "CAPACITY_CONNECTED_EVIDENCE="+connected, "CAPACITY_PUSH_EVIDENCE="+push)
			output, err := command.CombinedOutput()
			if scenario == "valid boundary" {
				require.NoError(t, err, string(output))
			} else {
				require.Error(t, err, string(output))
			}
		})
	}
}
