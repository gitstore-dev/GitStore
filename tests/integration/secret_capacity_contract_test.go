// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSecretCapacityDockerDesktopMounts(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "docker"), []byte(`#!/bin/sh
case "$1" in
  context)
    [ "$2" = inspect ] && [ "$3" = --format ] || exit 1
    [ "$5" = "$DOCKER_CONTEXT" ] || exit 1
    printf '%s\n' "$TEST_DOCKER_ENDPOINT"
    ;;
  info)
    [ "$TEST_DOCKER_INFO_FAILURE" != true ] || exit 1
    printf '%s\n' "$TEST_DOCKER_OS"
    ;;
  *) exit 1 ;;
esac
`), 0700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	for _, tc := range []struct {
		name, hostOS, context, host, endpoint, daemonOS string
		infoFailure, want, wantError                    bool
	}{
		{name: "local Desktop context", hostOS: "darwin", endpoint: "unix:///local/docker.sock", daemonOS: "Docker Desktop", want: true},
		{name: "explicit local host", hostOS: "darwin", host: "unix:///local/docker.sock", endpoint: "ssh://remote", daemonOS: "Docker Desktop", want: true},
		{name: "context overrides remote host", hostOS: "darwin", context: "desktop", host: "tcp://remote:2376", endpoint: "unix:///local/docker.sock", daemonOS: "Docker Desktop", want: true},
		{name: "remote context overrides local host", hostOS: "darwin", context: "remote", host: "unix:///local/docker.sock", endpoint: "ssh://remote", daemonOS: "Docker Desktop"},
		{name: "remote host overrides default context", hostOS: "darwin", host: "tcp://remote:2376", endpoint: "unix:///local/docker.sock", daemonOS: "Docker Desktop"},
		{name: "Linux does not translate", hostOS: "linux", endpoint: "unix:///var/run/docker.sock", daemonOS: "Docker Desktop"},
		{name: "other local engine", hostOS: "darwin", endpoint: "unix:///local/docker.sock", daemonOS: "Ubuntu"},
		{name: "Desktop name alone is insufficient", hostOS: "darwin", context: "desktop-linux", endpoint: "ssh://remote", daemonOS: "Docker Desktop"},
		{name: "relative socket rejected", hostOS: "darwin", endpoint: "unix:docker.sock", daemonOS: "Docker Desktop"},
		{name: "socket authority rejected", hostOS: "darwin", endpoint: "unix://remote/docker.sock", daemonOS: "Docker Desktop"},
		{name: "malformed endpoint rejected", hostOS: "darwin", endpoint: "%invalid", daemonOS: "Docker Desktop"},
		{name: "daemon lookup fails closed", hostOS: "darwin", endpoint: "unix:///local/docker.sock", infoFailure: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCKER_CONTEXT", tc.context)
			t.Setenv("DOCKER_HOST", tc.host)
			t.Setenv("TEST_DOCKER_ENDPOINT", tc.endpoint)
			t.Setenv("TEST_DOCKER_OS", tc.daemonOS)
			t.Setenv("TEST_DOCKER_INFO_FAILURE", strconv.FormatBool(tc.infoFailure))
			got, err := secretCapacityDockerDesktopMounts(t.Context(), tc.hostOS)
			require.Equal(t, tc.wantError, err != nil)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestSecretCapacityOwnedMounts(t *testing.T) {
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	root := filepath.Join(parent, "owned-run")
	for _, run := range []string{"owned-run", "owned-run-other"} {
		for _, role := range []string{"controller-a", "controller-b"} {
			require.NoError(t, os.MkdirAll(filepath.Join(parent, run, role, "provider"), 0700))
			require.NoError(t, os.WriteFile(filepath.Join(parent, run, role, "config.toml"), nil, 0600))
		}
	}
	alias := filepath.Join(parent, "alias")
	require.NoError(t, os.Symlink(root, alias))
	for _, representation := range []struct {
		name                      string
		desktop, provider, config bool
	}{
		{name: "Linux"},
		{name: "Desktop native", desktop: true},
		{name: "Desktop mixed directory", desktop: true, provider: true},
		{name: "Desktop mixed file", desktop: true, config: true},
		{name: "Desktop translated", desktop: true, provider: true, config: true},
	} {
		for index, role := range []string{"controller-a", "controller-b"} {
			t.Run(representation.name+"/"+role, func(t *testing.T) {
				mounts := []secretCapacityOwnedMount{
					{Type: "bind", Source: filepath.Join(root, role, "provider"), Destination: "/run/secrets"},
					{Type: "bind", Source: filepath.Join(root, role, "config.toml"), Destination: "/etc/gitstore/gitstore.toml"},
					{Type: "volume", Source: "/var/lib/docker/volumes/checkpoint/_data", Destination: "/var/lib/gitstore/checkpoints", RW: true},
					{Type: "volume", Source: "/var/lib/docker/volumes/bootstrap/_data", Destination: "/run/controller-bootstrap"},
				}
				translate := func(mounts []secretCapacityOwnedMount) {
					if representation.provider {
						mounts[0].Source = "/host_mnt" + mounts[0].Source
					}
					if representation.config {
						mounts[1].Source = "/host_mnt" + mounts[1].Source
					}
				}
				valid := append([]secretCapacityOwnedMount(nil), mounts...)
				translate(valid)
				require.NoError(t, validateSecretCapacityServiceMounts(root, index, valid, representation.desktop))
				if representation.provider || representation.config {
					require.Error(t, validateSecretCapacityServiceMounts(root, index, valid, false))
				}
				for _, tc := range []struct {
					name   string
					change func([]secretCapacityOwnedMount) []secretCapacityOwnedMount
				}{
					{"writable provider", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].RW = true; return m }},
					{"writable config", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[1].RW = true; return m }},
					{"wrong provider destination", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].Destination = "/wrong"; return m }},
					{"wrong config destination", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[1].Destination = "/wrong"; return m }},
					{"provider is not a bind", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].Type = "volume"; return m }},
					{"peer provider", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount {
						m[0].Source = filepath.Join(root, []string{"controller-b", "controller-a"}[index], "provider")
						return m
					}},
					{"peer config", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount {
						m[1].Source = filepath.Join(root, []string{"controller-b", "controller-a"}[index], "config.toml")
						return m
					}},
					{"other run", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount {
						m[0].Source = filepath.Join(parent, "owned-run-other", role, "provider")
						return m
					}},
					{"fixture root", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].Source = root; return m }},
					{"fixture ancestor", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].Source = parent; return m }},
					{"filesystem root", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[0].Source = "/"; return m }},
					{"missing provider", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { return m[1:] }},
					{"duplicate mount", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { return append(m, m[0]) }},
					{"writable bootstrap", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount { m[3].RW = true; return m }},
					{"fixture through checkpoint", func(m []secretCapacityOwnedMount) []secretCapacityOwnedMount {
						m[2].Type, m[2].Source = "bind", root
						return m
					}},
				} {
					t.Run(tc.name, func(t *testing.T) {
						invalid := tc.change(append([]secretCapacityOwnedMount(nil), mounts...))
						for i := range invalid {
							if representation.desktop && (representation.provider || representation.config) && invalid[i].Type == "bind" {
								invalid[i].Source = "/host_mnt" + invalid[i].Source
							}
						}
						require.Error(t, validateSecretCapacityServiceMounts(root, index, invalid, representation.desktop))
					})
				}
				for _, service := range []int{2, 3, 4} {
					require.Error(t, validateSecretCapacityServiceMounts(root, service, valid[:1], representation.desktop))
					require.NoError(t, validateSecretCapacityServiceMounts(root, service, mounts[2:3], representation.desktop))
					ancestor := secretCapacityOwnedMount{Type: "bind", Source: "/", Destination: "/host"}
					require.Error(t, validateSecretCapacityServiceMounts(root, service, []secretCapacityOwnedMount{ancestor}, representation.desktop))
				}
				if representation.desktop {
					aliased := append([]secretCapacityOwnedMount(nil), mounts...)
					aliased[0].Source = "/host_mnt" + filepath.Join(alias, role, "provider")
					require.NoError(t, validateSecretCapacityServiceMounts(root, index, aliased, true))
					aliased[0].Source = "/host_mnt" + filepath.Join(parent, "missing")
					require.Error(t, validateSecretCapacityServiceMounts(root, index, aliased, true))
				}
			})
		}
	}
}

func TestSecretCapacityProductionEvidence(t *testing.T) {
	require.NoError(t, validateSecretCapacityEvidence(secretCapacityEvidenceFixture()))
	cases := []struct {
		name   string
		change func(*secretCapacityEvidence)
	}{
		{"missing scenario", func(e *secretCapacityEvidence) { e.Scenario = "" }},
		{"diagnostic cannot pass", func(e *secretCapacityEvidence) { e.Mode = capacityModeDiagnostic }},
		{"alpha cannot certify production", func(e *secretCapacityEvidence) { e.Mode = capacityModeAlpha }},
		{"repository invariants missing", func(e *secretCapacityEvidence) { e.RepositoryLifecyclePassed = false }},
		{"undersized dataset", func(e *secretCapacityEvidence) {
			e.Dataset.Rows--
			e.Dataset.Titled = e.Dataset.Rows
			e.Dataset.Acknowledged = e.Dataset.Rows
		}},
		{"untitled Products", func(e *secretCapacityEvidence) { e.Dataset.Titled-- }},
		{"unacknowledged fixtures", func(e *secretCapacityEvidence) { e.Dataset.Acknowledged-- }},
		{"count query is not offline proof", func(e *secretCapacityEvidence) { e.Dataset.Method = "graphql-count" }},
		{"counting during load", func(e *secretCapacityEvidence) { e.Dataset.CompletedBeforeLoad = false }},
		{"missing pagination", func(e *secretCapacityEvidence) { e.Dataset.Pages = 0 }},
		{"unbounded page", func(e *secretCapacityEvidence) { e.Dataset.MaxPageRows = e.Dataset.PageLimit + 1 }},
		{"impossible count", func(e *secretCapacityEvidence) { e.Dataset.Pages = 1 }},
		{"short load", func(e *secretCapacityEvidence) { e.Duration -= time.Second }},
		{"short baseline", func(e *secretCapacityEvidence) { e.Baseline -= time.Second }},
		{"short post-load", func(e *secretCapacityEvidence) { e.PostLoad -= time.Second }},
		{"wrong File pool", func(e *secretCapacityEvidence) { e.FilePool-- }},
		{"wrong worker count", func(e *secretCapacityEvidence) { e.Workers++ }},
		{"unbounded queue", func(e *secretCapacityEvidence) { e.QueueLimit++ }},
		{"observed queue exceeded bound", func(e *secretCapacityEvidence) { e.QueuePeak++ }},
		{"observed worker count exceeded bound", func(e *secretCapacityEvidence) { e.PushWorkersPeak++ }},
		{"missing worker measurement", func(e *secretCapacityEvidence) { e.PushWorkersPeak = 0 }},
		{"empty queue", func(e *secretCapacityEvidence) { e.QueueLimit = 0 }},
		{"oversized File batch", func(e *secretCapacityEvidence) { e.MaxFilesPerPush++ }},
		{"oversized push", func(e *secretCapacityEvidence) { e.MaxBytesPerPush++ }},
		{"empty pushes", func(e *secretCapacityEvidence) { e.MaxFilesPerPush = 0 }},
		{"reduced offered rate", func(e *secretCapacityEvidence) { e.PushInterval += time.Millisecond }},
		{"missing sustained offer", func(e *secretCapacityEvidence) { e.SustainedOffered-- }},
		{"missed scheduled offer", func(e *secretCapacityEvidence) { e.MissedSchedules++ }},
		{"missing minute burst", func(e *secretCapacityEvidence) { e.Bursts = e.Bursts[:59] }},
		{"duplicate burst minute", func(e *secretCapacityEvidence) { e.Bursts[1].At = e.Bursts[0].At }},
		{"reduced burst", func(e *secretCapacityEvidence) { e.Bursts[0].Offered-- }},
		{"slow burst", func(e *secretCapacityEvidence) { e.Bursts[0].OfferDuration += time.Nanosecond }},
		{"slow drain", func(e *secretCapacityEvidence) { e.Bursts[0].Drain += time.Nanosecond }},
		{"metadata-only load", func(e *secretCapacityEvidence) { e.GitPushes = 0 }},
		{"missing acknowledgments", func(e *secretCapacityEvidence) { e.Acknowledged-- }},
		{"healthy errors at threshold", func(e *secretCapacityEvidence) {
			e.Failed = 42
			e.Acknowledged -= 42
		}},
		{"dropped offers", func(e *secretCapacityEvidence) { e.Dropped++ }},
		{"projection loss", func(e *secretCapacityEvidence) { e.ProjectionLoss++ }},
		{"cross-namespace success", func(e *secretCapacityEvidence) { e.CrossNamespaceSuccess++ }},
		{"credential leakage", func(e *secretCapacityEvidence) { e.LeakedArtifacts++ }},
		{"untyped File traffic", func(e *secretCapacityEvidence) { e.TypedFiles = 0 }},
		{"no reference samples", func(e *secretCapacityEvidence) { e.Reference.Samples = 0 }},
		{"partial reference measurement", func(e *secretCapacityEvidence) { e.Reference.Samples-- }},
		{"reference p95", func(e *secretCapacityEvidence) { e.Reference.P95 += time.Nanosecond }},
		{"reference p99", func(e *secretCapacityEvidence) { e.Reference.P99 += time.Nanosecond }},
		{"no resolution samples", func(e *secretCapacityEvidence) { e.Resolution.Samples = 0 }},
		{"healthy resolver errors at threshold", func(e *secretCapacityEvidence) { e.LocalHealthyFailures = 42 }},
		{"negative resolver error count", func(e *secretCapacityEvidence) { e.LocalHealthyFailures = -1 }},
		{"resolution p95", func(e *secretCapacityEvidence) { e.Resolution.P95 += time.Nanosecond }},
		{"resolution p99", func(e *secretCapacityEvidence) { e.Resolution.P99 += time.Nanosecond }},
		{"push p95", func(e *secretCapacityEvidence) { e.Push.P95 += time.Nanosecond }},
		{"push p99", func(e *secretCapacityEvidence) { e.Push.P99 += time.Nanosecond }},
		{"negative timing", func(e *secretCapacityEvidence) { e.Push.P95 = -1 }},
		{"inverted percentiles", func(e *secretCapacityEvidence) { e.Push.P99 = time.Second }},
		{"partial push measurement", func(e *secretCapacityEvidence) { e.Push.Samples-- }},
		{"insufficient contention", func(e *secretCapacityEvidence) { e.ResolverCallers-- }},
		{"excess provider concurrency", func(e *secretCapacityEvidence) { e.ProviderPeak++ }},
		{"saturation not reached", func(e *secretCapacityEvidence) { e.ProviderPeak-- }},
		{"provider queue", func(e *secretCapacityEvidence) { e.ProviderQueued++ }},
		{"provider retries", func(e *secretCapacityEvidence) { e.ProviderRetries++ }},
		{"missing saturation rejection", func(e *secretCapacityEvidence) { e.SaturationDenied = 0 }},
		{"missing deadline rejection", func(e *secretCapacityEvidence) { e.DeadlineDenied = 0 }},
		{"single API", func(e *secretCapacityEvidence) {
			e.Processes = e.Processes[1:]
			e.ScannedProcesses = int64(len(e.Processes))
		}},
		{"single controller", func(e *secretCapacityEvidence) {
			e.Processes = append(e.Processes[:2], e.Processes[3:]...)
			e.Controllers = e.Controllers[1:]
			e.RestartTargetID = e.Controllers[0].ID
			e.ScannedProcesses = int64(len(e.Processes))
		}},
		{"multiple Git services", func(e *secretCapacityEvidence) {
			git := e.Processes[4]
			git.ID = "second-git"
			e.Processes = append(e.Processes, git)
			e.ScannedProcesses = int64(len(e.Processes))
		}},
		{"duplicate process identity", func(e *secretCapacityEvidence) { e.Processes[1].ID = e.Processes[0].ID }},
		{"CPU at limit", func(e *secretCapacityEvidence) { e.Processes[0].Cpu = .8 }},
		{"nonfinite CPU", func(e *secretCapacityEvidence) { e.Processes[0].Cpu = math.NaN() }},
		{"RSS at limit", func(e *secretCapacityEvidence) { e.Processes[0].RssAfter = 110 }},
		{"goroutines above limit", func(e *secretCapacityEvidence) { e.Processes[0].GoroutinesAfter = 111 }},
		{"missing resource samples", func(e *secretCapacityEvidence) { e.Processes[0].Samples = 0 }},
		{"missing warmed RSS", func(e *secretCapacityEvidence) { e.Processes[0].RssBefore = 0 }},
		{"missing goroutines", func(e *secretCapacityEvidence) { e.Processes[0].GoroutinesBefore = 0 }},
		{"long-lived tokens", func(e *secretCapacityEvidence) { e.Controllers[0].TokenTTL += time.Second }},
		{"parallel identity exchanges", func(e *secretCapacityEvidence) { e.Controllers[0].ExchangePeak++ }},
		{"retry above cap", func(e *secretCapacityEvidence) { e.Controllers[0].MaxRetry += time.Nanosecond }},
		{"missing fresh token", func(e *secretCapacityEvidence) { e.Controllers[0].FreshTokens = 0 }},
		{"no repeated token exchange", func(e *secretCapacityEvidence) { e.Controllers[0].FreshTokens = 1 }},
		{"no reconciler progress", func(e *secretCapacityEvidence) { e.Controllers[0].Reconciliations = 0 }},
		{"recovery too slow", func(e *secretCapacityEvidence) { e.Controllers[1].Recovery += time.Nanosecond }},
		{"missing controller evidence", func(e *secretCapacityEvidence) { e.Controllers = e.Controllers[:1] }},
		{"unmeasured controller", func(e *secretCapacityEvidence) { e.Controllers[0].ID = "unknown" }},
		{"wrong outage schedule", func(e *secretCapacityEvidence) { e.OutageAt += time.Minute }},
		{"outage shorter than required", func(e *secretCapacityEvidence) { e.OutageDuration -= time.Second }},
		{"readiness extended past expiry", func(e *secretCapacityEvidence) { e.ExpiredUnready = false }},
		{"peer affected", func(e *secretCapacityEvidence) { e.PeerFailures++ }},
		{"missing classified outage failure", func(e *secretCapacityEvidence) { e.ClassifiedOutageFailures = 0 }},
		{"wrong rotation schedule", func(e *secretCapacityEvidence) { e.RotationAt += time.Minute }},
		{"missing overlap renewal", func(e *secretCapacityEvidence) { e.Controllers[0].OverlapRenewals = 0 }},
		{"missing old-key denial", func(e *secretCapacityEvidence) { e.RetiredKeyDenied = 0 }},
		{"missing wrong-subject denial", func(e *secretCapacityEvidence) { e.WrongSubjectDenied = 0 }},
		{"wrong restart schedule", func(e *secretCapacityEvidence) { e.RestartAt += time.Minute }},
		{"unconfirmed restart", func(e *secretCapacityEvidence) { e.RestartConfirmed = false }},
		{"restart aimed at API", func(e *secretCapacityEvidence) { e.RestartTargetID = e.Processes[0].ID }},
		{"replacement never authenticated", func(e *secretCapacityEvidence) { e.ReplacementFreshTokens = 0 }},
		{"replacement made no progress", func(e *secretCapacityEvidence) { e.ReplacementReconciliations = 0 }},
		{"replacement recovery too slow", func(e *secretCapacityEvidence) { e.ReplacementRecovery += time.Nanosecond }},
		{"unchanged replacement identity", func(e *secretCapacityEvidence) { e.ReplacementID = e.Controllers[0].ID }},
		{"missing replacement identity", func(e *secretCapacityEvidence) { e.ReplacementID = "" }},
		{"API replacement loses fields", func(e *secretCapacityEvidence) { e.CrossReplicaProjectionChecks = 0 }},
		{"unscanned telemetry", func(e *secretCapacityEvidence) { e.ScannedProcesses = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := secretCapacityEvidenceFixture()
			tc.change(&e)
			require.Error(t, validateSecretCapacityEvidence(e))
		})
	}
}

func TestSecretCapacityStrictAndInclusiveThresholds(t *testing.T) {
	e := secretCapacityEvidenceFixture()
	e.Failed = 41
	e.LocalHealthyFailures = 41
	e.Acknowledged -= 41
	e.Processes[0].GoroutinesAfter = 110
	require.NoError(t, validateSecretCapacityEvidence(e), "healthy errors below 0.1%% and goroutines at +10%% are permitted")
}

type secretCapacityDatasetProof struct {
	Method              string `json:"method"`
	Rows                int64  `json:"rows"`
	Titled              int64  `json:"titled"`
	Acknowledged        int64  `json:"acknowledged"`
	Pages               int64  `json:"pages"`
	PageLimit           int64  `json:"pageLimit"`
	MaxPageRows         int64  `json:"maxPageRows"`
	CompletedBeforeLoad bool   `json:"completedBeforeLoad"`
}

type secretCapacityLatency struct {
	Samples int64         `json:"samples"`
	P95     time.Duration `json:"p95"`
	P99     time.Duration `json:"p99"`
}

type secretCapacityBurst struct {
	At            time.Duration `json:"at"`
	OfferDuration time.Duration `json:"offerDuration"`
	Drain         time.Duration `json:"drain"`
	Offered       int64         `json:"offered"`
}

type secretCapacityProcessProof struct {
	Role             string  `json:"role"`
	ID               string  `json:"id"`
	Samples          int64   `json:"samples"`
	Cpu              float64 `json:"cpu"`
	RssBefore        uint64  `json:"rssBefore"`
	RssAfter         uint64  `json:"rssAfter"`
	GoroutinesBefore int64   `json:"goroutinesBefore"`
	GoroutinesAfter  int64   `json:"goroutinesAfter"`
}

type secretCapacityControllerProof struct {
	ID              string        `json:"id"`
	TokenTTL        time.Duration `json:"tokenTTL"`
	MaxRetry        time.Duration `json:"maxRetry"`
	Recovery        time.Duration `json:"recovery"`
	ExchangePeak    int           `json:"exchangePeak"`
	FreshTokens     int64         `json:"freshTokens"`
	Reconciliations int64         `json:"reconciliations"`
	OverlapRenewals int64         `json:"overlapRenewals"`
}

// These assertions are shared by the eventual deployed collector and its
// negative fixtures. They do not enable the guarded capacity scenario.
type secretCapacityEvidence struct {
	Scenario                     string                          `json:"scenario"`
	Mode                         capacityMode                    `json:"mode"`
	RepositoryLifecyclePassed    bool                            `json:"repositoryLifecyclePassed"`
	Dataset                      secretCapacityDatasetProof      `json:"dataset"`
	Duration                     time.Duration                   `json:"duration"`
	OfferedDuration              time.Duration                   `json:"offeredDuration"`
	Baseline                     time.Duration                   `json:"baseline"`
	PostLoad                     time.Duration                   `json:"postLoad"`
	FilePool                     int                             `json:"filePool"`
	Workers                      int                             `json:"workers"`
	QueueLimit                   int                             `json:"queueLimit"`
	QueuePeak                    int                             `json:"queuePeak"`
	PushWorkersPeak              int                             `json:"pushWorkersPeak"`
	MaxFilesPerPush              int                             `json:"maxFilesPerPush"`
	MaxBytesPerPush              int                             `json:"maxBytesPerPush"`
	PushInterval                 time.Duration                   `json:"pushInterval"`
	SustainedOffered             int64                           `json:"sustainedOffered"`
	MissedSchedules              int64                           `json:"missedSchedules"`
	Bursts                       []secretCapacityBurst           `json:"bursts"`
	GitPushes                    int64                           `json:"gitPushes"`
	Acknowledged                 int64                           `json:"acknowledged"`
	Failed                       int64                           `json:"failed"`
	Dropped                      int64                           `json:"dropped"`
	TypedFiles                   int64                           `json:"typedFiles"`
	ProjectionLoss               int64                           `json:"projectionLoss"`
	CrossNamespaceSuccess        int64                           `json:"crossNamespaceSuccess"`
	LeakedArtifacts              int64                           `json:"leakedArtifacts"`
	ScannedProcesses             int64                           `json:"scannedProcesses"`
	Reference                    secretCapacityLatency           `json:"reference"`
	Resolution                   secretCapacityLatency           `json:"resolution"`
	Push                         secretCapacityLatency           `json:"push"`
	LocalHealthyFailures         int64                           `json:"localHealthyFailures"`
	ResolverCallers              int                             `json:"resolverCallers"`
	ProviderPeak                 int                             `json:"providerPeak"`
	ProviderQueued               int64                           `json:"providerQueued"`
	ProviderRetries              int64                           `json:"providerRetries"`
	SaturationDenied             int64                           `json:"saturationDenied"`
	DeadlineDenied               int64                           `json:"deadlineDenied"`
	Processes                    []secretCapacityProcessProof    `json:"processes"`
	Controllers                  []secretCapacityControllerProof `json:"controllers"`
	OutageAt                     time.Duration                   `json:"outageAt"`
	OutageDuration               time.Duration                   `json:"outageDuration"`
	OutageElapsed                time.Duration                   `json:"outageElapsed"`
	ExpiredUnready               bool                            `json:"expiredUnready"`
	PeerFailures                 int64                           `json:"peerFailures"`
	ClassifiedOutageFailures     int64                           `json:"classifiedOutageFailures"`
	RotationAt                   time.Duration                   `json:"rotationAt"`
	RetiredKeyDenied             int64                           `json:"retiredKeyDenied"`
	WrongSubjectDenied           int64                           `json:"wrongSubjectDenied"`
	RestartAt                    time.Duration                   `json:"restartAt"`
	RestartConfirmed             bool                            `json:"restartConfirmed"`
	RestartTargetID              string                          `json:"restartTargetID"`
	ReplacementID                string                          `json:"replacementID"`
	APIReplacementID             string                          `json:"apiReplacementID"`
	ReplacementFreshTokens       int64                           `json:"replacementFreshTokens"`
	ReplacementReconciliations   int64                           `json:"replacementReconciliations"`
	ReplacementRecovery          time.Duration                   `json:"replacementRecovery"`
	CrossReplicaProjectionChecks int64                           `json:"crossReplicaProjectionChecks"`
}

func validateSecretCapacityEvidence(e secretCapacityEvidence) error {
	return validateSecretCapacityEvidenceMode(e, true)
}

func validateSecretCapacityEvidenceMode(e secretCapacityEvidence, requireProduction bool) error {
	var failures []error
	check := func(ok bool, name string) {
		if !ok {
			failures = append(failures, fmt.Errorf("secret capacity: %s", name))
		}
	}
	check(e.Scenario == "repository-lifecycle-secrets/v1", "missing scenario identity")
	check(e.Mode == capacityModeProduction || e.Mode == capacityModeAlpha || e.Mode == capacityModeDiagnostic, "invalid evidence mode")
	check(!requireProduction || e.Mode == capacityModeProduction, "only production evidence can satisfy this contract")
	check(e.RepositoryLifecyclePassed, "existing Repository lifecycle invariants must also pass")
	d := e.Dataset
	check(d.Method == "offline-paginated" && d.CompletedBeforeLoad, "dataset must be verified offline before load")
	check(d.Rows > 0 && (e.Mode != capacityModeProduction || d.Rows >= 5_000_000) &&
		d.Rows == d.Titled && d.Rows == d.Acknowledged, "dataset needs acknowledged titled Products and five million rows in production")
	check(d.PageLimit > 0 && d.PageLimit <= 1000 && d.MaxPageRows > 0 && d.MaxPageRows <= d.PageLimit && d.Pages <= d.Rows &&
		d.Rows > 0 && d.Pages > 0 && d.Pages >= (d.Rows-1)/d.MaxPageRows+1, "missing or inconsistent bounded pagination proof")
	check(e.Duration == 60*time.Minute && e.Baseline > 0 && e.PostLoad > 0 &&
		(e.Mode != capacityModeProduction || (e.Baseline >= 5*time.Minute && e.PostLoad >= 10*time.Minute)), "load and stabilization durations do not meet the contract")
	check(e.OfferedDuration >= e.Duration && e.OfferedDuration <= e.Duration+e.PushInterval,
		"measured offering period must cover the nominal schedule without an extra cadence of drift")
	check(e.FilePool == 100 && e.Workers == 32 && e.QueueLimit > 0 && e.QueueLimit <= 256, "File pool, workers or queue outside bounds")
	check(e.QueuePeak >= 0 && e.QueuePeak <= e.QueueLimit && e.PushWorkersPeak > 0 && e.PushWorkersPeak <= e.Workers, "observed client concurrency or queue outside bounds")
	check(e.MaxFilesPerPush > 0 && e.MaxFilesPerPush <= 10 && e.MaxBytesPerPush > 0 && e.MaxBytesPerPush <= 128*1024, "push batch outside bounds")
	check(e.PushInterval == 100*time.Millisecond && e.SustainedOffered == 36_000 && e.MissedSchedules == 0, "sustained offered load was reduced")
	check(len(e.Bursts) == 60, "one burst per minute is required")
	for i, burst := range e.Bursts {
		check(burst.At >= 0 && burst.At/time.Minute == time.Duration(i) && burst.At+burst.OfferDuration <= e.Duration &&
			burst.Offered == 100 && burst.OfferDuration > 0 && burst.OfferDuration <= time.Second &&
			burst.Drain >= 0 && burst.Drain <= 30*time.Second, "burst schedule, offered count or drain outside bounds")
	}
	check(e.GitPushes == 42_000 && e.Acknowledged >= 0 && e.Failed >= 0 && e.Failed <= e.GitPushes &&
		e.Acknowledged == e.GitPushes-e.Failed && e.Dropped == 0, "all offered batches must execute real Git pushes and have accounted outcomes")
	check(e.Failed >= 0 && e.Failed < 42, "unexpected healthy push errors must be below 0.1 percent")
	check(e.TypedFiles >= e.GitPushes && e.TypedFiles <= e.GitPushes*10, "every push must contain typed File traffic")
	check(e.ProjectionLoss == 0 && e.CrossNamespaceSuccess == 0 && e.LeakedArtifacts == 0, "integrity, namespace isolation or credential scanning failed")
	check(e.CrossReplicaProjectionChecks >= 2, "typed projections must survive API replacement and cross-replica reads")
	latency := func(value secretCapacityLatency, p95, p99 time.Duration) bool {
		return value.Samples > 0 && value.P95 >= 0 && value.P99 >= value.P95 && value.P95 <= p95 && value.P99 <= p99
	}
	check(latency(e.Reference, 5*time.Millisecond, 20*time.Millisecond) && e.Reference.Samples == e.TypedFiles, "per-File reference latency evidence missing or over threshold")
	check(latency(e.Resolution, 10*time.Millisecond, 50*time.Millisecond), "healthy local resolution latency evidence missing or over threshold")
	check(e.LocalHealthyFailures >= 0 && e.Resolution.Samples > 0 &&
		float64(e.LocalHealthyFailures)/float64(e.Resolution.Samples) < .001, "unexpected healthy resolver errors must be below 0.1 percent")
	check(latency(e.Push, 2*time.Second, 30*time.Second) && e.Push.Samples == e.GitPushes, "push latency must cover sustained and burst traffic")
	check(e.ResolverCallers == 32 && e.ProviderPeak == 16 && e.ProviderQueued == 0 && e.ProviderRetries == 0 &&
		e.SaturationDenied > 0 && e.DeadlineDenied > 0, "bounded resolver contention/deadline proof missing")

	identities := make(map[string]string, len(e.Processes))
	roles := make(map[string]int)
	for _, process := range e.Processes {
		_, duplicate := identities[process.ID]
		check(process.ID != "" && !duplicate, "process identities must be present and unique")
		identities[process.ID] = process.Role
		roles[process.Role]++
		check(process.Role == "api" || process.Role == "controller" || process.Role == "git", "unknown measured process role")
		check(process.Samples > 0 && process.Cpu >= 0 && process.Cpu < .8, "normalized CPU must be measured and below 80 percent")
		check(process.RssBefore > 0 && process.RssAfter > 0 &&
			float64(process.RssAfter)/float64(process.RssBefore) < 1.1, "warmed RSS growth must be below 10 percent")
		if process.Role != "git" {
			check(process.GoroutinesBefore > 0 && process.GoroutinesAfter > 0 &&
				float64(process.GoroutinesAfter)/float64(process.GoroutinesBefore) <= 1.1, "post-load goroutines must return within 10 percent")
		}
	}
	check(roles["api"] >= 2 && roles["controller"] >= 2 && roles["git"] == 1, "requires two APIs/two controllers and exactly one Git service")
	check(e.ScannedProcesses == int64(len(e.Processes)), "all measured processes need telemetry scanning")
	controllerIDs := make(map[string]bool, len(e.Controllers))
	for _, controller := range e.Controllers {
		check(identities[controller.ID] == "controller" && !controllerIDs[controller.ID], "controller proof must match distinct measured processes")
		controllerIDs[controller.ID] = true
		check(controller.TokenTTL == 60*time.Second && controller.ExchangePeak == 1 &&
			controller.MaxRetry >= 0 && controller.MaxRetry <= 30*time.Second, "identity TTL, single-flight or retry cap not demonstrated")
		check(controller.FreshTokens >= 2 && controller.Reconciliations > 0 && controller.OverlapRenewals > 0,
			"every controller must authenticate, reconcile and renew through overlap")
		check(controller.Recovery > 0 && controller.Recovery <= 60*time.Second, "each controller needs recovery within 60 seconds")
	}
	check(len(e.Controllers) == roles["controller"], "missing controller proof")
	check(e.OutageAt/time.Minute == 15 && e.OutageDuration == 90*time.Second &&
		e.OutageElapsed >= e.OutageDuration && e.OutageElapsed <= e.OutageDuration+time.Second && e.ExpiredUnready &&
		e.PeerFailures == 0 && e.ClassifiedOutageFailures > 0, "minute-15 expiry-spanning isolated outage not demonstrated")
	check(e.RotationAt/time.Minute == 30 && e.RetiredKeyDenied > 0 && e.WrongSubjectDenied > 0, "minute-30 rotation/retirement and authorization denial missing")
	_, reusedIdentity := identities[e.ReplacementID]
	_, reusedAPIIdentity := identities[e.APIReplacementID]
	check(e.APIReplacementID != "" && e.APIReplacementID != e.ReplacementID && !reusedAPIIdentity,
		"API replacement requires a distinct observed process identity")
	check(e.RestartAt/time.Minute == 45 && e.RestartConfirmed && controllerIDs[e.RestartTargetID] &&
		e.ReplacementID != "" && !reusedIdentity, "minute-45 restart needs an explicit controller target and new process identity")
	check(e.ReplacementFreshTokens > 0 && e.ReplacementReconciliations > 0 &&
		e.ReplacementRecovery > 0 && e.ReplacementRecovery <= 60*time.Second, "replacement must freshly authenticate and resume reconciliation within 60 seconds")
	return errors.Join(failures...)
}

func secretCapacityEvidenceFixture() secretCapacityEvidence {
	e := secretCapacityEvidence{
		Scenario: "repository-lifecycle-secrets/v1", Mode: capacityModeProduction, RepositoryLifecyclePassed: true,
		Dataset: secretCapacityDatasetProof{
			Method: "offline-paginated", Rows: 5_000_000, Titled: 5_000_000, Acknowledged: 5_000_000,
			Pages: 5000, PageLimit: 1000, MaxPageRows: 1000, CompletedBeforeLoad: true,
		},
		Duration: time.Hour, OfferedDuration: time.Hour, Baseline: 5 * time.Minute, PostLoad: 10 * time.Minute,
		FilePool: 100, Workers: 32, QueueLimit: 256, MaxFilesPerPush: 10, MaxBytesPerPush: 128 * 1024,
		QueuePeak: 256, PushWorkersPeak: 32,
		PushInterval: 100 * time.Millisecond, SustainedOffered: 36_000, GitPushes: 42_000, Acknowledged: 42_000,
		TypedFiles: 42_000, CrossReplicaProjectionChecks: 2,
		Reference:       secretCapacityLatency{42_000, 5 * time.Millisecond, 20 * time.Millisecond},
		Resolution:      secretCapacityLatency{42_000, 10 * time.Millisecond, 50 * time.Millisecond},
		Push:            secretCapacityLatency{42_000, 2 * time.Second, 30 * time.Second},
		ResolverCallers: 32, ProviderPeak: 16, SaturationDenied: 16, DeadlineDenied: 1,
		OutageAt: 15 * time.Minute, OutageDuration: 90 * time.Second, OutageElapsed: 90 * time.Second, ExpiredUnready: true, ClassifiedOutageFailures: 1,
		RotationAt: 30 * time.Minute, RetiredKeyDenied: 1, WrongSubjectDenied: 1,
		RestartAt: 45 * time.Minute, RestartConfirmed: true, RestartTargetID: "controller-a", ReplacementID: "controller-replacement",
		APIReplacementID:       "api-replacement",
		ReplacementFreshTokens: 1, ReplacementReconciliations: 1, ReplacementRecovery: time.Minute,
	}
	for i := range 60 {
		e.Bursts = append(e.Bursts, secretCapacityBurst{time.Duration(i) * time.Minute, time.Second, 30 * time.Second, 100})
	}
	for _, process := range []struct{ Role, ID string }{
		{"api", "api-a"}, {"api", "api-b"}, {"controller", "controller-a"}, {"controller", "controller-b"}, {"git", "git"},
	} {
		e.Processes = append(e.Processes, secretCapacityProcessProof{
			Role: process.Role, ID: process.ID, Samples: 3600, Cpu: .79,
			RssBefore: 100, RssAfter: 109, GoroutinesBefore: 100, GoroutinesAfter: 109,
		})
		if process.Role == "controller" {
			e.Controllers = append(e.Controllers, secretCapacityControllerProof{
				ID: process.ID, TokenTTL: time.Minute, MaxRetry: 30 * time.Second, Recovery: time.Minute,
				ExchangePeak: 1, FreshTokens: 120, Reconciliations: 120, OverlapRenewals: 1,
			})
		}
	}
	e.ScannedProcesses = int64(len(e.Processes))
	return e
}

const secretCapacityJSONLimit = 2 * 1024 * 1024

type secretCapacityArtifact struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	ProcessID string `json:"processID"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
}

