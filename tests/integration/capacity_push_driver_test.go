// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The capacity push driver is the Git-push companion for capacity profiles
// whose load generator cannot push (k6). It offers one commit per interval to
// a bounded worker pool, pushes it to a single repository's main branch, and
// writes push evidence for the profile's verifier. Offers that find every
// worker busy are counted as dropped, never queued without bound.
//
// Run it next to a profile, for example the category hierarchy profile:
//
//	CAPACITY_PUSH_DRIVER=1 \
//	CAPACITY_PUSH_REMOTE=http://localhost:8080/acme/gitstore-system.git \
//	CAPACITY_PUSH_TOKEN_FILE=/path/to/token \
//	CAPACITY_PUSH_DURATION=360s CAPACITY_PUSH_INTERVAL=1s \
//	CAPACITY_PUSH_EVIDENCE=/abs/path/push-evidence.json \
//	go test -run '^TestCapacityGitPushDriver$' -timeout 2h ./...
type capacityPushConfig struct {
	remote, tokenFile, namespace, repository, kind, runID, evidence string
	duration, interval                                              time.Duration
	workers                                                         int
}

// capacityPushEvidence is the evidence document. repository/pushes/failures/
// firstPushMs/lastPushMs is the contract the category hierarchy verifier
// reads; the remaining fields are diagnostic.
type capacityPushEvidence struct {
	Repository  string            `json:"repository"`
	Namespace   string            `json:"namespace,omitempty"`
	Kind        string            `json:"kind"`
	RunID       string            `json:"runId"`
	Workers     int               `json:"workers"`
	IntervalMs  int64             `json:"intervalMs"`
	Offered     int               `json:"offered"`
	Pushes      int               `json:"pushes"`
	Failures    int               `json:"failures"`
	Dropped     int               `json:"dropped"`
	Retries     int               `json:"retries"`
	FirstPushMs int64             `json:"firstPushMs"`
	LastPushMs  int64             `json:"lastPushMs"`
	LatencyMs   capacityPushStats `json:"latencyMs"`
}

type capacityPushStats struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	P99 int64 `json:"p99"`
	Max int64 `json:"max"`
}

type capacityPushResult struct {
	acknowledged bool
	retried      bool
	at           time.Time
	latency      time.Duration
}

func loadCapacityPushConfig() (capacityPushConfig, error) {
	cfg := capacityPushConfig{
		remote:     os.Getenv("CAPACITY_PUSH_REMOTE"),
		tokenFile:  os.Getenv("CAPACITY_PUSH_TOKEN_FILE"),
		namespace:  os.Getenv("CAPACITY_PUSH_NAMESPACE"),
		repository: getEnv("CAPACITY_PUSH_REPOSITORY", "gitstore-system"),
		kind:       getEnv("CAPACITY_PUSH_KIND", "category"),
		runID:      getEnv("CAPACITY_RUN_ID", strconv.FormatInt(time.Now().Unix(), 36)),
		evidence:   os.Getenv("CAPACITY_PUSH_EVIDENCE"),
		interval:   time.Second,
		workers:    1,
	}
	var err error
	if cfg.duration, err = time.ParseDuration(os.Getenv("CAPACITY_PUSH_DURATION")); err != nil {
		return cfg, errors.New("CAPACITY_PUSH_DURATION is required (for example 360s)")
	}
	if raw := os.Getenv("CAPACITY_PUSH_INTERVAL"); raw != "" {
		if cfg.interval, err = time.ParseDuration(raw); err != nil {
			return cfg, fmt.Errorf("CAPACITY_PUSH_INTERVAL: %w", err)
		}
	}
	if raw := os.Getenv("CAPACITY_PUSH_WORKERS"); raw != "" {
		if cfg.workers, err = strconv.Atoi(raw); err != nil {
			return cfg, fmt.Errorf("CAPACITY_PUSH_WORKERS: %w", err)
		}
	}
	switch {
	case cfg.remote == "":
		return cfg, errors.New("CAPACITY_PUSH_REMOTE is required")
	case cfg.evidence == "" || !filepath.IsAbs(cfg.evidence):
		return cfg, errors.New("CAPACITY_PUSH_EVIDENCE must be an absolute path")
	case cfg.duration <= 0 || cfg.duration > 2*time.Hour:
		return cfg, errors.New("CAPACITY_PUSH_DURATION must be within (0, 2h]")
	case cfg.interval < 50*time.Millisecond:
		return cfg, errors.New("CAPACITY_PUSH_INTERVAL must be at least 50ms")
	case cfg.workers < 1 || cfg.workers > 32:
		return cfg, errors.New("CAPACITY_PUSH_WORKERS must be within [1, 32]")
	case cfg.kind != "category" && cfg.kind != "product":
		return cfg, errors.New("CAPACITY_PUSH_KIND must be category or product")
	}
	for _, r := range cfg.runID {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return cfg, errors.New("CAPACITY_RUN_ID must be a lower-case DNS label fragment")
		}
	}
	return cfg, nil
}

