// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/require"
)

func validateRBACCapacityAlpha(config rbacCapacityNativeSeedConfig) error {
	if config.Mode != "alpha" || config.Count != 1000 || config.ImportBatchSize != 60 ||
		config.Workers < 1 || config.Workers > 4 || config.VerifyRetained ||
		!config.Plan.MaximumRootBatch || !config.Plan.GeneralRoleMutations ||
		config.DecisionWorkload != nil || len(config.SourceModelFiles) != 2 || len(config.DecisionLogFiles) != 2 ||
		len(config.PrincipalTokenFiles) != len(config.Plan.principalSubjects()) || config.InitialAdminSubject == "" {
		return errors.New("RBAC alpha requires full native geometry, complete imports, general Role mutations, maximum root batch and independent replica evidence")
	}
	if !filepath.IsAbs(config.ReceiptDirectory) || filepath.Clean(config.ReceiptDirectory) != config.ReceiptDirectory ||
		config.PersonalTokenFile == "" {
		return errors.New("alpha requires an explicit canonical evidence directory and personal-owner credential")
	}
	paths := make(map[string]bool)
	for _, path := range append(slices.Clone(config.SourceModelFiles), config.DecisionLogFiles...) {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || paths[path] {
			return errors.New("alpha requires four distinct canonical source/decision capture paths")
		}
		paths[path] = true
	}
	for _, subject := range config.Plan.principalSubjects() {
		if config.PrincipalTokenFiles[subject] == "" {
			return errors.New("alpha requires every independently authored ordinary principal credential")
		}
	}
	workload := config.K6Workload
	if err := validateRBACCapacityK6Diagnostic(workload); err != nil {
		return err
	}
	if err := validateRBACCapacityMutation(config.Plan, config.Count, workload, config.InitialAdminSubject); err != nil {
		return err
	}
	if !workload.RootUnderLoad || workload.Duration != 31*time.Minute || workload.Rate != 500 ||
		workload.Mutation.Schedule.Duration != 30*time.Minute || workload.Mutation.Transport != "mixed" {
		return errors.New("RBAC alpha requires 1860s ordinary at 1000 HTTP/s total, 1800s of 50 writes/minute total, and mixed Git/GraphQL admission")
	}
	_, err := config.Plan.request(0, config.Count, workload.GroupMembers)
	return err
}

func validateRBACCapacityMode(config rbacCapacityNativeSeedConfig) error {
	switch config.Mode {
	case "diagnostic":
		return nil
	case "alpha":
		return validateRBACCapacityAlpha(config)
	default:
		return errors.New("RBAC production remains gated on separate larger-catalog interference and recovery evidence")
	}
}

func rbacCapacitySourceRevision(t *testing.T) string {
	t.Helper()
	status, err := exec.CommandContext(t.Context(), "git", "status", "--porcelain", "--untracked-files=normal").Output()
	require.NoError(t, err)
	require.Empty(t, strings.TrimSpace(string(status)), "alpha requires a clean attributable source checkout")
	revision, err := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(revision))
}

func sealRBACCapacityEvidence(t *testing.T, path string, record any) {
	t.Helper()
	content, err := json.Marshal(record)
	require.NoError(t, err)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		sealed, readErr := readRBACCapacityPrivateFixture(path)
		require.NoError(t, readErr)
		require.JSONEq(t, string(content), string(sealed), "immutable evidence must reproduce the same inputs and result")
		return
	}
	require.NoError(t, err)
	defer file.Close()
	_, err = file.Write(content)
	require.NoError(t, err)
	require.NoError(t, file.Sync())
}