func decodeSecretCapacityObservations(data []byte) (secretCapacityEvidence, error) {
	var observations secretCapacityEvidence
	if err := decodeSecretCapacityJSON(data, &observations); err != nil {
		return observations, err
	}
	return observations, nil
}

// All fields, including zero-valued counters, are required. encoding/json alone
// accepts duplicate keys, case-folded names and nulls for scalar observations.
func decodeSecretCapacityJSON(data []byte, destination any) error {
	invalid := errors.New("secret capacity: incomplete or invalid evidence JSON")
	if len(data) == 0 || len(data) > secretCapacityJSONLimit {
		return invalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := checkSecretCapacityJSON(decoder, reflect.TypeOf(destination).Elem(), 0); err != nil {
		return invalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid
	}
	if err := json.Unmarshal(data, destination); err != nil {
		return invalid
	}
	return nil
}

func checkSecretCapacityJSON(decoder *json.Decoder, shape reflect.Type, depth int) error {
	invalid := errors.New("invalid observation shape")
	if depth > 16 {
		return invalid
	}
	token, err := decoder.Token()
	if err != nil || token == nil {
		return invalid
	}
	if shape == reflect.TypeFor[time.Time]() {
		value, ok := token.(string)
		if !ok {
			return invalid
		}
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return invalid
		}
		return nil
	}
	switch shape.Kind() {
	case reflect.Struct:
		if token != json.Delim('{') {
			return invalid
		}
		fields := make(map[string]reflect.Type, shape.NumField())
		for i := range shape.NumField() {
			field := shape.Field(i)
			fields[field.Tag.Get("json")] = field.Type
		}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return invalid
			}
			name, ok := key.(string)
			field, exists := fields[name]
			if !ok || !exists {
				return invalid
			}
			delete(fields, name)
			if err := checkSecretCapacityJSON(decoder, field, depth+1); err != nil {
				return invalid
			}
		}
		if len(fields) != 0 {
			return invalid
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return invalid
		}
	case reflect.Map:
		if shape.Key().Kind() != reflect.String || token != json.Delim('{') {
			return invalid
		}
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] || len(seen) >= 8192 {
				return invalid
			}
			seen[name] = true
			if err := checkSecretCapacityJSON(decoder, shape.Elem(), depth+1); err != nil {
				return invalid
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim('}') {
			return invalid
		}
	case reflect.Slice:
		if token != json.Delim('[') {
			return invalid
		}
		count := 0
		for decoder.More() {
			count++
			if count > 8192 {
				return invalid
			}
			if err := checkSecretCapacityJSON(decoder, shape.Elem(), depth+1); err != nil {
				return invalid
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return invalid
		}
	case reflect.String:
		if _, ok := token.(string); !ok {
			return invalid
		}
	case reflect.Bool:
		if _, ok := token.(bool); !ok {
			return invalid
		}
	case reflect.Int, reflect.Int64, reflect.Uint64, reflect.Float64:
		if _, ok := token.(json.Number); !ok {
			return invalid
		}
	default:
		return invalid
	}
	return nil
}