// capacityPushManifest renders one bounded manifest per push. Names are
// unique per run and sequence, so pushes never contend on the same path.
func capacityPushManifest(kind, runID, namespace string, sequence int) (path string, content []byte) {
	name := fmt.Sprintf("cpd-%s-%06d", runID, sequence)
	ns := ""
	if namespace != "" {
		ns = "\n  namespace: " + namespace
	}
	switch kind {
	case "product":
		return "products/" + name + ".md", []byte(fmt.Sprintf("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: Product\nmetadata:\n  name: %s%s\n  labels:\n    capacity-push-run: %s\nspec:\n  title: Capacity push %d\n---\nCapacity push driver fixture.\n", name, ns, runID, sequence))
	default:
		return "categories/" + name + ".md", []byte(fmt.Sprintf("---\napiVersion: catalog.gitstore.dev/v1beta1\nkind: CategoryTaxonomy\nmetadata:\n  name: %s%s\n  labels:\n    capacity-push-run: %s\nspec:\n  title: Capacity push %d\n---\nCapacity push driver fixture.\n", name, ns, runID, sequence))
	}
}

// runCapacityPushSchedule offers one push per interval for duration to at
// most workers concurrent pushes and summarizes the outcome.
func runCapacityPushSchedule(ctx context.Context, duration, interval time.Duration, workers int,
	push func(ctx context.Context, sequence int) capacityPushResult) (offered, dropped int, results []capacityPushResult) {
	slots := make(chan struct{}, workers)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	offer := func() {
		sequence := offered
		offered++
		select {
		case slots <- struct{}{}:
		default:
			dropped++
			return
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			result := push(ctx, sequence)
			mu.Lock()
			results = append(results, result)
			mu.Unlock()
		}()
	}
	offer()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-deadline.C:
			break loop
		case <-ticker.C:
			offer()
		}
	}
	wg.Wait()
	return offered, dropped, results
}

func summarizeCapacityPushes(cfg capacityPushConfig, offered, dropped int, results []capacityPushResult) capacityPushEvidence {
	evidence := capacityPushEvidence{
		Repository: cfg.repository, Namespace: cfg.namespace, Kind: cfg.kind, RunID: cfg.runID,
		Workers: cfg.workers, IntervalMs: cfg.interval.Milliseconds(), Offered: offered, Dropped: dropped,
	}
	var latencies []int64
	for _, result := range results {
		if result.retried {
			evidence.Retries++
		}
		if !result.acknowledged {
			evidence.Failures++
			continue
		}
		evidence.Pushes++
		at := result.at.UnixMilli()
		if evidence.FirstPushMs == 0 || at < evidence.FirstPushMs {
			evidence.FirstPushMs = at
		}
		if at > evidence.LastPushMs {
			evidence.LastPushMs = at
		}
		latencies = append(latencies, result.latency.Milliseconds())
	}
	if len(latencies) > 0 {
		sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
		quantile := func(q float64) int64 {
			index := int(float64(len(latencies))*q+0.999999) - 1
			if index < 0 {
				index = 0
			}
			return latencies[index]
		}
		evidence.LatencyMs = capacityPushStats{P50: quantile(0.50), P95: quantile(0.95), P99: quantile(0.99), Max: latencies[len(latencies)-1]}
	}
	return evidence
}