func sealRBACCapacityProvenance(t *testing.T, config rbacCapacityNativeSeedConfig, topology rbacCapacityTopology, live bool) {
	t.Helper()
	revision := rbacCapacitySourceRevision(t)
	root, err := exec.CommandContext(t.Context(), "git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	script := filepath.Join(strings.TrimSpace(string(root)), "scripts", "capacity-build-receipt.py")
	receipts := make(map[string]json.RawMessage)
	for _, process := range topology.Processes {
		if process.Role == "scylla" {
			continue
		}
		require.NotEmpty(t, process.BuildReceipt, "every live service requires its repository-generated source/artifact receipt")
		if live && process.Role == "api" {
			pid := strings.TrimPrefix(process.ID, "native:")
			arguments, err := exec.CommandContext(t.Context(), "ps", "-p", pid, "-o", "command=").Output()
			require.NoError(t, err)
			require.NoError(t, checkRBACCapacityUnprofiledArgs(string(arguments)))
		}
		output, err := exec.CommandContext(t.Context(), "python3", script, "verify",
			process.Role, process.Executable, process.BuildReceipt).CombinedOutput()
		require.NoError(t, err, "%s", output)
		content, err := readRBACCapacityPrivateFixture(process.BuildReceipt)
		require.NoError(t, err)
		var receipt struct {
			ArtifactSHA256 string `json:"artifactSha256"`
		}
		require.NoError(t, json.Unmarshal(content, &receipt))
		require.Equal(t, process.ExecutableDigest, receipt.ArtifactSHA256,
			"the source build receipt must attest the verified running executable")
		receipts[process.ID] = content
	}
	content, err := readRBACCapacityPrivateFixture(os.Getenv("CAPACITY_RBAC_SEED_FILE"))
	require.NoError(t, err)
	digest := sha256.Sum256(content)
	path := filepath.Join(config.ReceiptDirectory, "rbac-provenance.json")
	if !live {
		require.FileExists(t, path, "sealed replay cannot invent missing pre-workload build/source evidence")
	}
	sealRBACCapacityEvidence(t, path, struct {
		SourceRevision, ConfigDigest string
		Topology                     rbacCapacityTopology
		Builds                       map[string]json.RawMessage
	}{revision, hex.EncodeToString(digest[:]), topology, receipts})
}

type rbacCapacityRuntimeReceipt struct {
	Owner         string
	Before, After [2]time.Time
	MetricsSHA256 [2][2]string
}

func captureRBACCapacityRuntime(t *testing.T, fixture rbacNativeFixture, directory, phase string) (observations [2]time.Time, digests [2]string) {
	t.Helper()
	require.Contains(t, []string{"before", "after"}, phase)
	require.Len(t, fixture.apis, 2)
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	for replica, endpoint := range fixture.apis {
		_, err := fetchCapacityMetricsObserved(client, endpoint, func(raw []byte) error {
			observations[replica] = time.Now()
			if _, err := checkRBACCapacityRuntimeMetrics(raw); err != nil {
				return err
			}
			digest := sha256.Sum256(raw)
			digests[replica] = hex.EncodeToString(digest[:])
			file, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("rbac-runtime-%s-%d.prom", phase, replica)),
				os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := file.Write(raw)
			return errors.Join(writeErr, file.Sync(), file.Close())
		})
		require.NoError(t, err, "alpha requires actual process and SDK resource observations from both APIs")
	}
	return observations, digests
}

type rbacCapacityRuntimeFacts struct {
	Instance                                  string
	Epoch, RSS, Heap, Goroutines, Unavailable float64
	SnapshotPeak, RebuildPeak, QueuePeak      float64
}