func openSecretCapacityArtifact(root *os.Root, name string) (*os.File, error) {
	invalid := errors.New("secret capacity: artifact must be a contained regular file")
	if !fs.ValidPath(name) || name == "." || strings.Contains(name, "\\") {
		return nil, invalid
	}
	for component := name; component != "."; component = path.Dir(component) {
		info, err := root.Lstat(component)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || (component == name && !info.Mode().IsRegular()) {
			return nil, invalid
		}
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, invalid
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, invalid
	}
	return file, nil
}

func verifySecretCapacityArtifact(directory string, artifact secretCapacityArtifact) error {
	invalid := errors.New("secret capacity: invalid artifact declaration or digest")
	if artifact.Kind != "log" && artifact.Kind != "metrics" && artifact.Kind != "trace" && artifact.Kind != "summary" {
		return invalid
	}
	if artifact.Bytes <= 0 || artifact.Bytes > 128*1024*1024 || len(artifact.SHA256) != 64 {
		return invalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return invalid
	}
	defer root.Close()
	file, err := openSecretCapacityArtifact(root, artifact.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(file, artifact.Bytes+1))
	if err != nil || n != artifact.Bytes || hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		return invalid
	}
	return nil
}

type secretCapacityBundle struct {
	SchemaVersion int                      `json:"schemaVersion"`
	RunID         string                   `json:"runID"`
	GitRevision   string                   `json:"gitRevision"`
	Observations  secretCapacityEvidence   `json:"observations"`
	Artifacts     []secretCapacityArtifact `json:"artifacts"`
}