func writeCapacityPushEvidence(path string, evidence capacityPushEvidence) error {
	data, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

// capacityPushWorker owns one clone. Every push is a single new file, so a
// rejected (non-fast-forward) push is retried once after rebasing onto the
// concurrent writer's commit.
type capacityPushWorker struct {
	directory, token string
	cfg              capacityPushConfig
}

func newCapacityPushWorker(ctx context.Context, cfg capacityPushConfig, root, token string, index int) (*capacityPushWorker, error) {
	directory := filepath.Join(root, fmt.Sprintf("worker-%02d", index))
	if _, err := secretCapacityGit(ctx, root, token, "clone", "--quiet", "--no-tags", "--depth=1", "--", cfg.remote, directory); err != nil {
		return nil, fmt.Errorf("clone %s: %w", cfg.repository, err)
	}
	for _, setting := range [][2]string{
		{"user.name", "GitStore Capacity"}, {"user.email", "capacity@gitstore.dev"},
		{"commit.gpgSign", "false"}, {"core.hooksPath", os.DevNull}, {"pull.rebase", "true"},
	} {
		if _, err := secretCapacityGit(ctx, directory, "", "config", setting[0], setting[1]); err != nil {
			return nil, err
		}
	}
	return &capacityPushWorker{directory: directory, token: token, cfg: cfg}, nil
}

func (w *capacityPushWorker) push(ctx context.Context, sequence int) capacityPushResult {
	started := time.Now()
	result := capacityPushResult{}
	path, content := capacityPushManifest(w.cfg.kind, w.cfg.runID, w.cfg.namespace, sequence)
	full := filepath.Join(w.directory, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return result
	}
	if err := os.WriteFile(full, content, 0o600); err != nil {
		return result
	}
	for _, args := range [][]string{{"add", "--", path}, {"commit", "--quiet", "-m", fmt.Sprintf("capacity push %s %d", w.cfg.runID, sequence)}} {
		if _, err := secretCapacityGit(ctx, w.directory, "", args...); err != nil {
			return result
		}
	}
	_, err := secretCapacityGit(ctx, w.directory, w.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
	if err != nil && ctx.Err() == nil {
		result.retried = true
		if _, pullErr := secretCapacityGit(ctx, w.directory, w.token, "pull", "--quiet", "--rebase", "origin", "main"); pullErr == nil {
			_, err = secretCapacityGit(ctx, w.directory, w.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
		}
	}
	result.acknowledged = err == nil
	result.at = time.Now()
	result.latency = time.Since(started)
	return result
}

func TestCapacityGitPushDriver(t *testing.T) {
	if os.Getenv("CAPACITY_PUSH_DRIVER") != "1" {
		t.Skip("set CAPACITY_PUSH_DRIVER=1 to run the capacity Git push driver")
	}
	cfg, err := loadCapacityPushConfig()
	if err != nil {
		t.Fatal(err)
	}
	token := ""
	if cfg.tokenFile != "" {
		raw, err := os.ReadFile(cfg.tokenFile)
		if err != nil {
			t.Fatalf("read CAPACITY_PUSH_TOKEN_FILE: %v", err)
		}
		token = strings.TrimSpace(string(raw))
	}
	ctx, cancel := context.WithTimeout(t.Context(), cfg.duration+5*time.Minute)
	defer cancel()

	root := t.TempDir()
	workers := make([]*capacityPushWorker, cfg.workers)
	for i := range workers {
		if workers[i], err = newCapacityPushWorker(ctx, cfg, root, token, i); err != nil {
			t.Fatal(err)
		}
	}
	// Each in-flight push holds one slot; a slot maps to one clone, so a
	// clone is never used by two pushes at once.
	free := make(chan *capacityPushWorker, len(workers))
	for _, worker := range workers {
		free <- worker
	}
	offered, dropped, results := runCapacityPushSchedule(ctx, cfg.duration, cfg.interval, cfg.workers, func(ctx context.Context, sequence int) capacityPushResult {
		worker := <-free
		defer func() { free <- worker }()
		return worker.push(ctx, sequence)
	})
	evidence := summarizeCapacityPushes(cfg, offered, dropped, results)
	if err := writeCapacityPushEvidence(cfg.evidence, evidence); err != nil {
		t.Fatalf("write push evidence: %v", err)
	}
	t.Logf("push evidence: offered=%d pushes=%d failures=%d dropped=%d retries=%d p95=%dms",
		evidence.Offered, evidence.Pushes, evidence.Failures, evidence.Dropped, evidence.Retries, evidence.LatencyMs.P95)
	if evidence.Pushes == 0 {
		t.Fatal("no push was acknowledged")
	}
}

// In-process coverage of the driver's scheduling and evidence; no Git service
// is needed.
func TestCapacityPushDriverScheduleBoundsWorkAndSummarizes(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	offered, dropped, results := runCapacityPushSchedule(t.Context(), 300*time.Millisecond, 10*time.Millisecond, 2,
		func(_ context.Context, sequence int) capacityPushResult {
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			mu.Unlock()
			time.Sleep(35 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
			return capacityPushResult{acknowledged: sequence%5 != 0, retried: sequence%7 == 0, at: time.Now(), latency: time.Duration(sequence) * time.Millisecond}
		})
	if peak > 2 {
		t.Fatalf("peak concurrency %d exceeds the worker bound", peak)
	}
	if dropped == 0 {
		t.Fatal("offers faster than the workers can push must be dropped, not queued")
	}
	if offered != dropped+len(results) {
		t.Fatalf("offered %d != dropped %d + completed %d", offered, dropped, len(results))
	}

	cfg := capacityPushConfig{repository: "gitstore-system", namespace: "acme", kind: "category", runID: "r1", workers: 2, interval: 10 * time.Millisecond}
	evidence := summarizeCapacityPushes(cfg, offered, dropped, results)
	if evidence.Pushes+evidence.Failures != len(results) || evidence.Pushes == 0 || evidence.Failures == 0 {
		t.Fatalf("unexpected evidence counts: %+v", evidence)
	}
	if evidence.FirstPushMs == 0 || evidence.LastPushMs < evidence.FirstPushMs {
		t.Fatalf("push window not recorded: %+v", evidence)
	}
	if evidence.LatencyMs.P95 > evidence.LatencyMs.Max || evidence.LatencyMs.P50 > evidence.LatencyMs.P95 {
		t.Fatalf("latency quantiles out of order: %+v", evidence.LatencyMs)
	}

	path := filepath.Join(t.TempDir(), "push-evidence.json")
	if err := writeCapacityPushEvidence(path, evidence); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repository", "pushes", "failures", "firstPushMs", "lastPushMs"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("evidence is missing verifier key %q", key)
		}
	}
}

func TestCapacityPushManifestsAreUniqueAndBounded(t *testing.T) {
	seen := map[string]bool{}
	for _, kind := range []string{"category", "product"} {
		for sequence := range 3 {
			path, content := capacityPushManifest(kind, "r1", "acme", sequence)
			if seen[path] {
				t.Fatalf("duplicate manifest path %s", path)
			}
			seen[path] = true
			if len(content) > 1024 || !strings.Contains(string(content), "namespace: acme") {
				t.Fatalf("unexpected manifest %s:\n%s", path, content)
			}
		}
	}
}