func checkRBACCapacityRuntimeMetrics(raw []byte) (facts rbacCapacityRuntimeFacts, err error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(bytes.NewReader(raw))
	if err != nil {
		return facts, err
	}
	value := func(name, pool string) (float64, error) {
		family := families[name]
		if family == nil {
			return 0, fmt.Errorf("required RBAC alpha metric %s is missing", name)
		}
		found, result := false, float64(0)
		for _, sample := range family.Metric {
			actualPool := ""
			for _, label := range sample.Label {
				if label.GetName() == "pool" {
					actualPool = label.GetValue()
				}
			}
			if actualPool != pool {
				continue
			}
			if found || sample.Gauge == nil {
				return 0, fmt.Errorf("ambiguous RBAC alpha gauge %s", name)
			}
			found, result = true, sample.Gauge.GetValue()
		}
		if !found || math.IsNaN(result) || math.IsInf(result, 0) || result < 0 ||
			strings.HasPrefix(name, "gitstore_authz_rbac_") && result != math.Trunc(result) {
			return 0, fmt.Errorf("invalid RBAC alpha gauge %s", name)
		}
		return result, nil
	}
	read := func(name, pool string) float64 {
		result, readErr := value(name, pool)
		err = errors.Join(err, readErr)
		return result
	}
	for _, pool := range []struct {
		name string
		cap  float64
	}{{"snapshot", 512 << 20}, {"rebuild", 4 * (64 << 20)}} {
		reserved := read("gitstore_authz_rbac_reserved_bytes", pool.name)
		peak := read("gitstore_authz_rbac_peak_reserved_bytes", pool.name)
		limit := read("gitstore_authz_rbac_reservation_limit_bytes", pool.name)
		if pool.name == "snapshot" {
			facts.SnapshotPeak = peak
		} else {
			facts.RebuildPeak = peak
		}
		if limit <= 0 || peak <= 0 || reserved > peak || peak > limit || limit > pool.cap {
			err = errors.Join(err, fmt.Errorf("RBAC %s reservations exceeded or lack their bounded SDK accounting", pool.name))
		}
	}
	buffer := read("gitstore_authz_rbac_rebuild_buffer_limit_bytes", "")
	if buffer <= 0 || buffer > 64<<20 || read("gitstore_authz_rbac_reservation_limit_bytes", "rebuild") > 4*buffer {
		err = errors.Join(err, errors.New("RBAC scratch requires at most four bounded 64 MiB buffers"))
	}
	queue := read("gitstore_authz_rbac_dirty_queue_entries", "")
	facts.QueuePeak = read("gitstore_authz_rbac_dirty_queue_peak_entries", "")
	queueLimit := read("gitstore_authz_rbac_dirty_queue_limit_entries", "")
	if queueLimit <= 0 || queueLimit > 1024 || queue > facts.QueuePeak || facts.QueuePeak > queueLimit {
		err = errors.Join(err, errors.New("RBAC dirty-tier queue exceeded its observed 1024-entry bound"))
	}
	facts.Epoch = read("process_start_time_seconds", "")
	facts.RSS = read("process_resident_memory_bytes", "")
	facts.Heap = read("go_memstats_heap_alloc_bytes", "")
	facts.Goroutines = read("go_goroutines", "")
	if facts.Epoch <= 0 || facts.RSS <= 0 || facts.Heap <= 0 || facts.Goroutines <= 0 {
		err = errors.Join(err, errors.New("alpha requires process epoch, RSS, heap and goroutine observations separately from reservations"))
	}
	if identity := families["gitstore_api_process_instance_info"]; identity != nil && len(identity.Metric) == 1 &&
		identity.Metric[0].Gauge != nil && identity.Metric[0].Gauge.GetValue() == 1 {
		for _, label := range identity.Metric[0].Label {
			if label.GetName() == "instance_id" {
				facts.Instance = label.GetValue()
			}
		}
	}
	if facts.Instance == "" {
		err = errors.Join(err, errors.New("alpha lacks an unambiguous API process identity"))
	}
	if unavailable := families["gitstore_authz_rbac_unavailable_total"]; unavailable != nil {
		for _, sample := range unavailable.Metric {
			if sample.Counter == nil || math.IsNaN(sample.Counter.GetValue()) || math.IsInf(sample.Counter.GetValue(), 0) || sample.Counter.GetValue() < 0 {
				err = errors.Join(err, errors.New("RBAC unavailable counter is invalid"))
				continue
			}
			facts.Unavailable += sample.Counter.GetValue()
		}
	}
	return facts, err
}

func checkRBACCapacityUnprofiledArgs(arguments string) error {
	for _, flag := range []string{"-test.cpuprofile", "-test.memprofile", "-test.blockprofile", "-test.mutexprofile", "-test.trace"} {
		if strings.Contains(arguments, flag) {
			return errors.New("healthy alpha refuses active Go test CPU/heap/block/mutex/trace profiling")
		}
	}
	return nil
}