// This validates a completed, immutable collection, not an in-progress log.
// The caller must supply the expected run/revision from independent provenance.
func loadSecretCapacityBundle(directory, runID, revision string, secrets []string) (secretCapacityBundle, error) {
	return loadSecretCapacityBundleMode(directory, runID, revision, secrets, true)
}

func loadSecretCapacityBundleMode(directory, runID, revision string, secrets []string, requireProduction bool) (secretCapacityBundle, error) {
	var bundle secretCapacityBundle
	invalid := errors.New("secret capacity: incomplete or inconsistent artifact bundle")
	if runID == "" || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(revision) {
		return bundle, invalid
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return bundle, invalid
	}
	defer root.Close()
	file, err := openSecretCapacityArtifact(root, "secret-evidence.json")
	if err != nil {
		return bundle, invalid
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, secretCapacityJSONLimit+1))
	if err != nil || decodeSecretCapacityJSON(data, &bundle) != nil {
		return secretCapacityBundle{}, invalid
	}
	if bundle.SchemaVersion != 1 || bundle.RunID != runID || bundle.GitRevision != revision {
		return secretCapacityBundle{}, invalid
	}
	if err := validateSecretCapacityEvidenceMode(bundle.Observations, requireProduction); err != nil {
		return secretCapacityBundle{}, err
	}
	if len(bundle.Artifacts) == 0 || len(bundle.Artifacts) > 512 {
		return secretCapacityBundle{}, invalid
	}
	processes := make(map[string]map[string]bool)
	for _, process := range bundle.Observations.Processes {
		processes[process.ID] = make(map[string]bool)
	}
	processes[bundle.Observations.ReplacementID] = make(map[string]bool)
	processes[bundle.Observations.APIReplacementID] = make(map[string]bool)
	driverID := "driver:" + runID
	if _, exists := processes[driverID]; exists {
		return secretCapacityBundle{}, invalid
	}
	processes[driverID] = make(map[string]bool)
	paths := make(map[string]bool)
	for _, artifact := range bundle.Artifacts {
		kinds, exists := processes[artifact.ProcessID]
		if !exists || paths[artifact.Path] || artifact.Path == "secret-evidence.json" {
			return secretCapacityBundle{}, invalid
		}
		paths[artifact.Path] = true
		kinds[artifact.Kind] = true
		if err := verifySecretCapacityArtifact(directory, artifact); err != nil {
			return secretCapacityBundle{}, err
		}
	}
	for id, kinds := range processes {
		if id == driverID {
			continue
		}
		if !kinds["log"] || !kinds["metrics"] {
			return secretCapacityBundle{}, invalid
		}
	}
	// Enumerate actual files, not just the submitted manifest: unlisted artifacts
	// must not evade either integrity validation or the credential scan.
	err = walkSecretCapacityArtifacts(root, func(name string, directory bool) error {
		if !directory && name != "secret-evidence.json" && !paths[name] {
			return invalid
		}
		return nil
	})
	if err != nil {
		return secretCapacityBundle{}, invalid
	}
	if err := scanSecretCapacityArtifacts(directory, secrets); err != nil {
		return secretCapacityBundle{}, err
	}
	return bundle, nil
}

func walkSecretCapacityArtifacts(root *os.Root, visit func(string, bool) error) error {
	invalid := errors.New("secret capacity: artifact tree contains unsafe files or exceeds bounds")
	entries := 0
	var walk func(string, int) error
	walk = func(name string, depth int) error {
		if depth > 16 {
			return invalid
		}
		directory, err := root.Open(name)
		if err != nil {
			return invalid
		}
		defer directory.Close()
		for {
			children, err := directory.ReadDir(64)
			if err != nil && err != io.EOF {
				return invalid
			}
			for _, child := range children {
				entries++
				if entries > 1024 || child.Type()&os.ModeSymlink != 0 {
					return invalid
				}
				childName := path.Join(name, child.Name())
				if err := visit(childName, child.IsDir()); err != nil {
					return err
				}
				if child.IsDir() {
					if err := walk(childName, depth+1); err != nil {
						return err
					}
				} else {
					if !child.Type().IsRegular() {
						return invalid
					}
				}
			}
			if err == io.EOF {
				return nil
			}
		}
	}
	return walk(".", 0)
}

var secretCapacitySensitivePattern = regexp.MustCompile(
	`(?i)-----BEGIN [A-Z ]*PRIVATE KEY|` +
		`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+|` +
		`authorization\s*[:=]\s*["']?(?:bearer|basic)\s+|` +
		`"(?:privateKey|accessToken|refreshToken|assertion|secretAccessKey)"\s*:|` +
		`serviceaccount-signing-key/v1|` +
		`GITSTORE_[A-Z0-9_]+\s*=|\[controller(?:\.|\])`,
)
var secretCapacityEscapedASCII = regexp.MustCompile(`\\u00[0-9a-fA-F]{2}`)

func secretCapacityScanBlock(data []byte, markers [][]byte) bool {
	data = secretCapacityEscapedASCII.ReplaceAllFunc(data, func(escape []byte) []byte {
		value, _ := strconv.ParseUint(string(escape[2:]), 16, 8)
		return []byte{byte(value)}
	})
	data = bytes.ReplaceAll(data, []byte(`\"`), []byte(`"`))
	if secretCapacitySensitivePattern.Match(data) {
		return false
	}
	for _, marker := range markers {
		if bytes.Contains(data, marker) {
			return false
		}
	}
	return true
}

func scanSecretCapacityArtifacts(directory string, secrets []string) error {
	invalid := errors.New("secret capacity: artifact scan failed (unsafe content, file or bounds)")
	if len(secrets) > 128 {
		return invalid
	}
	var markers [][]byte
	overlap := 4096
	for _, secret := range secrets {
		if secret == "" || len(secret) > 256*1024 {
			return invalid
		}
		encoded, err := json.Marshal(secret)
		if err != nil {
			return invalid
		}
		for _, value := range []string{
			secret, string(encoded[1 : len(encoded)-1]),
			base64.StdEncoding.EncodeToString([]byte(secret)),
			base64.RawStdEncoding.EncodeToString([]byte(secret)),
			base64.URLEncoding.EncodeToString([]byte(secret)),
			base64.RawURLEncoding.EncodeToString([]byte(secret)),
		} {
			markers = append(markers, []byte(value))
			// Escaped ASCII expands each byte to six characters.
			overlap = max(overlap, len(value)*6)
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return invalid
	}
	defer root.Close()
	var total int64
	count := 0
	err = walkSecretCapacityArtifacts(root, func(name string, directory bool) error {
		if !secretCapacityScanBlock([]byte(name), markers) {
			return invalid
		}
		if directory {
			return nil
		}
		count++
		if count > 512 {
			return invalid
		}
		file, err := openSecretCapacityArtifact(root, filepath.ToSlash(name))
		if err != nil {
			return invalid
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil || info.Size() > 128*1024*1024 || info.Size() > 512*1024*1024-total {
			return invalid
		}
		var size int64
		tail := make([]byte, 0, overlap)
		buffer := make([]byte, 64*1024)
		for {
			n, readErr := file.Read(buffer)
			total += int64(n)
			size += int64(n)
			if total > 512*1024*1024 || size > 128*1024*1024 {
				return invalid
			}
			block := append(tail, buffer[:n]...)
			if !secretCapacityScanBlock(block, markers) {
				return invalid
			}
			tail = append(tail[:0], block[max(0, len(block)-overlap):]...)
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return invalid
			}
		}
	})
	if err != nil || count == 0 {
		return invalid
	}
	return nil
}

func TestSecretCapacityObservationJSON(t *testing.T) {
	valid, err := json.Marshal(secretCapacityEvidenceFixture())
	require.NoError(t, err)
	decoded, err := decodeSecretCapacityObservations(valid)
	require.NoError(t, err)
	require.NoError(t, validateSecretCapacityEvidence(decoded))

	for name, data := range map[string][]byte{
		"empty":                    nil,
		"missing all fields":       []byte(`{}`),
		"null":                     []byte(`null`),
		"trailing document":        append(append([]byte{}, valid...), []byte(` {}`)...),
		"trailing garbage":         append(append([]byte{}, valid...), 'x'),
		"duplicate":                bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"dropped":1,"dropped":0`), 1),
		"escaped duplicate":        bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"drop\u0070ed":1,"dropped":0`), 1),
		"unknown field":            bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"dropped":0,"privateKey":"hidden"`), 1),
		"unsupported Git HA claim": bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"dropped":0,"gitHA":true`), 1),
		"missing zero count":       bytes.Replace(valid, []byte(`"dropped":0,`), nil, 1),
		"null zero count":          bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"dropped":null`), 1),
		"case folded field":        bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"Dropped":0`), 1),
		"missing nested count":     bytes.Replace(valid, []byte(`"samples":42000,`), nil, 1),
		"duplicate nested field":   bytes.Replace(valid, []byte(`"samples":42000`), []byte(`"samples":0,"samples":42000`), 1),
		"fractional count":         bytes.Replace(valid, []byte(`"dropped":0`), []byte(`"dropped":0.1`), 1),
		"oversized":                bytes.Repeat([]byte(" "), 2*1024*1024+1),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeSecretCapacityObservations(data)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "hidden")
		})
	}
}

func TestSecretCapacityArtifactScan(t *testing.T) {
	marker := "test-owned-opaque-material"
	for name, data := range map[string]string{
		"marker":        marker,
		"base64 marker": base64.StdEncoding.EncodeToString([]byte(marker)),
		"PEM":           "-----BEGIN PRIVATE KEY-----",
		"escaped PEM":   `\u002d\u002d\u002d\u002d\u002dBEGIN PRIVATE KEY`,
		"JWT":           "eyJhbGciOiJFUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0.c2lnbmF0dXJl",
		"authorization": "Authorization: Bearer opaque",
		"record":        `{"format":"serviceaccount-signing-key/v1","values":{}}`,
		"token body":    `{"data":{"issueServiceAccountToken":{"accessToken":"opaque"}}}`,
		"assertion":     `{"assertion":"opaque"}`,
		"environment":   "GITSTORE_CONTROLLER__SERVICEACCOUNT__UID=some-uid",
		"config":        "[controller.serviceaccount]\nuid = 'some-uid'",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "telemetry.log"), []byte(data), 0600))
			err := scanSecretCapacityArtifacts(root, []string{marker})
			require.Error(t, err)
			require.NotContains(t, err.Error(), marker)
			require.NotContains(t, err.Error(), data)
		})
	}
	t.Run("stream boundary", func(t *testing.T) {
		root := t.TempDir()
		data := strings.Repeat(" ", 64*1024-5) + marker
		require.NoError(t, os.WriteFile(filepath.Join(root, "telemetry.log"), []byte(data), 0600))
		require.Error(t, scanSecretCapacityArtifacts(root, []string{marker}))
	})
	t.Run("filename leakage", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(root, marker+".log"), []byte("safe"), 0600))
		require.Error(t, scanSecretCapacityArtifacts(root, []string{marker}))
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "outside")))
		require.Error(t, scanSecretCapacityArtifacts(root, nil))
	})
	t.Run("healthy sanitized evidence", func(t *testing.T) {
		root := t.TempDir()
		data, err := json.Marshal(secretCapacityEvidenceFixture())
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(root, "observations.json"), data, 0600))
		require.NoError(t, scanSecretCapacityArtifacts(root, nil))
	})
	t.Run("oversized artifact", func(t *testing.T) {
		root := t.TempDir()
		file, err := os.Create(filepath.Join(root, "oversized.log"))
		require.NoError(t, err)
		require.NoError(t, file.Truncate(128*1024*1024+1))
		require.NoError(t, file.Close())
		require.Error(t, scanSecretCapacityArtifacts(root, nil))
	})
	t.Run("too many files", func(t *testing.T) {
		root := t.TempDir()
		for i := range 513 {
			require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("%d.log", i)), []byte("safe"), 0600))
		}
		require.Error(t, scanSecretCapacityArtifacts(root, nil))
	})
	t.Run("too many directories", func(t *testing.T) {
		root := t.TempDir()
		for i := range 1025 {
			require.NoError(t, os.Mkdir(filepath.Join(root, fmt.Sprint(i)), 0700))
		}
		require.Error(t, scanSecretCapacityArtifacts(root, nil))
	})
	t.Run("excessive nesting", func(t *testing.T) {
		root := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(root, strings.Repeat("child/", 17)), 0700))
		require.Error(t, scanSecretCapacityArtifacts(root, nil))
	})
}

