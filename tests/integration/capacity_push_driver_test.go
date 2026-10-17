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
	"sync/atomic"
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
	duration, interval, drainTimeout                                time.Duration
	workers                                                         int
}

// capacityPushEvidence binds complete offer/outcome accounting and timing to
// the category verifier's repository, namespace, kind and run identity.
type capacityPushEvidence struct {
	Repository        string            `json:"repository"`
	Namespace         string            `json:"namespace,omitempty"`
	Kind              string            `json:"kind"`
	RunID             string            `json:"runId"`
	Workers           int               `json:"workers"`
	IntervalMs        int64             `json:"intervalMs"`
	IntervalNs        int64             `json:"intervalNs"`
	DurationNs        int64             `json:"durationNs"`
	Scheduled         int               `json:"scheduled"`
	MissedSchedules   int               `json:"missedSchedules"`
	OfferedDurationNs int64             `json:"offeredDurationNs"`
	DrainDurationNs   int64             `json:"drainDurationNs"`
	DrainTimeoutNs    int64             `json:"drainTimeoutNs"`
	DrainTimedOut     bool              `json:"drainTimedOut"`
	Cancelled         bool              `json:"cancelled"`
	Offered           int               `json:"offered"`
	Pushes            int               `json:"pushes"`
	Failures          int               `json:"failures"`
	Dropped           int               `json:"dropped"`
	Retries           int               `json:"retries"`
	FirstPushMs       int64             `json:"firstPushMs"`
	LastPushMs        int64             `json:"lastPushMs"`
	LatencyMs         capacityPushStats `json:"latencyMs"`
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

type capacityPushSchedule struct {
	scheduled, offered, dropped, missedSchedules int
	offeredDuration, drainDuration               time.Duration
	drainTimedOut, cancelled                     bool
	results                                      []capacityPushResult
}

func loadCapacityPushConfig() (capacityPushConfig, error) {
	cfg := capacityPushConfig{
		remote:       os.Getenv("CAPACITY_PUSH_REMOTE"),
		tokenFile:    os.Getenv("CAPACITY_PUSH_TOKEN_FILE"),
		namespace:    os.Getenv("CAPACITY_PUSH_NAMESPACE"),
		repository:   getEnv("CAPACITY_PUSH_REPOSITORY", "gitstore-system"),
		kind:         getEnv("CAPACITY_PUSH_KIND", "category"),
		runID:        getEnv("CAPACITY_RUN_ID", strconv.FormatInt(time.Now().Unix(), 36)),
		evidence:     os.Getenv("CAPACITY_PUSH_EVIDENCE"),
		interval:     time.Second,
		drainTimeout: capacityMutationDrainTimeout,
		workers:      1,
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
	case int64(cfg.interval) > 9007199254740991:
		return cfg, errors.New("CAPACITY_PUSH_INTERVAL exceeds exact capacity JSON precision")
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
func runCapacityPushSchedule(ctx context.Context, cfg capacityPushConfig,
	push func(ctx context.Context, sequence int) capacityPushResult) capacityPushSchedule {
	schedule := capacityPushSchedule{scheduled: int((cfg.duration-1)/cfg.interval) + 1}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	slots := make(chan struct{}, cfg.workers)
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	started := time.Now()
	offer := func(sequence int, offeredAt time.Time) {
		schedule.offered++
		if time.Since(offeredAt) >= cfg.interval {
			schedule.missedSchedules++
		}
		select {
		case slots <- struct{}{}:
		default:
			schedule.dropped++
			return
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			result := push(workCtx, sequence)
			if result.acknowledged {
				result.latency = result.at.Sub(offeredAt)
			}
			mu.Lock()
			schedule.results = append(schedule.results, result)
			mu.Unlock()
		}()
	}
	for sequence := range schedule.scheduled {
		if ctx.Err() != nil {
			break
		}
		offeredAt := started.Add(time.Duration(sequence) * cfg.interval)
		timer := time.NewTimer(max(time.Duration(0), time.Until(offeredAt)))
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		if ctx.Err() != nil || time.Since(started) >= cfg.duration {
			break
		}
		offer(sequence, offeredAt)
	}
	timer := time.NewTimer(max(time.Duration(0), time.Until(started.Add(cfg.duration))))
	select {
	case <-ctx.Done():
		timer.Stop()
	case <-timer.C:
	}
	schedule.offeredDuration = time.Since(started)
	drainStarted := time.Now()
	drainTimeout := cfg.drainTimeout
	if drainTimeout == 0 {
		drainTimeout = capacityMutationDrainTimeout
	}
	drained := make(chan struct{})
	go func() {
		wg.Wait()
		close(drained)
	}()
	drainTimer := time.NewTimer(drainTimeout)
	select {
	case <-drained:
		drainTimer.Stop()
	case <-drainTimer.C:
		schedule.drainTimedOut = true
		cancel()
		<-drained
	}
	schedule.drainDuration = time.Since(drainStarted)
	schedule.drainTimedOut = schedule.drainTimedOut || schedule.drainDuration > drainTimeout
	schedule.cancelled = ctx.Err() != nil
	return schedule
}

func summarizeCapacityPushes(cfg capacityPushConfig, schedule capacityPushSchedule) capacityPushEvidence {
	drainTimeout := cfg.drainTimeout
	if drainTimeout == 0 {
		drainTimeout = capacityMutationDrainTimeout
	}
	evidence := capacityPushEvidence{
		Repository: cfg.repository, Namespace: cfg.namespace, Kind: cfg.kind, RunID: cfg.runID,
		Workers: cfg.workers, IntervalMs: cfg.interval.Milliseconds(), IntervalNs: int64(cfg.interval),
		DurationNs: int64(cfg.duration), Scheduled: schedule.scheduled,
		Offered: schedule.offered, Dropped: schedule.dropped, MissedSchedules: schedule.missedSchedules,
		OfferedDurationNs: int64(schedule.offeredDuration), DrainDurationNs: int64(schedule.drainDuration),
		DrainTimeoutNs: int64(drainTimeout), DrainTimedOut: schedule.drainTimedOut, Cancelled: schedule.cancelled,
	}
	var latencies []int64
	for _, result := range schedule.results {
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
	schedule := runCapacityPushSchedule(ctx, cfg, func(ctx context.Context, sequence int) capacityPushResult {
		worker := <-free
		defer func() { free <- worker }()
		return worker.push(ctx, sequence)
	})
	evidence := summarizeCapacityPushes(cfg, schedule)
	if err := writeCapacityPushEvidence(cfg.evidence, evidence); err != nil {
		t.Fatalf("write push evidence: %v", err)
	}
	t.Logf("push evidence: scheduled=%d offered=%d pushes=%d failures=%d dropped=%d missed=%d cancelled=%t drainTimedOut=%t retries=%d p95=%dms",
		evidence.Scheduled, evidence.Offered, evidence.Pushes, evidence.Failures, evidence.Dropped,
		evidence.MissedSchedules, evidence.Cancelled, evidence.DrainTimedOut, evidence.Retries, evidence.LatencyMs.P95)
	if evidence.Pushes == 0 || evidence.Scheduled != evidence.Offered || evidence.Pushes != evidence.Offered ||
		evidence.Failures != 0 || evidence.Dropped != 0 || evidence.MissedSchedules != 0 ||
		evidence.Cancelled || evidence.DrainTimedOut {
		t.Fatal("push schedule did not complete with zero failures, drops, missed offers and drain errors")
	}
}

// In-process coverage of the driver's scheduling and evidence; no Git service
// is needed.
func TestCapacityPushDriverScheduleBoundsWorkAndSummarizes(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	cfg := capacityPushConfig{duration: 300 * time.Millisecond, interval: 10 * time.Millisecond, workers: 2}
	schedule := runCapacityPushSchedule(t.Context(), cfg,
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
	if schedule.dropped == 0 {
		t.Fatal("offers faster than the workers can push must be dropped, not queued")
	}
	if schedule.offered != schedule.dropped+len(schedule.results) {
		t.Fatalf("offered %d != dropped %d + completed %d", schedule.offered, schedule.dropped, len(schedule.results))
	}
	if schedule.scheduled != 30 || schedule.offered > schedule.scheduled || schedule.cancelled || schedule.drainTimedOut {
		t.Fatalf("unexpected declared schedule: %+v", schedule)
	}

	cfg.repository, cfg.namespace, cfg.kind, cfg.runID = "gitstore-system", "acme", "category", "r1"
	evidence := summarizeCapacityPushes(cfg, schedule)
	if evidence.Pushes+evidence.Failures != len(schedule.results) || evidence.Pushes == 0 || evidence.Failures == 0 {
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
	for _, key := range []string{"repository", "pushes", "failures", "firstPushMs", "lastPushMs",
		"scheduled", "durationNs", "intervalNs", "missedSchedules", "offeredDurationNs",
		"drainDurationNs", "drainTimeoutNs", "drainTimedOut", "cancelled"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("evidence is missing verifier key %q", key)
		}
	}
}

func TestCapacityPushDriverCanceledScheduleOffersNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cfg := capacityPushConfig{duration: time.Second, interval: 50 * time.Millisecond, workers: 1}
	schedule := runCapacityPushSchedule(ctx, cfg,
		func(context.Context, int) capacityPushResult {
			return capacityPushResult{acknowledged: true, at: time.Now()}
		})
	if schedule.offered != 0 || schedule.dropped != 0 || len(schedule.results) != 0 || !schedule.cancelled || schedule.scheduled != 20 {
		t.Fatalf("canceled schedule still offered work or lost its declared plan: %+v", schedule)
	}
}

func TestCapacityPushDriverDeclaredScheduleKeepsExactDurations(t *testing.T) {
	for _, test := range []struct {
		duration, interval time.Duration
		scheduled          int
	}{
		{time.Second, 50 * time.Millisecond, 20},
		{time.Second + time.Nanosecond, 50 * time.Millisecond, 21},
		{100*time.Millisecond + time.Nanosecond, 50*time.Millisecond + time.Nanosecond, 2},
		{time.Millisecond, time.Second, 1},
		{2 * time.Hour, 50 * time.Millisecond, 144000},
	} {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		cfg := capacityPushConfig{duration: test.duration, interval: test.interval, workers: 1}
		schedule := runCapacityPushSchedule(ctx, cfg, func(context.Context, int) capacityPushResult {
			t.Error("a canceled declared schedule executed a push")
			return capacityPushResult{}
		})
		evidence := summarizeCapacityPushes(cfg, schedule)
		if evidence.Scheduled != test.scheduled || evidence.DurationNs != int64(test.duration) ||
			evidence.IntervalNs != int64(test.interval) || evidence.Offered != 0 || !evidence.Cancelled {
			t.Fatalf("declared schedule lost its exact duration or planned cardinality: %+v", evidence)
		}
	}
}

func TestCapacityPushDriverDrainCancelsAndCollectsWorkers(t *testing.T) {
	var active atomic.Int32
	cfg := capacityPushConfig{
		duration: 50 * time.Millisecond, interval: 50 * time.Millisecond,
		workers: 1, drainTimeout: 20 * time.Millisecond,
	}
	schedule := runCapacityPushSchedule(t.Context(), cfg, func(ctx context.Context, _ int) capacityPushResult {
		active.Add(1)
		defer active.Add(-1)
		<-ctx.Done()
		return capacityPushResult{}
	})
	evidence := summarizeCapacityPushes(cfg, schedule)
	if active.Load() != 0 || !evidence.DrainTimedOut || evidence.Cancelled ||
		evidence.DrainDurationNs < int64(cfg.drainTimeout) ||
		evidence.Scheduled != 1 || evidence.Offered != 1 || evidence.Failures != 1 {
		t.Fatalf("drain returned before worker cleanup or hid its timeout: %+v active=%d", evidence, active.Load())
	}
}

func TestCapacityPushDriverCancellationCollectsAcceptedWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var active atomic.Int32
	cfg := capacityPushConfig{duration: time.Second, interval: 50 * time.Millisecond, workers: 1}
	schedule := runCapacityPushSchedule(ctx, cfg, func(workCtx context.Context, _ int) capacityPushResult {
		active.Add(1)
		defer active.Add(-1)
		cancel()
		<-workCtx.Done()
		return capacityPushResult{}
	})
	evidence := summarizeCapacityPushes(cfg, schedule)
	if active.Load() != 0 || !evidence.Cancelled || evidence.DrainTimedOut ||
		evidence.Offered == 0 || evidence.Offered >= evidence.Scheduled ||
		evidence.Offered != evidence.Failures+evidence.Dropped {
		t.Fatalf("cancellation lost accepted work or the unfinished plan: %+v active=%d", evidence, active.Load())
	}
}

func TestCapacityPushDriverEvidenceRetainsMissedSchedule(t *testing.T) {
	cfg := capacityPushConfig{duration: time.Second, interval: 50 * time.Millisecond, workers: 1}
	schedule := capacityPushSchedule{
		scheduled: 20, offered: 20, missedSchedules: 1, offeredDuration: time.Second,
		results: []capacityPushResult{{acknowledged: true, at: time.Now()}},
	}
	evidence := summarizeCapacityPushes(cfg, schedule)
	if evidence.MissedSchedules != 1 || evidence.Scheduled != 20 || evidence.Offered != 20 {
		t.Fatalf("evidence discarded a late scheduled offer: %+v", evidence)
	}
}

type capacityPushPausedContext struct {
	context.Context
	once sync.Once
}

func (ctx *capacityPushPausedContext) Err() error {
	ctx.once.Do(func() { time.Sleep(75 * time.Millisecond) })
	return ctx.Context.Err()
}

func TestCapacityPushDriverObservesSchedulerPause(t *testing.T) {
	ctx := &capacityPushPausedContext{Context: t.Context()}
	cfg := capacityPushConfig{duration: 200 * time.Millisecond, interval: 50 * time.Millisecond, workers: 1}
	schedule := runCapacityPushSchedule(ctx, cfg, func(context.Context, int) capacityPushResult {
		return capacityPushResult{acknowledged: true, at: time.Now()}
	})
	evidence := summarizeCapacityPushes(cfg, schedule)
	if evidence.Scheduled != 4 || (evidence.MissedSchedules == 0 && evidence.Offered == evidence.Scheduled) {
		t.Fatalf("scheduler pause disappeared from declared offer accounting: %+v", evidence)
	}
	if evidence.Offered > 0 && evidence.MissedSchedules > 0 && evidence.LatencyMs.Max < 50 {
		t.Fatalf("acknowledgement latency excluded scheduler lateness: %+v", evidence)
	}
	if evidence.Offered != evidence.Pushes+evidence.Failures+evidence.Dropped {
		t.Fatalf("scheduler pause lost outcome accounting: %+v", evidence)
	}
}

func TestCapacityPushDriverConfigRejectsInexactDurationEvidence(t *testing.T) {
	t.Setenv("CAPACITY_PUSH_REMOTE", "http://localhost/acme/gitstore-system.git")
	t.Setenv("CAPACITY_PUSH_EVIDENCE", filepath.Join(t.TempDir(), "push-evidence.json"))
	t.Setenv("CAPACITY_PUSH_DURATION", "1s")
	t.Setenv("CAPACITY_PUSH_INTERVAL", "2580h")
	t.Setenv("CAPACITY_PUSH_WORKERS", "1")
	t.Setenv("CAPACITY_RUN_ID", "schedule-contract")
	_, err := loadCapacityPushConfig()
	if err == nil || !strings.Contains(err.Error(), "JSON precision") {
		t.Fatalf("unrepresentable nanosecond interval was accepted: %v", err)
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