func checkRBACCapacityRuntimeReceipt(record rbacCapacityRuntimeReceipt, window rbacCapacityK6ProcessWindow,
	before, after [2]rbacCapacityRuntimeFacts, maxVUs int,
) error {
	if record.Owner != window.Owner || window.StartedAt.IsZero() || !window.CompletedAt.After(window.StartedAt) ||
		maxVUs < 1 || maxVUs > 200 {
		return errors.New("runtime observations lack the owned bounded actual process window")
	}
	instances := make(map[string]bool)
	for replica := range 2 {
		start, end := record.Before[replica], record.After[replica]
		if start.IsZero() || !start.Before(window.StartedAt) || !end.After(window.CompletedAt) ||
			window.StartedAt.Sub(start) > 15*time.Second || end.Sub(window.CompletedAt) > 15*time.Second {
			return errors.New("SDK observations must closely bracket the complete ordinary/root/Namespace workload")
		}
		first, last := before[replica], after[replica]
		if first.Instance == "" || first.Instance != last.Instance || first.Epoch != last.Epoch || instances[first.Instance] ||
			last.Unavailable != first.Unavailable || last.SnapshotPeak < first.SnapshotPeak ||
			last.RebuildPeak < first.RebuildPeak || last.QueuePeak < first.QueuePeak ||
			last.Goroutines > first.Goroutines+float64(maxVUs+4) {
			return errors.New("healthy alpha requires distinct unchanged API epochs, monotonic high-water accounting, bounded goroutines and zero SDK unavailability")
		}
		instances[first.Instance] = true
	}
	return nil
}

func verifyRBACCapacityAlphaRuntime(t *testing.T, directory string, window rbacCapacityK6ProcessWindow, maxVUs int) {
	t.Helper()
	content, err := readRBACCapacityPrivateFixture(filepath.Join(directory, "rbac-runtime.json"))
	require.NoError(t, err)
	var record rbacCapacityRuntimeReceipt
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	require.NoError(t, decoder.Decode(&record))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
	var observations [2][2]rbacCapacityRuntimeFacts
	for phase, label := range []string{"before", "after"} {
		for replica := range 2 {
			file, err := openRBACCapacityPrivateFixture(filepath.Join(directory, fmt.Sprintf("rbac-runtime-%s-%d.prom", label, replica)), 8<<20)
			require.NoError(t, err)
			raw, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
			require.NoError(t, err)
			require.NoError(t, file.Close())
			observations[phase][replica], err = checkRBACCapacityRuntimeCapture(raw, record.MetricsSHA256[phase][replica])
			require.NoError(t, err)
		}
	}
	require.NoError(t, checkRBACCapacityRuntimeReceipt(record, window, observations[0], observations[1], maxVUs))
	for replica := range 2 {
		t.Logf("SDK budget/high-water bounds verified; RSS/heap/process costs separate: replica=%d before=%+v after=%+v",
			replica, observations[0][replica], observations[1][replica])
	}
}

func checkRBACCapacityRuntimeCapture(raw []byte, digest string) (rbacCapacityRuntimeFacts, error) {
	actual := sha256.Sum256(raw)
	if hex.EncodeToString(actual[:]) != digest {
		return rbacCapacityRuntimeFacts{}, errors.New("SDK capture differs from its sealed workload-clock receipt")
	}
	return checkRBACCapacityRuntimeMetrics(raw)
}

func checkRBACCapacityAlphaPercentiles(latencies []time.Duration, provider bool) error {
	if len(latencies) == 0 || !slices.IsSorted(latencies) || latencies[0] < 0 {
		return errors.New("alpha percentiles require every observed duration in sorted nonnegative order")
	}
	p99 := latencies[int(math.Ceil(float64(len(latencies))*.99))-1]
	if provider && p99 > 2*time.Millisecond || !provider && p99 > 5*time.Second {
		return errors.New("alpha failed provider p99 2ms or propagation p99 5s; propagation p95 2s is a non-blocking goal")
	}
	return nil
}