func TestSecretCapacityArtifactManifest(t *testing.T) {
	root := t.TempDir()
	data := []byte("safe measured telemetry\n")
	require.NoError(t, os.WriteFile(filepath.Join(root, "api-a.log"), data, 0600))
	digest := sha256.Sum256(data)
	artifact := secretCapacityArtifact{
		Path: "api-a.log", Kind: "log", ProcessID: "api-a",
		Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
	}
	require.NoError(t, verifySecretCapacityArtifact(root, artifact))
	for name, change := range map[string]func(*secretCapacityArtifact){
		"traversal":     func(a *secretCapacityArtifact) { a.Path = "../api-a.log" },
		"absolute":      func(a *secretCapacityArtifact) { a.Path = filepath.Join(root, a.Path) },
		"missing":       func(a *secretCapacityArtifact) { a.Path = "missing.log" },
		"wrong digest":  func(a *secretCapacityArtifact) { a.SHA256 = strings.Repeat("0", 64) },
		"wrong size":    func(a *secretCapacityArtifact) { a.Bytes++ },
		"response body": func(a *secretCapacityArtifact) { a.Kind = "token-response" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := artifact
			change(&bad)
			require.Error(t, verifySecretCapacityArtifact(root, bad))
		})
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "api-a.log"), filepath.Join(root, "link.log")))
	artifact.Path = "link.log"
	require.Error(t, verifySecretCapacityArtifact(root, artifact))
}

func TestSecretCapacityMeasuredSchedule(t *testing.T) {
	for name, change := range map[string]func(*secretCapacityEvidence){
		"negative outage":                      func(e *secretCapacityEvidence) { e.OutageAt = -time.Second },
		"dataset empty pages":                  func(e *secretCapacityEvidence) { e.Dataset.Pages = e.Dataset.Rows + 1 },
		"nominal rather than elapsed duration": func(e *secretCapacityEvidence) { e.Duration = time.Hour - time.Nanosecond },
		"short measured offering period":       func(e *secretCapacityEvidence) { e.OfferedDuration = time.Hour - time.Nanosecond },
		"offering schedule drift":              func(e *secretCapacityEvidence) { e.OfferedDuration = time.Hour + e.PushInterval + time.Nanosecond },
		"negative burst offer":                 func(e *secretCapacityEvidence) { e.Bursts[0].OfferDuration = -time.Second },
	} {
		t.Run(name, func(t *testing.T) {
			e := secretCapacityEvidenceFixture()
			change(&e)
			require.Error(t, validateSecretCapacityEvidence(e))
		})
	}
}

func TestSecretCapacityBundle(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string, *secretCapacityBundle)
	}{
		{"valid", func(*testing.T, string, *secretCapacityBundle) {}},
		{"wrong run", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.RunID = "another-run" }},
		{"wrong revision", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.GitRevision = strings.Repeat("b", 40) }},
		{"wrong schema", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.SchemaVersion++ }},
		{"missing scenario", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.Observations.Scenario = "" }},
		{"diagnostic", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.Observations.Mode = capacityModeDiagnostic }},
		{"missing fault", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.Observations.RestartConfirmed = false }},
		{"two Git instances", func(_ *testing.T, _ string, b *secretCapacityBundle) {
			git := b.Observations.Processes[4]
			git.ID = "git-two"
			b.Observations.Processes = append(b.Observations.Processes, git)
		}},
		{"no artifacts", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.Artifacts = nil }},
		{"missing process telemetry", func(t *testing.T, root string, b *secretCapacityBundle) {
			require.NoError(t, os.Remove(filepath.Join(root, b.Artifacts[0].Path)))
			b.Artifacts = b.Artifacts[1:]
		}},
		{"missing replacement telemetry", func(t *testing.T, root string, b *secretCapacityBundle) {
			last := len(b.Artifacts) - 1
			require.NoError(t, os.Remove(filepath.Join(root, b.Artifacts[last].Path)))
			b.Artifacts = b.Artifacts[:last]
		}},
		{"unknown process", func(_ *testing.T, _ string, b *secretCapacityBundle) { b.Artifacts[0].ProcessID = "invented" }},
		{"duplicate artifact", func(_ *testing.T, _ string, b *secretCapacityBundle) {
			b.Artifacts = append(b.Artifacts, b.Artifacts[0])
		}},
		{"unlisted artifact", func(t *testing.T, root string, _ *secretCapacityBundle) {
			require.NoError(t, os.WriteFile(filepath.Join(root, "unlisted.log"), []byte("safe"), 0600))
		}},
		{"modified telemetry", func(t *testing.T, root string, b *secretCapacityBundle) {
			require.NoError(t, os.WriteFile(filepath.Join(root, b.Artifacts[0].Path), []byte("changed"), 0600))
		}},
		{"credential in hashed telemetry", func(t *testing.T, root string, b *secretCapacityBundle) {
			data := []byte(`{"accessToken":"opaque"}`)
			require.NoError(t, os.WriteFile(filepath.Join(root, b.Artifacts[0].Path), data, 0600))
			digest := sha256.Sum256(data)
			b.Artifacts[0].Bytes = int64(len(data))
			b.Artifacts[0].SHA256 = hex.EncodeToString(digest[:])
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bundle := secretCapacityBundleFixture(t, root)
			test.change(t, root, &bundle)
			data, err := json.Marshal(bundle)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(root, "secret-evidence.json"), data, 0600))
			_, err = loadSecretCapacityBundle(root, "test-run", strings.Repeat("a", 40), nil)
			if test.name == "valid" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func secretCapacityBundleFixture(t *testing.T, root string) secretCapacityBundle {
	t.Helper()
	bundle := secretCapacityBundle{
		SchemaVersion: 1, RunID: "test-run", GitRevision: strings.Repeat("a", 40),
		Observations: secretCapacityEvidenceFixture(),
	}
	var ids []string
	for _, process := range bundle.Observations.Processes {
		ids = append(ids, process.ID)
	}
	ids = append(ids, bundle.Observations.ReplacementID)
	ids = append(ids, bundle.Observations.APIReplacementID)
	for _, id := range ids {
		for _, kind := range []string{"log", "metrics"} {
			name := fmt.Sprintf("%s.%s", id, kind)
			data := []byte("sanitized synthetic fixture\n")
			require.NoError(t, os.WriteFile(filepath.Join(root, name), data, 0600))
			digest := sha256.Sum256(data)
			bundle.Artifacts = append(bundle.Artifacts, secretCapacityArtifact{
				Path: name, ProcessID: id, Kind: kind, Bytes: int64(len(data)), SHA256: hex.EncodeToString(digest[:]),
			})
		}
	}
	return bundle
}

func TestSecretCapacityFilePool(t *testing.T) {
	seen := make(map[int]bool)
	for worker := range 32 {
		files := secretCapacityWorkerFiles(worker)
		require.NotEmpty(t, files)
		require.LessOrEqual(t, len(files), 10)
		for _, file := range files {
			require.False(t, seen[file])
			seen[file] = true
		}
	}
	require.Len(t, seen, 100)
}

func TestSecretCapacityPushScheduling(t *testing.T) {
	cfg := secretCapacityLoadConfig{
		duration: time.Second, interval: 50 * time.Millisecond,
		burstInterval: 500 * time.Millisecond, burstSize: 10, burstWindow: 200 * time.Millisecond,
		workers: 32, queueLimit: 256, drainTimeout: time.Second,
	}
	var calls atomic.Int64
	var active, peak atomic.Int64
	result, err := runSecretCapacityPushes(t.Context(), cfg, func(ctx context.Context, worker int, batch secretCapacityPushBatch) secretCapacityPushResult {
		calls.Add(1)
		now := active.Add(1)
		for old := peak.Load(); now > old && !peak.CompareAndSwap(old, now); old = peak.Load() {
		}
		defer active.Add(-1)
		select {
		case <-ctx.Done():
			return secretCapacityPushResult{err: ctx.Err()}
		case <-time.After(time.Millisecond):
		}
		return secretCapacityPushResult{gitAttempted: true, acknowledged: true, files: len(secretCapacityWorkerFiles(worker)), bytes: 1024}
	})
	require.NoError(t, err)
	require.EqualValues(t, 20, result.sustainedOffered)
	require.EqualValues(t, 40, calls.Load())
	require.EqualValues(t, 40, result.acknowledged)
	require.EqualValues(t, 40, result.gitPushes)
	require.Zero(t, result.dropped)
	require.Zero(t, result.failed)
	require.Len(t, result.bursts, 2)
	require.Len(t, result.pushLatencies, 40)
	require.GreaterOrEqual(t, result.offeredDuration, cfg.duration)
	require.LessOrEqual(t, peak.Load(), int64(32))
	for _, burst := range result.bursts {
		require.EqualValues(t, 10, burst.Offered)
		require.Greater(t, burst.OfferDuration, time.Duration(0))
		require.Greater(t, burst.Drain, time.Duration(0))
	}
}

func TestSecretCapacityPushQueueDoesNotHideDrops(t *testing.T) {
	cfg := secretCapacityLoadConfig{
		duration: 20 * time.Millisecond, interval: time.Millisecond,
		burstInterval: 20 * time.Millisecond, burstSize: 100, burstWindow: time.Millisecond,
		workers: 1, queueLimit: 1, drainTimeout: time.Second,
	}
	result, err := runSecretCapacityPushes(t.Context(), cfg, func(ctx context.Context, _ int, _ secretCapacityPushBatch) secretCapacityPushResult {
		select {
		case <-time.After(40 * time.Millisecond):
			return secretCapacityPushResult{gitAttempted: true, acknowledged: true}
		case <-ctx.Done():
			return secretCapacityPushResult{err: ctx.Err()}
		}
	})
	require.Error(t, err)
	require.Greater(t, result.dropped, int64(0))
	require.LessOrEqual(t, result.queuePeak, 1)
	require.Equal(t, result.sustainedOffered+result.bursts[0].Offered, result.completed+result.dropped)
}

func TestSecretCapacityPushCancellationDrainsWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var entered sync.Once
	var active atomic.Int64
	cfg := secretCapacityProductionLoadConfig()
	result, err := runSecretCapacityPushes(ctx, cfg, func(ctx context.Context, _ int, _ secretCapacityPushBatch) secretCapacityPushResult {
		active.Add(1)
		defer active.Add(-1)
		entered.Do(cancel)
		<-ctx.Done()
		return secretCapacityPushResult{err: ctx.Err()}
	})
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, active.Load(), "all workers must exit before returning")
	require.Equal(t, result.completed+result.dropped, result.sustainedOffered+result.bursts[0].Offered)
}

func TestSecretCapacityGitPushesRealFileCommits(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	_, err := secretCapacityGit(t.Context(), root, "", "init", "--bare", "--initial-branch=main", remote)
	require.NoError(t, err)
	worker, err := newSecretCapacityGitWorker(t.Context(), filepath.Join(root, "work"), remote, "capacity-test", "fixture", "", 0)
	require.NoError(t, err)
	for sequence := int64(1); sequence <= 2; sequence++ {
		result := worker.push(t.Context(), secretCapacityPushBatch{sequence: sequence})
		require.NoError(t, result.err)
		require.True(t, result.gitAttempted)
		require.True(t, result.acknowledged)
		require.Len(t, result.referenceLatencies, len(secretCapacityWorkerFiles(0)))
		for _, file := range secretCapacityWorkerFiles(0) {
			data, err := secretCapacityGit(t.Context(), root, "", "--git-dir="+remote, "show",
				"main:files/"+secretCapacityFileName("fixture", file)+".md")
			require.NoError(t, err)
			require.Contains(t, string(data), "kind: CredentialsRef")
			require.Contains(t, string(data), fmt.Sprintf("secret-capacity.gitstore.dev/sequence: \"%d\"", sequence))
		}
	}
	config, err := os.ReadFile(filepath.Join(root, "work", ".git", "config"))
	require.NoError(t, err)
	require.NotContains(t, string(config), "Authorization")
}

func TestSecretCapacityGitFailureNeverEchoesOutput(t *testing.T) {
	root := t.TempDir()
	_, err := secretCapacityGit(t.Context(), root, "", "not-a-command-secret-marker")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-marker")
	require.False(t, errors.Is(err, context.Canceled))
}