func rbacCapacityRootPropagationLatencies(record rbacCapacityRootPropagationReceipt, replica int,
	revisions map[string]string,
) ([]time.Duration, error) {
	if replica < 0 || replica > 1 || len(record.History) != 9 || len(record.ProbeStages) != 2 {
		return nil, errors.New("root percentiles require both complete four-phase source-joined stages")
	}
	latencies := make([]time.Duration, 0, 8)
	for step := range 8 {
		admission := record.History[step+1]
		prefix := fmt.Sprintf("%s-root-propagation-%d-%d-", record.Owner, step, replica)
		var first time.Time
		for id, probe := range record.ProbeStages[step/4][replica] {
			if strings.HasPrefix(id, prefix) && probe.Allowed == (step%2 == 0) && revisions[id] == admission.GitRevision &&
				!probe.ObservedAt.Before(admission.AcknowledgedAt) && (first.IsZero() || probe.ObservedAt.Before(first)) {
				first = probe.ObservedAt
			}
		}
		if first.IsZero() || first.Sub(admission.AcknowledgedAt) > 5*time.Second {
			return nil, errors.New("each root phase requires a matching decision at its exact admitted revision within the original five-second ACK bound")
		}
		latencies = append(latencies, first.Sub(admission.AcknowledgedAt))
	}
	return latencies, nil
}

func TestRBACCapacityAlphaRejectsWeakenedOrPartialDeclarations(t *testing.T) {
	makeConfig := func() rbacCapacityNativeSeedConfig {
		plan, config, _, _, _ := makeRBACCapacityMutationJournal(t)
		config.Mode, config.Count, config.Workers, config.ImportBatchSize = "alpha", 1000, 1, 60
		config.PersonalTokenFile, config.ReceiptDirectory = "/private/owner.jwt", "/private/alpha"
		config.SourceModelFiles = []string{"/private/api-0.sources", "/private/api-1.sources"}
		config.DecisionLogFiles = []string{"/private/api-0.decisions", "/private/api-1.decisions"}
		config.PrincipalTokenFiles = make(map[string]string)
		for _, subject := range plan.principalSubjects() {
			config.PrincipalTokenFiles[subject] = "/private/" + subject + ".jwt"
		}
		plan.MutationSubject = config.K6Workload.Mutation.Subject
		plan.GeneralRoleMutations, plan.MaximumRootBatch = true, true
		config.Plan = plan
		config.K6Workload.RootUnderLoad, config.K6Workload.Duration, config.K6Workload.Rate = true, 31*time.Minute, 500
		config.K6Workload.GroupMembers = []string{"reader-a"}
		config.K6Workload.Mutation.Schedule.Duration = 30 * time.Minute
		config.K6Workload.Mutation.Transport = "mixed"
		return config
	}
	require.NoError(t, validateRBACCapacityMode(makeConfig()))
	for _, test := range []struct {
		name string
		edit func(*rbacCapacityNativeSeedConfig)
	}{
		{"small-dataset", func(c *rbacCapacityNativeSeedConfig) { c.Count = 999 }},
		{"small-imports", func(c *rbacCapacityNativeSeedConfig) { c.ImportBatchSize = 10 }},
		{"missing-writer", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation = nil }},
		{"short-writes", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation.Schedule.Duration -= 12 * time.Second }},
		{"short-ordinary", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Duration -= time.Second }},
		{"long-ordinary", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Duration += time.Second }},
		{"under-rate", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Rate = 490 }},
		{"under-write-rate", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation.Schedule.Rate = 49 }},
		{"writer-fanout", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation.Schedule.Workers = 5 }},
		{"unbounded-pending", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation.Schedule.Pending = 2 }},
		{"git-only", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.Mutation.Transport = "" }},
		{"no-root-preamble", func(c *rbacCapacityNativeSeedConfig) { c.K6Workload.RootUnderLoad = false }},
		{"small-root", func(c *rbacCapacityNativeSeedConfig) { c.Plan.MaximumRootBatch = false }},
		{"condition-only", func(c *rbacCapacityNativeSeedConfig) { c.Plan.GeneralRoleMutations = false }},
		{"standalone-propagation", func(c *rbacCapacityNativeSeedConfig) {
			c.DecisionWorkload = &rbacCapacityDecisionWorkloadConfig{PropagationDiagnostic: true}
		}},
		{"retained-readonly", func(c *rbacCapacityNativeSeedConfig) { c.VerifyRetained = true }},
		{"missing-source", func(c *rbacCapacityNativeSeedConfig) { c.SourceModelFiles = nil }},
		{"shared-capture", func(c *rbacCapacityNativeSeedConfig) { c.SourceModelFiles[1] = c.SourceModelFiles[0] }},
		{"missing-principal", func(c *rbacCapacityNativeSeedConfig) { delete(c.PrincipalTokenFiles, "reader-a") }},
		{"production", func(c *rbacCapacityNativeSeedConfig) { c.Mode = "production" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := makeConfig()
			test.edit(&config)
			require.Error(t, validateRBACCapacityMode(config))
		})
	}
}

func makeRBACCapacityAlphaMetrics(instance string) string {
	var body strings.Builder
	for _, item := range []struct{ name, samples string }{
		{"gitstore_authz_rbac_reserved_bytes", "{pool=\"snapshot\"} 10\n%s{pool=\"rebuild\"} 5"},
		{"gitstore_authz_rbac_peak_reserved_bytes", "{pool=\"snapshot\"} 20\n%s{pool=\"rebuild\"} 10"},
		{"gitstore_authz_rbac_reservation_limit_bytes", "{pool=\"snapshot\"} 536870912\n%s{pool=\"rebuild\"} 268435456"},
		{"gitstore_authz_rbac_rebuild_buffer_limit_bytes", " 67108864"},
		{"gitstore_authz_rbac_dirty_queue_entries", " 0"},
		{"gitstore_authz_rbac_dirty_queue_peak_entries", " 8"},
		{"gitstore_authz_rbac_dirty_queue_limit_entries", " 1024"},
		{"process_start_time_seconds", " 1000"},
		{"process_resident_memory_bytes", " 800000000"},
		{"go_memstats_heap_alloc_bytes", " 600000000"},
		{"go_goroutines", " 100"},
		{"gitstore_api_process_instance_info", "{instance_id=\"" + instance + "\"} 1"},
	} {
		samples := strings.ReplaceAll(item.samples, "%s", item.name)
		fmt.Fprintf(&body, "# TYPE %s gauge\n%s%s\n", item.name, item.name, samples)
	}
	body.WriteString("# TYPE gitstore_authz_rbac_unavailable_total counter\ngitstore_authz_rbac_unavailable_total{reason=\"tier\"} 0\n")
	return body.String()
}

func TestRBACCapacityNativeExecutableChild(t *testing.T) {
	if os.Getenv("GITSTORE_CAPACITY_EXECUTABLE_TEST_CHILD") != "1" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	fmt.Fprintln(os.Stdout, listener.Addr().String())
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		connection.Close()
	}
}

func TestRBACCapacityNativeExecutableTracksRunningImage(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("native executable provenance supports Linux and macOS")
	}
	original, err := os.Executable()
	require.NoError(t, err)
	content, err := os.ReadFile(original)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "native-service")
	require.NoError(t, os.Chmod(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, content, 0o700))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, path, "-test.run=^TestRBACCapacityNativeExecutableChild$", "-test.timeout=30s")
	child.Env = append(os.Environ(), "GITSTORE_CAPACITY_EXECUTABLE_TEST_CHILD=1")
	output, err := child.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.Start())
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})
	address, err := bufio.NewReader(output).ReadString('\n')
	require.NoError(t, err)
	id := strconv.Itoa(child.Process.Pid)
	epoch := func() string {
		command := exec.CommandContext(ctx, "ps", "-p", id, "-o", "lstart=")
		command.Env = append(os.Environ(), "LC_ALL=C")
		output, err := command.Output()
		require.NoError(t, err)
		return strings.TrimSpace(string(output))
	}
	digest := sha256.Sum256(content)
	topology := rbacCapacityTopology{Processes: []rbacCapacityProcess{{
		ID: "native:" + id, Role: "git", Epoch: epoch(), Executable: path,
		ExecutableDigest: hex.EncodeToString(digest[:]), Endpoint: strings.TrimSpace(address),
	}}}
	require.NoError(t, topology.verifyLive(ctx), "the actual unchanged executable must pass")

	replacement := append(content, '\n')
	require.NoError(t, os.WriteFile(path+".replacement", replacement, 0o700))
	require.NoError(t, os.Rename(path+".replacement", path))
	require.Equal(t, topology.Processes[0].Epoch, epoch(), "atomic replacement does not restart the old process")
	connection, err := net.DialTimeout("tcp", topology.Processes[0].Endpoint, time.Second)
	require.NoError(t, err, "the old executable is still running")
	require.NoError(t, connection.Close())
	replacementDigest := sha256.Sum256(replacement)
	topology.Processes[0].ExecutableDigest = hex.EncodeToString(replacementDigest[:])
	require.Error(t, topology.verifyLive(ctx), "the replacement file cannot attest the still-running old image")
	topology.Processes[0].ExecutableDigest = hex.EncodeToString(digest[:])
	require.Error(t, topology.verifyLive(ctx), "the old digest cannot attest the replacement pathname either")
}