func TestSecretCapacityGitOutputIsBounded(t *testing.T) {
	var output secretCapacityGitOutput
	n, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 256*1024)))
	require.NoError(t, err)
	require.EqualValues(t, 256*1024, n)
	require.True(t, output.overflow)
	require.Equal(t, 128*1024, output.buffer.Len())
}

func TestSecretCapacityProjectionReadsNullableGraphQLFields(t *testing.T) {
	name := secretCapacityFileName("fixture", 0)
	var projection secretCapacityFileProjection
	data := []byte(fmt.Sprintf(`{
		"id":"file-id","metadata":{"name":%q,"namespace":"ns","annotations":{"secret-capacity.gitstore.dev/sequence":"1"}},
		"spec":{"contentType":"image/jpeg","type":"gitstore.dev/media","source":{
			"type":"s3","uri":%q,"credentialsRef":{"kind":"CredentialsRef","type":"aws-access-key/v1",
				"secretRef":{"kind":"SecretRef","name":"capacity-media","key":null,"namespace":null}}}}}`, name, "s3://capacity-fixture/"+name+".jpg"))
	require.NoError(t, json.Unmarshal(data, &projection))
	require.NoError(t, validateSecretCapacityFile(projection, "ns", name, 1))
	require.Error(t, validateSecretCapacityFile(projection, "another-ns", name, 1))
	require.Error(t, validateSecretCapacityFile(projection, "ns", name, 2))
	projection.Spec.Source.CredentialsRef.Kind = "SecretRef"
	require.Error(t, validateSecretCapacityFile(projection, "ns", name, 1))
}

func TestSecretCapacityGraphQLErrorsDoNotExposeBodies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"errors":[{"message":"private-response-marker"}]}`)
	}))
	defer server.Close()
	var data struct{}
	err := secretCapacityGraphQL(t.Context(), server.Client(), server.URL, "private-token", "query { node(id:\"id\") { id } }", nil, &data)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "private-response-marker")
	require.NotContains(t, err.Error(), "private-token")
}

func TestSecretCapacityFailedComponentPersistsWithoutPassingGate(t *testing.T) {
	root := t.TempDir()
	result := secretCapacityFileLoadResult{
		err:          errors.New("private-error-marker"),
		measurements: secretCapacityPushMeasurements{dropped: 2, offeredDuration: time.Second},
	}
	require.NoError(t, writeSecretCapacityFileObservations(root, "test-run", capacityModeDiagnostic, time.Minute, secretCapacityRuntimeProof{}, result))
	data, err := os.ReadFile(filepath.Join(root, "secret", "file-workload.json"))
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-error-marker")
	require.NotContains(t, string(data), `"passed"`)
	var summary struct {
		WorkloadCompleted bool          `json:"workloadCompleted"`
		Dropped           int64         `json:"dropped"`
		NominalDuration   time.Duration `json:"nominalDuration"`
		OfferedDuration   time.Duration `json:"offeredDuration"`
	}
	require.NoError(t, json.Unmarshal(data, &summary))
	require.False(t, summary.WorkloadCompleted)
	require.EqualValues(t, 2, summary.Dropped)
	require.Equal(t, time.Minute, summary.NominalDuration)
	require.Equal(t, time.Second, summary.OfferedDuration)
	_, err = loadSecretCapacityBundle(filepath.Join(root, "secret"), "test-run", strings.Repeat("a", 40), nil)
	require.Error(t, err, "component observations cannot replace the scenario bundle")
}

func TestSecretCapacityVerifiesEveryFileThroughBothAPIs(t *testing.T) {
	worker := &secretCapacityGitWorker{namespace: "ns", runID: "fixture", files: secretCapacityWorkerFiles(0)}
	ids := make(map[string]string)
	var nodes []map[string]any
	for _, index := range worker.files {
		name := secretCapacityFileName(worker.runID, index)
		id := fmt.Sprintf("file-%d", index)
		ids[name] = id
		nodes = append(nodes, map[string]any{
			"id": id,
			"metadata": map[string]any{"name": name, "namespace": "ns",
				"annotations": map[string]string{"secret-capacity.gitstore.dev/sequence": "1"}},
			"spec": map[string]any{"contentType": "image/jpeg", "type": "gitstore.dev/media",
				"source": map[string]any{"type": "s3", "uri": "s3://capacity-fixture/" + name + ".jpg",
					"credentialsRef": map[string]any{"kind": "CredentialsRef", "type": "aws-access-key/v1",
						"secretRef": map[string]any{"kind": "SecretRef", "name": "capacity-media", "key": nil, "namespace": nil}}}},
		})
	}
	var readsA, readsB atomic.Int64
	var missingPeer atomic.Bool
	handler := func(counter *atomic.Int64, peer bool) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			if r.Header.Get("Authorization") != "Bearer test-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if peer && missingPeer.Load() {
				_, _ = io.WriteString(w, `{"data":{"nodes":[]}}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"nodes": nodes}})
		}
	}
	a, b := httptest.NewServer(handler(&readsA, false)), httptest.NewServer(handler(&readsB, true))
	defer a.Close()
	defer b.Close()
	endpoints := []string{a.URL, b.URL}
	batch := secretCapacityPushBatch{sequence: 1}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.NoError(t, secretCapacityVerifyFileBatch(ctx, a.Client(), endpoints, "test-token", worker, ids, batch))
	require.EqualValues(t, 1, readsA.Load())
	require.EqualValues(t, 1, readsB.Load())
	missingPeer.Store(true)
	short, stop := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer stop()
	require.Error(t, secretCapacityVerifyFileBatch(short, a.Client(), endpoints, "test-token", worker, ids, batch),
		"an acknowledged batch missing from one replica must not pass")
}

func assembleSecretCapacityEvidence(directory, runID string, mode capacityMode) (secretCapacityEvidence, error) {
	var evidence secretCapacityEvidence
	root, err := os.OpenRoot(directory)
	if err != nil {
		return evidence, errors.New("secret capacity: cannot open completed evidence")
	}
	defer root.Close()
	read := func(name string, destination any) error {
		file, err := openSecretCapacityArtifact(root, "secret/"+name)
		if err != nil {
			return err
		}
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, secretCapacityJSONLimit+1))
		if err != nil || decodeSecretCapacityJSON(data, destination) != nil {
			return errors.New("secret capacity: incomplete component observations")
		}
		return nil
	}
	var workload secretCapacityFileObservations
	var dataset secretCapacityDatasetObservations
	var resources secretCapacityResourceObservations
	var faults secretCapacityFaultObservations
	for name, destination := range map[string]any{
		"file-workload.json": &workload, "dataset.json": &dataset, "resources.json": &resources, "faults.json": &faults,
	} {
		if err := read(name, destination); err != nil {
			return evidence, err
		}
	}
	if workload.SchemaVersion != 1 || dataset.SchemaVersion != 1 || resources.SchemaVersion != 1 || faults.SchemaVersion != 1 ||
		workload.Component != "secret-file-workload/v1" || dataset.Component != "secret-dataset/v1" ||
		resources.Component != "secret-resources/v1" || faults.Component != "secret-faults/v1" ||
		workload.RunID != runID || dataset.RunID != runID || resources.RunID != runID || faults.RunID != runID ||
		workload.Mode != mode || dataset.Mode != mode || resources.Mode != mode ||
		!workload.WorkloadCompleted || !faults.Completed || !resources.RepositoryLifecyclePassed ||
		len(resources.Processes) != 5 || len(resources.Before) != 5 || len(resources.LoadEnd) != 5 || len(resources.Stabilized) != 5 ||
		len(resources.ControllersBefore) != 2 || len(resources.ControllersAfter) != 2 ||
		len(faults.Recovery) != 2 || len(faults.OverlapRenewals) != 2 || len(dataset.Replicas) != 2 ||
		!dataset.VerifiedAt.Before(resources.Before[0].ObservedAt) ||
		workload.NamespaceDenied != 1 || workload.BindingDenied != 1 || workload.UnsupportedDenied != 1 ||
		workload.ProviderCalls != workload.AcceptedCalls || faults.AuthorizedIssuance < 4 ||
		workload.Completed != workload.GitPushes || workload.CrossReplicaChecks < 2*workload.Acknowledged {
		return evidence, errors.New("secret capacity: components do not describe one complete measured run")
	}
	for i, replica := range dataset.Replicas {
		if replica.Role != []string{"api_a", "api_b"}[i] || replica.Rows != dataset.Proof.Rows || replica.SetDigest != dataset.SetDigest {
			return evidence, errors.New("secret capacity: replica dataset proof disagrees")
		}
	}
	for i, process := range resources.Processes {
		if process.ID != resources.Before[i].ID || process.Role != resources.Before[i].Role ||
			resources.LoadEnd[i].ID != resources.Stabilized[i].ID {
			return evidence, errors.New("secret capacity: resource identities disagree")
		}
	}
	evidence = secretCapacityEvidence{
		Scenario: "repository-lifecycle-secrets/v1", Mode: mode, RepositoryLifecyclePassed: resources.RepositoryLifecyclePassed,
		Dataset: dataset.Proof, Duration: workload.NominalDuration, OfferedDuration: workload.OfferedDuration,
		Baseline: resources.Baseline, PostLoad: resources.PostLoad, FilePool: resources.FilePool, Workers: resources.Workers,
		QueueLimit: secretCapacityProductionLoadConfig().queueLimit, QueuePeak: workload.QueuePeak, PushWorkersPeak: workload.WorkersPeak,
		MaxFilesPerPush: workload.MaxFiles, MaxBytesPerPush: workload.MaxBytes, PushInterval: secretCapacityProductionLoadConfig().interval,
		SustainedOffered: workload.SustainedOffered, MissedSchedules: workload.MissedSchedules, Bursts: workload.Bursts,
		GitPushes: workload.GitPushes, Acknowledged: workload.Acknowledged, Failed: workload.Failed, Dropped: workload.Dropped,
		TypedFiles: workload.TypedFiles, ProjectionLoss: workload.ProjectionLoss, CrossNamespaceSuccess: 1 - workload.NamespaceDenied,
		Reference: workload.Reference, Resolution: workload.Resolution, Push: workload.Push, LocalHealthyFailures: workload.LocalHealthyFailures,
		ResolverCallers: workload.RuntimeCallers, ProviderPeak: workload.ProviderPeak,
		ProviderQueued:  int64(workload.RuntimeCallers) - workload.AcceptedCalls - workload.SaturationDenied,
		ProviderRetries: workload.ProviderCalls - workload.AcceptedCalls, SaturationDenied: workload.SaturationDenied, DeadlineDenied: workload.DeadlineDenied,
		Processes: resources.Processes, ScannedProcesses: int64(len(resources.Processes)),
		OutageAt: faults.OutageAt, OutageDuration: 90 * time.Second, OutageElapsed: faults.OutageElapsed,
		ExpiredUnready: faults.ExpiredUnready, PeerFailures: faults.PeerFailures, ClassifiedOutageFailures: faults.ClassifiedFailures,
		RotationAt: faults.RotationAt, RetiredKeyDenied: faults.RetiredKeyDenied, WrongSubjectDenied: faults.WrongSubjectDenied,
		RestartAt: faults.RestartAt, RestartConfirmed: faults.RestartConfirmed, RestartTargetID: faults.RestartTargetID,
		ReplacementID: faults.Replacement.ID, APIReplacementID: resources.LoadEnd[1].ID,
		ReplacementFreshTokens: faults.Replacement.FreshTokens, ReplacementReconciliations: faults.Replacement.Reconciliations,
		ReplacementRecovery: faults.ReplacementRecovery, CrossReplicaProjectionChecks: workload.CrossReplicaChecks,
	}
	for i, before := range resources.ControllersBefore {
		after := resources.ControllersAfter[i]
		fresh, reconciled := after.FreshTokens-before.FreshTokens, after.Reconciliations-before.Reconciliations
		retry, peak := after.MaxRetry, after.ExchangePeak
		if i == 0 {
			if faults.RestartBefore.ID != before.ID || after.ID != faults.Replacement.ID {
				return secretCapacityEvidence{}, errors.New("secret capacity: controller replacement identity disagrees")
			}
			fresh = faults.RestartBefore.FreshTokens - before.FreshTokens + after.FreshTokens
			reconciled = faults.RestartBefore.Reconciliations - before.Reconciliations + after.Reconciliations
			retry, peak = max(retry, faults.RestartBefore.MaxRetry), max(peak, faults.RestartBefore.ExchangePeak)
		} else if before.ID != after.ID {
			return secretCapacityEvidence{}, errors.New("secret capacity: unaffected controller changed identity")
		}
		if before.ID != resources.Before[i+2].ID || after.ID != resources.LoadEnd[i+2].ID {
			return secretCapacityEvidence{}, errors.New("secret capacity: authentication and resource identities disagree")
		}
		evidence.Controllers = append(evidence.Controllers, secretCapacityControllerProof{
			ID: before.ID, TokenTTL: time.Minute, MaxRetry: retry, ExchangePeak: int(peak), Recovery: faults.Recovery[i],
			FreshTokens: fresh, Reconciliations: reconciled, OverlapRenewals: faults.OverlapRenewals[i],
		})
	}
	return evidence, validateSecretCapacityEvidenceMode(evidence, false)
}