func TestRBACCapacityAlphaRequiresActualSDKBoundsNotDeclaredAllocations(t *testing.T) {
	raw := makeRBACCapacityAlphaMetrics("api-a")
	facts, err := checkRBACCapacityRuntimeMetrics([]byte(raw))
	require.NoError(t, err)
	require.EqualValues(t, 800000000, facts.RSS, "process RSS is reported, not confused with the 512 MiB snapshot reservation cap")
	digest := sha256.Sum256([]byte(raw))
	_, err = checkRBACCapacityRuntimeCapture([]byte(raw), hex.EncodeToString(digest[:]))
	require.NoError(t, err)
	_, err = checkRBACCapacityRuntimeCapture([]byte(strings.Replace(raw, "peak_entries 8", "peak_entries 9", 1)), hex.EncodeToString(digest[:]))
	require.Error(t, err, "even an in-budget replacement cannot substitute for the original SDK capture")
	for _, mutation := range []struct{ name, old, replacement string }{
		{"snapshot-overflow", "peak_reserved_bytes{pool=\"snapshot\"} 20", "peak_reserved_bytes{pool=\"snapshot\"} 536870913"},
		{"scratch-buffer-overflow", "buffer_limit_bytes 67108864", "buffer_limit_bytes 67108865"},
		{"aggregate-scratch-overflow", "limit_bytes{pool=\"rebuild\"} 268435456", "limit_bytes{pool=\"rebuild\"} 268435457"},
		{"queue-overflow", "peak_entries 8", "peak_entries 1025"},
		{"enlarged-queue", "limit_entries 1024", "limit_entries 2048"},
		{"missing-heap", "go_memstats_heap_alloc_bytes 600000000", ""},
		{"nonfinite", "go_goroutines 100", "go_goroutines NaN"},
		{"missing-identity", "instance_id=\"api-a\"", "other=\"api-a\""},
		{"inactive-identity", "instance_id=\"api-a\"} 1", "instance_id=\"api-a\"} 0"},
		{"fractional-queue", "peak_entries 8", "peak_entries 8.5"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			changed := strings.Replace(raw, mutation.old, mutation.replacement, 1)
			require.NotEqual(t, raw, changed)
			_, err := checkRBACCapacityRuntimeMetrics([]byte(changed))
			require.Error(t, err)
		})
	}
}

func TestRBACCapacityAlphaRuntimeMustBracketBothUnchangedAPIs(t *testing.T) {
	window := rbacCapacityK6ProcessWindow{Owner: "owned", StartedAt: time.Unix(1001, 0), CompletedAt: time.Unix(2861, 0)}
	record := rbacCapacityRuntimeReceipt{Owner: window.Owner,
		Before: [2]time.Time{window.StartedAt.Add(-time.Second), window.StartedAt.Add(-time.Millisecond)},
		After:  [2]time.Time{window.CompletedAt.Add(time.Millisecond), window.CompletedAt.Add(time.Second)}}
	var before [2]rbacCapacityRuntimeFacts
	for replica := range 2 {
		var err error
		before[replica], err = checkRBACCapacityRuntimeMetrics([]byte(makeRBACCapacityAlphaMetrics(fmt.Sprint("api-", replica))))
		require.NoError(t, err)
	}
	require.NoError(t, checkRBACCapacityRuntimeReceipt(record, window, before, before, 200))
	for _, name := range []string{"late-start", "early-end", "shared-api", "restart", "unavailable", "peak-reset", "goroutine-leak"} {
		t.Run(name, func(t *testing.T) {
			changed, after := record, before
			switch name {
			case "late-start":
				changed.Before[1] = window.StartedAt.Add(time.Nanosecond)
			case "early-end":
				changed.After[1] = window.CompletedAt.Add(-time.Nanosecond)
			case "shared-api":
				before := [2]rbacCapacityRuntimeFacts{before[0], before[0]}
				require.Error(t, checkRBACCapacityRuntimeReceipt(changed, window, before, before, 200))
				return
			case "restart":
				after[1].Epoch++
			case "unavailable":
				after[1].Unavailable++
			case "peak-reset":
				after[1].SnapshotPeak--
			case "goroutine-leak":
				after[1].Goroutines += 205
			}
			require.Error(t, checkRBACCapacityRuntimeReceipt(changed, window, before, after, 200))
		})
	}
}

func TestRBACCapacityAlphaPercentilesRetainIndependentACKAndProviderClocks(t *testing.T) {
	require.NoError(t, checkRBACCapacityUnprofiledArgs("/private/api.test -test.run=^TestRBACNativeProcess$"))
	require.Error(t, checkRBACCapacityUnprofiledArgs("/private/api.test -test.cpuprofile=/private/profile.pb.gz"))
	require.NoError(t, checkRBACCapacityAlphaPercentiles([]time.Duration{time.Millisecond, 2 * time.Millisecond}, true))
	require.Error(t, checkRBACCapacityAlphaPercentiles([]time.Duration{2*time.Millisecond + time.Nanosecond}, true))
	require.NoError(t, checkRBACCapacityAlphaPercentiles([]time.Duration{2 * time.Second}, false))
	require.NoError(t, checkRBACCapacityAlphaPercentiles([]time.Duration{2*time.Second + time.Nanosecond}, false))
	require.NoError(t, checkRBACCapacityAlphaPercentiles([]time.Duration{3120 * time.Millisecond, 3250 * time.Millisecond}, false))
	require.NoError(t, checkRBACCapacityAlphaPercentiles([]time.Duration{5 * time.Second}, false))
	require.Error(t, checkRBACCapacityAlphaPercentiles([]time.Duration{5*time.Second + time.Nanosecond}, false))
	_, _, record, _, _ := makeRBACCapacityRootUnderLoadJournal(t)
	for replica := range 2 {
		revisions := make(map[string]string)
		for step := range 8 {
			id := fmt.Sprintf("%s-root-propagation-%d-%d-00", record.Owner, step, replica)
			revisions[id] = record.History[step+1].GitRevision
		}
		latencies, err := rbacCapacityRootPropagationLatencies(record, replica, revisions)
		require.NoError(t, err)
		require.Len(t, latencies, 8)
		slices.Sort(latencies)
		require.NoError(t, checkRBACCapacityAlphaPercentiles(latencies, false))
		id := fmt.Sprintf("%s-root-propagation-2-%d-00", record.Owner, replica)
		revisions[id] = record.History[1].GitRevision
		_, err = rbacCapacityRootPropagationLatencies(record, replica, revisions)
		require.Error(t, err, "an older same-verdict grant cannot hide a slow restoration")
		revisions[id] = record.History[3].GitRevision
		probe := record.ProbeStages[0][replica][id]
		original := probe
		probe.ObservedAt = record.History[3].AcknowledgedAt.Add(31 * time.Second)
		record.ProbeStages[0][replica][id] = probe
		_, err = rbacCapacityRootPropagationLatencies(record, replica, revisions)
		require.Error(t, err, "small claimed Latencies cannot conceal an actual response beyond the original five-second ACK bound")
		record.ProbeStages[0][replica][id] = original
	}
}