func finalizeSecretCapacityBundle(directory, runID, revision string, mode capacityMode, secrets []string) error {
	evidence, err := assembleSecretCapacityEvidence(directory, runID, mode)
	if err != nil {
		return err
	}
	if err := scanSecretCapacityArtifacts(directory, secrets); err != nil {
		return err
	}
	bundle := secretCapacityBundle{SchemaVersion: 1, RunID: runID, GitRevision: revision, Observations: evidence}
	ids := []string{evidence.ReplacementID, evidence.APIReplacementID}
	for _, process := range evidence.Processes {
		ids = append(ids, process.ID)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	err = walkSecretCapacityArtifacts(root, func(name string, directory bool) error {
		if directory || name == "secret-evidence.json" {
			return nil
		}
		file, err := openSecretCapacityArtifact(root, name)
		if err != nil {
			return err
		}
		defer file.Close()
		digest := sha256.New()
		size, err := io.Copy(digest, io.LimitReader(file, 128*1024*1024+1))
		if err != nil || size > 128*1024*1024 {
			return errors.New("secret capacity: artifact exceeds finalization bounds")
		}
		kind := "summary"
		switch filepath.Ext(name) {
		case ".log":
			kind = "log"
		case ".metrics":
			kind = "metrics"
		}
		processID := "driver:" + runID
		for _, id := range ids {
			base := filepath.Base(name)
			if strings.HasPrefix(base, id+".") || strings.HasPrefix(base, id+"-") {
				processID = id
				break
			}
		}
		bundle.Artifacts = append(bundle.Artifacts, secretCapacityArtifact{Path: name, Kind: kind, ProcessID: processID, Bytes: size, SHA256: hex.EncodeToString(digest.Sum(nil))})
		return nil
	})
	if err != nil {
		return err
	}
	if err := writeSecretCapacityPrivateJSON(filepath.Join(directory, "secret-evidence.json"), bundle); err != nil {
		return err
	}
	_, err = loadSecretCapacityBundleMode(directory, runID, revision, secrets, false)
	return err
}

func TestSecretCapacityFinalizeEvidence(t *testing.T) {
	if os.Getenv("REPOSITORY_CAPACITY_SECRET_FINALIZE") != "1" {
		t.Skip("finalization belongs to the existing capacity dispatcher")
	}
	require.True(t, secretCapacityRequested())
	root, runID, err := secretCapacityFixturePath()
	require.NoError(t, err)
	fixture, err := readSecretCapacityOwnedFixture(root, runID)
	require.NoError(t, err)
	require.Len(t, fixture.RuntimeMarkers, 2)
	secrets := append([]string{}, fixture.RuntimeMarkers...)
	for _, key := range fixture.Keys {
		secrets = append(secrets, key.Private)
	}
	token, err := os.ReadFile(os.Getenv("REPOSITORY_TOKEN_FILE"))
	require.NoError(t, err)
	secrets = append(secrets, strings.TrimSpace(string(token)))
	clear(token)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	revision, err := secretCapacityOwnedCommand(ctx, "git", "rev-parse", "HEAD")
	require.NoError(t, err)
	mode := capacityMode(os.Getenv("MODE"))
	require.NoError(t, validateSecretCapacityRunMetadata(os.Getenv("CAPACITY_EVIDENCE_DIR"), runID, strings.TrimSpace(string(revision)), mode))
	if mode != capacityModeDiagnostic {
		dirty, err := secretCapacityOwnedCommand(ctx, "git", "status", "--porcelain")
		require.NoError(t, err)
		require.Empty(t, dirty, "non-diagnostic evidence requires a clean verifier at finalization")
	}
	require.NoError(t, finalizeSecretCapacityBundle(os.Getenv("CAPACITY_EVIDENCE_DIR"), runID, strings.TrimSpace(string(revision)), mode, secrets))
}

func validateSecretCapacityRunMetadata(directory, runID, revision string, mode capacityMode) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := openSecretCapacityArtifact(root, "metadata.json")
	if err != nil {
		return err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, secretCapacityJSONLimit+1))
	if err != nil || len(body) > secretCapacityJSONLimit {
		return errors.New("secret capacity: cannot read run provenance")
	}
	// The shared dispatcher owns additional metadata for other capacity profiles.
	var metadata struct {
		SchemaVersion        int          `json:"schemaVersion"`
		Target               string       `json:"target"`
		Profile              string       `json:"profile"`
		RunID                string       `json:"runId"`
		Revision             string       `json:"gitRevision"`
		Mode                 capacityMode `json:"mode"`
		CompletedAt          time.Time    `json:"completedAt"`
		SourceStateUnchanged bool         `json:"sourceStateUnchanged"`
		WorktreeDirty        bool         `json:"worktreeDirty"`
		Passed               bool         `json:"passed"`
		ExitCode             *int         `json:"exitCode"`
	}
	if json.Unmarshal(body, &metadata) != nil || metadata.SchemaVersion != 1 ||
		metadata.Target != "repository" || metadata.Profile != "lifecycle" ||
		metadata.RunID != runID || metadata.Revision != revision || metadata.Mode != mode ||
		metadata.CompletedAt.IsZero() || !metadata.SourceStateUnchanged || metadata.ExitCode == nil || *metadata.ExitCode != 0 ||
		(mode != capacityModeDiagnostic && (metadata.WorktreeDirty || !metadata.Passed)) ||
		(mode == capacityModeDiagnostic && metadata.Passed) {
		return errors.New("secret capacity: completed dispatcher provenance disagrees with finalization")
	}
	return nil
}

func TestSecretCapacityRequiresCompletedDispatcherProvenance(t *testing.T) {
	for _, fault := range []string{"", "revision", "failed", "unfinished", "changed", "diagnostic pass", "missing status"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			metadata := map[string]any{"schemaVersion": 1, "target": "repository", "profile": "lifecycle", "runId": "run",
				"gitRevision": "revision", "mode": "diagnostic", "completedAt": time.Now(), "sourceStateUnchanged": true, "exitCode": 0, "passed": false}
			switch fault {
			case "revision":
				metadata["gitRevision"] = "another"
			case "failed":
				metadata["exitCode"] = 1
			case "unfinished":
				delete(metadata, "completedAt")
			case "changed":
				metadata["sourceStateUnchanged"] = false
			case "diagnostic pass":
				metadata["passed"] = true
			case "missing status":
				delete(metadata, "exitCode")
			}
			require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "metadata.json"), metadata))
			err := validateSecretCapacityRunMetadata(root, "run", "revision", capacityModeDiagnostic)
			if fault == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func secretCapacityComponentFixture(t *testing.T, root string) (secretCapacityFileObservations, secretCapacityDatasetObservations, secretCapacityResourceObservations, secretCapacityFaultObservations) {
	t.Helper()
	e := secretCapacityEvidenceFixture()
	const runID = "test-run"
	w := secretCapacityFileObservations{
		SchemaVersion: 1, Component: "secret-file-workload/v1", RunID: runID, Mode: e.Mode, WorkloadCompleted: true,
		NominalDuration: e.Duration, OfferedDuration: e.OfferedDuration, SustainedOffered: e.SustainedOffered,
		Completed: e.GitPushes, GitPushes: e.GitPushes, Acknowledged: e.Acknowledged, Failed: e.Failed, Dropped: e.Dropped,
		TypedFiles: e.TypedFiles, QueuePeak: e.QueuePeak, WorkersPeak: e.PushWorkersPeak, MaxFiles: e.MaxFilesPerPush, MaxBytes: e.MaxBytesPerPush,
		CrossReplicaChecks: 2 * e.Acknowledged, Bursts: e.Bursts, Reference: e.Reference, Resolution: e.Resolution, Push: e.Push,
		RuntimeCallers: 32, ProviderPeak: 16, ProviderCalls: 16, AcceptedCalls: 16, SaturationDenied: 16, DeadlineDenied: 1,
		NamespaceDenied: 1, BindingDenied: 1, UnsupportedDenied: 1,
	}
	start := time.Now().Add(-2 * time.Hour)
	d := secretCapacityDatasetObservations{SchemaVersion: 1, Component: "secret-dataset/v1", RunID: runID, Mode: e.Mode,
		Proof: e.Dataset, ManifestSHA256: strings.Repeat("a", 64), ManifestBytes: 100, SetDigest: strings.Repeat("b", 64), VerifiedAt: start.Add(-time.Minute), Elapsed: time.Second}
	for _, role := range []string{"api_a", "api_b"} {
		d.Replicas = append(d.Replicas, secretCapacityDatasetReplica{Role: role, Rows: e.Dataset.Rows, Pages: e.Dataset.Pages, MaxPageRows: e.Dataset.MaxPageRows, SetDigest: d.SetDigest})
	}
	r := secretCapacityResourceObservations{SchemaVersion: 1, Component: "secret-resources/v1", RunID: runID, Mode: e.Mode,
		Processes: e.Processes, Baseline: e.Baseline, PostLoad: e.PostLoad, FilePool: 100, Workers: 32, RepositoryLifecyclePassed: true}
	for i, p := range e.Processes {
		first := secretCapacityResourceSnapshot{Role: p.Role, ID: p.ID, CPUSeconds: 1, CPUCapacity: 1, RSS: 100, Goroutines: 100, ObservedAt: start}
		last := first
		last.ObservedAt = start.Add(time.Hour)
		if i == 1 {
			last.ID = e.APIReplacementID
		}
		if i == 2 {
			last.ID = e.ReplacementID
		}
		r.Before, r.LoadEnd, r.Stabilized = append(r.Before, first), append(r.LoadEnd, last), append(r.Stabilized, last)
	}
	for i, p := range e.Controllers {
		before := secretCapacityControllerSample{ID: p.ID, ObservedAt: start, FreshTokens: 1, Reconciliations: 1}
		after := before
		after.FreshTokens, after.Reconciliations, after.ExchangePeak, after.MaxRetry = 121, 121, 1, 30*time.Second
		after.ObservedAt = start.Add(time.Hour)
		if i == 0 {
			after.ID = e.ReplacementID
		}
		r.ControllersBefore, r.ControllersAfter = append(r.ControllersBefore, before), append(r.ControllersAfter, after)
	}
	f := secretCapacityFaultObservations{SchemaVersion: 1, Component: "secret-faults/v1", RunID: runID, Completed: true,
		OutageAt: e.OutageAt, OutageElapsed: e.OutageElapsed, ExpiredUnready: true, ClassifiedFailures: 1, Recovery: []time.Duration{time.Minute, time.Minute},
		RotationAt: e.RotationAt, OverlapRenewals: []int64{1, 1}, RetiredKeyDenied: 2, WrongSubjectDenied: 2, AuthorizedIssuance: 4,
		RestartAt: e.RestartAt, RestartTargetID: e.RestartTargetID, RestartConfirmed: true, ReplacementRecovery: time.Minute,
		RestartBefore: secretCapacityControllerSample{ID: e.RestartTargetID, FreshTokens: 90, Reconciliations: 90, ExchangePeak: 1},
		Replacement:   secretCapacityControllerSample{ID: e.ReplacementID, FreshTokens: 1, Reconciliations: 1}}
	for name, value := range map[string]any{"file-workload.json": w, "dataset.json": d, "resources.json": r, "faults.json": f} {
		require.NoError(t, writeSecretCapacityComponent(root, name, value))
	}
	ids := []string{e.ReplacementID, e.APIReplacementID}
	for _, p := range e.Processes {
		ids = append(ids, p.ID)
	}
	for _, id := range ids {
		for _, suffix := range []string{".log", "-before.metrics"} {
			require.NoError(t, os.WriteFile(filepath.Join(root, "secret", id+suffix), []byte("synthetic unit fixture\n"), 0600))
		}
	}
	return w, d, r, f
}

func TestSecretCapacityFinalizesMeasuredComponentShape(t *testing.T) {
	root := t.TempDir()
	secretCapacityComponentFixture(t, root)
	revision := strings.Repeat("c", 40)
	require.NoError(t, finalizeSecretCapacityBundle(root, "test-run", revision, capacityModeProduction, nil))
	bundle, err := loadSecretCapacityBundle(root, "test-run", revision, nil)
	require.NoError(t, err)
	require.Equal(t, "api-replacement", bundle.Observations.APIReplacementID)
	require.Greater(t, len(bundle.Artifacts), 14)
	require.NoError(t, os.WriteFile(filepath.Join(root, "leaked.log"), []byte("opaque-private-marker"), 0600))
	require.Error(t, finalizeSecretCapacityBundle(root, "test-run", revision, capacityModeProduction, []string{"opaque-private-marker"}))
}

func TestSecretCapacityRejectsMixedOrIncompleteComponents(t *testing.T) {
	for _, fault := range []string{"mode", "run", "outage", "dataset replica", "not finished", "denial"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			w, d, r, f := secretCapacityComponentFixture(t, root)
			switch fault {
			case "mode":
				r.Mode = capacityModeDiagnostic
			case "run":
				d.RunID = "another-run"
			case "outage":
				f.OutageElapsed = 89 * time.Second
			case "dataset replica":
				d.Replicas[1].Role = "api_a"
			case "not finished":
				f.Completed = false
			case "denial":
				w.NamespaceDenied = 0
			}
			for name, value := range map[string]any{"file-workload.json": w, "dataset.json": d, "resources.json": r, "faults.json": f} {
				require.NoError(t, writeSecretCapacityComponent(root, name, value))
			}
			_, err := assembleSecretCapacityEvidence(root, "test-run", capacityModeProduction)
			require.Error(t, err)
		})
	}
}

func TestSecretCapacityDatasetAcceptsAdmissionRevision(t *testing.T) {
	row := secretCapacityProductAcknowledgment{Namespace: "capacity", Name: "product", Title: "Product", Revision: "main@sha1:" + strings.Repeat("a", 40)}
	require.True(t, validSecretCapacityProduct(row, "capacity"))
	for _, revision := range []string{"main@sha1:abc", "@sha1:" + strings.Repeat("a", 40), "bad\nbranch@sha1:" + strings.Repeat("a", 40)} {
		row.Revision = revision
		require.False(t, validSecretCapacityProduct(row, "capacity"))
	}
}

func TestSecretCapacityBoundedLogWriter(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "log")
	require.NoError(t, err)
	defer file.Close()
	writer := secretCapacityLogWriter{file: file, written: 128*1024*1024 - 2}
	n, err := writer.Write([]byte("abcd"))
	require.NoError(t, err)
	require.Equal(t, 4, n, "overflow must drain the pipe rather than deadlock the process")
	require.True(t, writer.overflow)
	n, err = writer.Write([]byte("discard"))
	require.NoError(t, err)
	require.Equal(t, 7, n)
	info, err := file.Stat()
	require.NoError(t, err)
	require.EqualValues(t, 2, info.Size())
}

func TestSecretCapacityProcessMetricParsing(t *testing.T) {
	body := "process_cpu_seconds_total 12\nprocess_resident_memory_bytes 1024\nprocess_start_time_seconds 1\ngo_sched_gomaxprocs_threads 2\ngo_goroutines 10\n"
	metrics, err := parseCapacityMetrics([]byte(body))
	require.NoError(t, err)
	require.EqualValues(t, 10, metrics.goroutines)
	for _, invalid := range []string{"process_cpu_seconds_total NaN\n", "process_cpu_seconds_total 2 123\n", "go_goroutines 1.5\n", "go_sched_gomaxprocs_threads -1\n", `gitstore_api_process_instance_info{instance_id="api"} 1 123` + "\n"} {
		_, err := parseCapacityMetrics([]byte(body + invalid))
		require.Error(t, err)
	}
}

func TestSecretCapacityResourceSnapshotsReuseReplacementBaselines(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	var before, end, stable []secretCapacityResourceSnapshot
	for i, role := range []string{"api", "api", "controller", "controller", "git"} {
		first := secretCapacityResourceSnapshot{Role: role, ID: fmt.Sprintf("instance-%d", i),
			CPUSeconds: 10, CPUCapacity: 1, RSS: 100, Goroutines: 100, ObservedAt: started}
		last := first
		last.CPUSeconds, last.RSS, last.Goroutines, last.ObservedAt = 310, 120, 120, started.Add(10*time.Minute)
		settled := last
		settled.CPUSeconds, settled.RSS, settled.Goroutines, settled.ObservedAt = 900, 109, 109, started.Add(20*time.Minute)
		before, end, stable = append(before, first), append(end, last), append(stable, settled)
	}
	proof, err := secretCapacityResourceProofs(before, end, stable, started, capacityRecoveryResult{}, secretCapacityFaultObservations{})
	require.NoError(t, err)
	for _, row := range proof {
		require.Equal(t, .5, row.Cpu, "post-load stabilization CPU must not dilute the offered-load measurement")
		require.EqualValues(t, 109, row.RssAfter)
		require.EqualValues(t, 109, row.GoroutinesAfter)
	}
	end[1].ID, stable[1].ID = "api-replacement", "api-replacement"
	end[1].CPUSeconds = 151
	recovery := capacityRecoveryResult{label: "api_b", beforeStopAt: 5 * time.Minute, afterStartAt: 5*time.Minute + time.Second,
		beforeStop: capacityProcessMetrics{instanceID: before[1].ID, cpu: 160, resident: 108, gomaxprocs: 1},
		afterStart: capacityProcessMetrics{instanceID: end[1].ID, cpu: 1, resident: 10, gomaxprocs: 1}}
	end[2].ID, stable[2].ID = "controller-replacement", "controller-replacement"
	end[2].CPUSeconds = 76
	faults := secretCapacityFaultObservations{RestartConfirmed: true,
		RestartBefore: secretCapacityControllerSample{ID: before[2].ID, CPUSeconds: 235, RSS: 108, GOMAXPROCS: 1,
			ObservedAt: started.Add(450 * time.Second)},
		Replacement: secretCapacityControllerSample{ID: end[2].ID, CPUSeconds: 1, RSS: 10, GOMAXPROCS: 1,
			ObservedAt: started.Add(451 * time.Second)}}
	proof, err = secretCapacityResourceProofs(before, end, stable, started, recovery, faults)
	require.NoError(t, err)
	require.InDelta(t, 300.0/599, proof[1].Cpu, 1e-9)
	require.InDelta(t, 300.0/599, proof[2].Cpu, 1e-9)
	require.EqualValues(t, 109, proof[2].RssAfter, "use the warmed original baseline, not the replacement's startup RSS")
	stable[4].ID = "another-git"
	_, err = secretCapacityResourceProofs(before, end, stable, started, recovery, faults)
	require.Error(t, err, "singleton Git replacement is not part of this scenario")
}

func TestSecretCapacityCPUSetBounds(t *testing.T) {
	count, err := secretCapacityCPUSetSize("0-3,6,8-10")
	require.NoError(t, err)
	require.Equal(t, 8, count)
	for _, value := range []string{"", "1-0", "0-2,2", "0,0", "0-999999999", "-1", "0-x", "1-2-3"} {
		_, err := secretCapacityCPUSetSize(value)
		require.Error(t, err, value)
	}
}

func TestSecretCapacityDatasetReadsAcknowledgedFixtures(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acknowledged.jsonl")
	rows := []secretCapacityProductAcknowledgment{
		{Namespace: "dataset", Name: "product-1", Title: "First", Revision: strings.Repeat("a", 40), Acknowledged: true},
		{Namespace: "dataset", Name: "product-2", Title: "Second", Revision: strings.Repeat("b", 40), Acknowledged: true},
	}
	writeSecretCapacityAcknowledgments(t, path, rows)
	proof, err := readSecretCapacityAcknowledgments(t.Context(), path, "dataset")
	require.NoError(t, err)
	require.EqualValues(t, 2, proof.rows)
	require.Len(t, proof.fileSHA256, 64)
	require.Greater(t, proof.bytes, int64(0))
	for name, change := range map[string]func([]secretCapacityProductAcknowledgment){
		"unacknowledged":   func(r []secretCapacityProductAcknowledgment) { r[0].Acknowledged = false },
		"untitled":         func(r []secretCapacityProductAcknowledgment) { r[0].Title = " " },
		"wrong namespace":  func(r []secretCapacityProductAcknowledgment) { r[0].Namespace = "another" },
		"missing revision": func(r []secretCapacityProductAcknowledgment) { r[0].Revision = "" },
		"duplicate":        func(r []secretCapacityProductAcknowledgment) { r[1] = r[0] },
		"unordered":        func(r []secretCapacityProductAcknowledgment) { r[0], r[1] = r[1], r[0] },
	} {
		t.Run(name, func(t *testing.T) {
			bad := append([]secretCapacityProductAcknowledgment(nil), rows...)
			change(bad)
			writeSecretCapacityAcknowledgments(t, path, bad)
			_, err := readSecretCapacityAcknowledgments(t.Context(), path, "dataset")
			require.Error(t, err)
		})
	}
}

func TestSecretCapacityDatasetRejectsUnsafeManifestShape(t *testing.T) {
	for name, data := range map[string]string{
		"empty":           "",
		"unknown":         `{"namespace":"dataset","name":"product","title":"Title","revision":"` + strings.Repeat("a", 40) + `","acknowledged":true,"privateKey":"marker"}`,
		"duplicate field": `{"namespace":"dataset","name":"product","title":"Title","revision":"` + strings.Repeat("a", 40) + `","acknowledged":false,"acknowledged":true}`,
		"oversized line":  strings.Repeat("x", 4097),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "acknowledged.jsonl")
			require.NoError(t, os.WriteFile(path, []byte(data), 0600))
			_, err := readSecretCapacityAcknowledgments(t.Context(), path, "dataset")
			require.Error(t, err)
			require.NotContains(t, err.Error(), "marker")
		})
	}
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		source, link := filepath.Join(root, "source.jsonl"), filepath.Join(root, "linked.jsonl")
		require.NoError(t, os.WriteFile(source, []byte("{}"), 0600))
		require.NoError(t, os.Symlink(source, link))
		_, err := readSecretCapacityAcknowledgments(t.Context(), link, "dataset")
		require.Error(t, err)
	})
	t.Run("oversized manifest", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.jsonl")
		file, err := os.Create(path)
		require.NoError(t, err)
		require.NoError(t, file.Truncate(secretCapacityMaxManifestBytes+1))
		require.NoError(t, file.Close())
		_, err = readSecretCapacityAcknowledgments(t.Context(), path, "dataset")
		require.Error(t, err)
	})
}

func TestSecretCapacityDatasetVerifiesBothPaginatedReplicas(t *testing.T) {
	rows := []secretCapacityProductAcknowledgment{
		{Namespace: "dataset", Name: "product-1", Title: "First", Revision: strings.Repeat("a", 40), Acknowledged: true},
		{Namespace: "dataset", Name: "product-2", Title: "Second", Revision: strings.Repeat("b", 40), Acknowledged: true},
		{Namespace: "dataset", Name: "product-3", Title: "Third", Revision: strings.Repeat("c", 40), Acknowledged: true},
	}
	path := filepath.Join(t.TempDir(), "acknowledged.jsonl")
	writeSecretCapacityAcknowledgments(t, path, rows)
	var calls atomic.Int64
	var fault atomic.Int64
	handler := func(peer bool) http.HandlerFunc {
		return func(w http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			var input struct {
				Query     string `json:"query"`
				Variables struct {
					Namespace string  `json:"namespace"`
					First     int     `json:"first"`
					After     *string `json:"after"`
				} `json:"variables"`
			}
			if json.NewDecoder(request.Body).Decode(&input) != nil || input.Variables.First != 2 ||
				input.Variables.Namespace != "dataset" || request.Header.Get("Authorization") != "Bearer token" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if strings.Contains(input.Query, "totalCount") {
				t.Error("dataset verification must not request an aggregate")
			}
			start, end, next := 0, 2, true
			if input.Variables.After != nil {
				start, end, next = 2, 3, false
			}
			var edges []map[string]any
			for i := start; i < end; i++ {
				row := rows[i]
				if peer && fault.Load() == 5 {
					row = rows[len(rows)-1-i]
				}
				if peer && fault.Load() == 1 {
					row.Title = "wrong title"
				}
				if peer && fault.Load() == 2 {
					row = rows[0]
				}
				edges = append(edges, map[string]any{"cursor": fmt.Sprint(i + 1),
					"node": map[string]any{"metadata": map[string]string{"namespace": row.Namespace, "name": row.Name, "revision": row.Revision},
						"spec": map[string]string{"title": row.Title}}})
			}
			if peer && fault.Load() == 3 {
				edges = nil
			}
			endCursor := fmt.Sprint(end)
			if peer && fault.Load() == 4 && input.Variables.After != nil {
				endCursor = *input.Variables.After
				next = true
			}
			pageInfo := map[string]any{"hasNextPage": next, "endCursor": endCursor}
			if peer && fault.Load() == 6 {
				delete(pageInfo, "hasNextPage")
			}
			if peer && fault.Load() == 7 {
				pageInfo = nil
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"products": map[string]any{
				"edges": edges, "pageInfo": pageInfo,
			}}})
		}
	}
	a, b := httptest.NewServer(handler(false)), httptest.NewServer(handler(true))
	defer a.Close()
	defer b.Close()
	endpoints := []string{a.URL, b.URL}
	proof, err := verifySecretCapacityDataset(t.Context(), a.Client(), endpoints, "token", "dataset", path, 2, capacityModeDiagnostic)
	require.NoError(t, err)
	require.EqualValues(t, 3, proof.Proof.Rows)
	require.EqualValues(t, 2, proof.Proof.Pages)
	require.True(t, proof.Proof.CompletedBeforeLoad)
	require.Len(t, proof.Replicas, 2)
	require.EqualValues(t, 4, calls.Load())
	for failure := int64(1); failure <= 4; failure++ {
		fault.Store(failure)
		_, err := verifySecretCapacityDataset(t.Context(), a.Client(), endpoints, "token", "dataset", path, 2, capacityModeDiagnostic)
		require.Error(t, err)
	}
	fault.Store(5)
	_, err = verifySecretCapacityDataset(t.Context(), a.Client(), endpoints, "token", "dataset", path, 2, capacityModeDiagnostic)
	require.NoError(t, err, "replicas may paginate the same immutable dataset in different orders")
	for failure := int64(6); failure <= 7; failure++ {
		fault.Store(failure)
		_, err = verifySecretCapacityDataset(t.Context(), a.Client(), endpoints, "token", "dataset", path, 2, capacityModeDiagnostic)
		require.Error(t, err, "absent pagination metadata cannot imply the final page")
	}
	fault.Store(0)
	before := calls.Load()
	_, err = verifySecretCapacityDataset(t.Context(), a.Client(), endpoints, "token", "dataset", path, 2, capacityModeProduction)
	require.Error(t, err, "a smaller fixture cannot certify the five-million-row gate")
	require.Equal(t, before, calls.Load(), "reject undersized production fixtures before requesting pages")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = verifySecretCapacityDataset(ctx, a.Client(), endpoints, "token", "dataset", path, 2, capacityModeDiagnostic)
	require.ErrorIs(t, err, context.Canceled)
}

func writeSecretCapacityAcknowledgments(t *testing.T, path string, rows []secretCapacityProductAcknowledgment) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	for _, row := range rows {
		require.NoError(t, json.NewEncoder(file).Encode(row))
	}
	require.NoError(t, file.Close())
}
