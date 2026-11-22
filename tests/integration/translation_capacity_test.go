// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

type translationCapacityPlan struct {
	RunID, Mode, SourceDigest, ConfigDigest, TopologyDigest string
	Start, End                                              time.Time
	APIs, Controllers                                       [2]string
	Git                                                     string
	Workers, ReadRate                                       int
	PushInterval                                            time.Duration
	Faults                                                  map[string]string
	Expected                                                map[int]translationCapacityContent
	ResourceLimits                                          map[string]translationCapacityResourceLimit
	ResourceInterval                                        time.Duration
	ResourceGaps                                            []translationCapacityResourceGap
}

type translationCapacityResourceLimit struct {
	CPUCores, MemoryBytes float64
}

type translationCapacityResourceGap struct {
	Replica, Fault string
	Start, End     time.Time
}

type translationCapacityResourceSample struct {
	At time.Time
	// CPUCores is consumed cores over the scrape interval ending at At;
	// RetainedBytes is post-GC live heap, not RSS or requested allocation.
	CPUCores, ResidentBytes, RetainedBytes, Goroutines float64
}

type translationCapacityResourceEpoch struct {
	Replica, Process string
	Start, End       time.Time
	Samples          []translationCapacityResourceSample
}

type translationCapacityContent struct {
	SourceDigest, BodyDigest, PatchDigest string
}

type translationCapacityDocument struct {
	Namespace, Name, Path string
	Source                []byte
	// Only generated trailing body filler may be resized for a new name.
	PaddingBytes int
	translationCapacityContent
}

type translationCapacityDataset struct {
	Targets   map[string]map[string][]byte
	Documents []translationCapacityDocument
}

func buildTranslationCapacityDataset(raw []byte, namespaces [10]string) (translationCapacityDataset, error) {
	var corpus struct {
		Namespace string
		Targets   map[string]string
		Valid     map[string]string
	}
	if err := json.Unmarshal(raw, &corpus); err != nil {
		return translationCapacityDataset{}, fmt.Errorf("translation capacity: invalid corpus")
	}
	seen := map[string]bool{}
	for _, namespace := range namespaces {
		if !translationIntegrationNamespace.MatchString(namespace) || !strings.HasPrefix(namespace, "translation-") || seen[namespace] {
			return translationCapacityDataset{}, fmt.Errorf("translation capacity: ten distinct owned namespaces required")
		}
		seen[namespace] = true
	}
	dataset := translationCapacityDataset{
		Targets: make(map[string]map[string][]byte, len(namespaces)),
	}
	for _, namespace := range namespaces {
		targets := make(map[string][]byte, 5)
		for _, kind := range []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"} {
			source, err := translationIntegrationSource([]byte(corpus.Targets[kind]), corpus.Namespace, namespace)
			if err != nil {
				return translationCapacityDataset{}, fmt.Errorf("translation capacity: missing or invalid target corpus")
			}
			targets["targets/"+kind+".md"] = source
		}
		dataset.Targets[namespace] = targets
	}
	digest := func(content []byte) string {
		value := sha256.Sum256(content)
		return hex.EncodeToString(value[:])
	}
	products := []string{"product-field", "product-body", "product-combined"}
	otherKinds := []string{"variant-field", "category-field", "collection-field", "file-body"}
	for index := range 1009 {
		namespace, template := namespaces[0], products[index%len(products)]
		if index >= 1000 {
			namespace, template = namespaces[index-999], otherKinds[(index-1000)%len(otherKinds)]
		} else if index == 999 {
			template = "product-body"
		}
		source, err := translationIntegrationSource([]byte(corpus.Valid[template]), corpus.Namespace, namespace)
		if err != nil {
			return translationCapacityDataset{}, fmt.Errorf("translation capacity: missing or invalid Translation corpus")
		}
		original, err := translationIntegrationExpected(source)
		if err != nil {
			return translationCapacityDataset{}, err
		}
		name, locale := fmt.Sprintf("translation-cap-%04d", index), "de-DE"
		if index%2 != 0 {
			locale = "fr-FR"
		}
		source = bytes.Replace(source, []byte("  name: "+original.Metadata.Name), []byte("  name: "+name), 1)
		source = bytes.Replace(source, []byte("  locale: "+original.Spec.Locale), []byte("  locale: "+locale), 1)
		if len(source) > 1<<20 {
			return translationCapacityDataset{}, fmt.Errorf("translation capacity: renamed source exceeds document budget")
		}
		padding := 0
		if index == 999 {
			padding = (1 << 20) - len(source)
			source = append(source, bytes.Repeat([]byte("x"), padding)...)
		}
		expected, err := translationIntegrationExpected(source)
		if err != nil || expected.Metadata.Name != name || expected.Spec.Locale != locale {
			return translationCapacityDataset{}, fmt.Errorf("translation capacity: source replacement failed")
		}
		patches := []byte("[]")
		if len(expected.Spec.Patches) > 0 {
			patches, err = json.Marshal(expected.Spec.Patches)
			if err != nil {
				return translationCapacityDataset{}, fmt.Errorf("translation capacity: invalid patch corpus")
			}
		}
		dataset.Documents = append(dataset.Documents, translationCapacityDocument{
			Namespace: namespace, Name: name, Path: "translations/" + name + ".md", Source: source, PaddingBytes: padding,
			translationCapacityContent: translationCapacityContent{
				SourceDigest: digest(source), BodyDigest: digest(expected.body), PatchDigest: digest(patches),
			},
		})
	}
	return dataset, nil
}

func renderTranslationCapacityPush(template translationCapacityDocument, runID string, sequence int) (translationCapacityDocument, error) {
	if runID == "" || len(runID) > 40 || sequence < 0 || sequence >= 1_000_000 {
		return translationCapacityDocument{}, fmt.Errorf("translation capacity: invalid bounded push identity")
	}
	for _, character := range runID {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '-' {
			return translationCapacityDocument{}, fmt.Errorf("translation capacity: unsafe push identity")
		}
	}
	name := fmt.Sprintf("cpd-%s-%06d", runID, sequence)
	source, padding := template.Source, template.PaddingBytes
	if padding < 0 || padding >= len(source) {
		return translationCapacityDocument{}, fmt.Errorf("translation capacity: invalid synthetic padding")
	}
	if padding > 0 {
		if len(source) != 1<<20 || bytes.Count(source[len(source)-padding:], []byte("x")) != padding {
			return translationCapacityDocument{}, fmt.Errorf("translation capacity: synthetic padding differs from generated body filler")
		}
		source = source[:len(source)-padding]
	}
	source = bytes.Replace(source, []byte("  name: "+template.Name), []byte("  name: "+name), 1)
	if padding > 0 {
		padding = (1 << 20) - len(source)
		if padding <= 0 {
			return translationCapacityDocument{}, fmt.Errorf("translation capacity: rendered envelope exceeds document budget")
		}
		source = append(source, bytes.Repeat([]byte("x"), padding)...)
	}
	expected, err := translationIntegrationExpected(source)
	if err != nil || expected.Metadata.Name != name || expected.Metadata.Namespace != template.Namespace ||
		!translationIntegrationNamespace.MatchString(template.Namespace) || !strings.HasPrefix(template.Namespace, "translation-") ||
		len(source) > 1<<20 {
		return translationCapacityDocument{}, fmt.Errorf("translation capacity: invalid rendered push source")
	}
	patches := []byte("[]")
	if len(expected.Spec.Patches) > 0 {
		patches, err = json.Marshal(expected.Spec.Patches)
		if err != nil {
			return translationCapacityDocument{}, fmt.Errorf("translation capacity: invalid rendered patches")
		}
	}
	return translationCapacityDocument{
		Namespace: template.Namespace, Name: name, Path: "translations/" + name + ".md", Source: source, PaddingBytes: padding,
		translationCapacityContent: translationCapacityContent{
			SourceDigest: fmt.Sprintf("%x", sha256.Sum256(source)),
			BodyDigest:   fmt.Sprintf("%x", sha256.Sum256(expected.body)), PatchDigest: fmt.Sprintf("%x", sha256.Sum256(patches)),
		},
	}, nil
}

func (worker *capacityPushWorker) pushTranslation(ctx context.Context, sequence int, document translationCapacityDocument) capacityPushResult {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	started := time.Now()
	if document.Namespace != worker.cfg.namespace {
		return capacityPushResult{sequence: sequence}
	}
	if _, err := commitTranslationIntegrationSources(ctx, worker, map[string][]byte{document.Path: document.Source}); err != nil {
		return capacityPushResult{sequence: sequence}
	}
	return worker.pushCommit(ctx, sequence, started)
}

func translationCapacityScheduledDocuments(dataset translationCapacityDataset, runID string, count int) ([]translationCapacityDocument, error) {
	if len(dataset.Documents) != 1009 || count < 10 || count > 5400 {
		return nil, fmt.Errorf("translation capacity: complete corpus and bounded ten-namespace schedule required")
	}
	documents := make([]translationCapacityDocument, count)
	for sequence := range documents {
		template := dataset.Documents[sequence%3]
		if scope := sequence % 10; scope > 0 {
			template = dataset.Documents[999+scope]
		} else if sequence%100 == 0 {
			template = dataset.Documents[999]
		}
		document, err := renderTranslationCapacityPush(template, runID, sequence)
		if err != nil {
			return nil, err
		}
		documents[sequence] = document
	}
	return documents, nil
}

type translationCapacityPushCallbacks struct {
	acknowledged func(capacityPushResult)
	observed     func(translationCapacityPush)
}

func runTranslationCapacityPushes(ctx context.Context, cfg capacityPushConfig, reader translationCapacityReader, watches map[string]*capacityPushWatch,
	workers []*capacityPushWorker, documents []translationCapacityDocument, keepWatches bool, callbacks *translationCapacityPushCallbacks) (int, int, []capacityPushResult, []translationCapacityPush, error) {
	if cfg.kind != "translation" || cfg.duration <= 0 || cfg.duration > 2*time.Hour || cfg.interval < 50*time.Millisecond ||
		cfg.workers < 1 || cfg.workers > 32 || len(watches) < 1 || len(watches) > 10 || len(workers) > len(watches)*cfg.workers {
		return 0, 0, nil, nil, fmt.Errorf("translation capacity: invalid finite push schedule")
	}
	planned := int((cfg.duration-1)/cfg.interval) + 1
	if len(documents) != planned {
		return 0, 0, nil, nil, fmt.Errorf("translation capacity: every offered push requires an independent authored document")
	}
	identities := make(map[string]bool, planned)
	scopes := make(map[string]bool, len(watches))
	for sequence, document := range documents {
		if (cfg.namespace != "" && document.Namespace != cfg.namespace) || watches[document.Namespace] == nil ||
			!translationIntegrationNamespace.MatchString(document.Namespace) || !strings.HasPrefix(document.Namespace, "translation-") ||
			document.Name != fmt.Sprintf("cpd-%s-%06d", cfg.runID, sequence) || identities[document.Name] {
			return 0, 0, nil, nil, fmt.Errorf("translation capacity: scheduled document scope/identity mismatch")
		}
		identities[document.Name] = true
		scopes[document.Namespace] = true
	}
	if len(scopes) != len(watches) {
		return 0, 0, nil, nil, fmt.Errorf("translation capacity: every watched namespace requires scheduled traffic")
	}
	free := make(map[string]chan *capacityPushWorker, len(scopes))
	for namespace := range scopes {
		free[namespace] = make(chan *capacityPushWorker, cfg.workers)
	}
	directories := make(map[string]bool, len(workers))
	for _, worker := range workers {
		if worker == nil || !scopes[worker.cfg.namespace] || worker.cfg.verifyAPIA != cfg.verifyAPIA ||
			worker.directory == "" || directories[worker.directory] {
			return 0, 0, nil, nil, fmt.Errorf("translation capacity: worker scope/oracle mismatch")
		}
		directories[worker.directory] = true
		pool := free[worker.cfg.namespace]
		if len(pool) == cap(pool) {
			return 0, 0, nil, nil, fmt.Errorf("translation capacity: namespace clone budget exceeded")
		}
		pool <- worker
	}
	for _, pool := range free {
		if len(pool) == 0 {
			return 0, 0, nil, nil, fmt.Errorf("translation capacity: scheduled namespace has no owned clone")
		}
	}
	var mu sync.Mutex
	receipts := make([]translationCapacityPush, 0, planned)
	oracle := startCapacityPushOracle(ctx, watches[documents[0].Namespace], func(ctx context.Context, result capacityPushResult) error {
		document := documents[result.sequence]
		observations, err := reader.converge(ctx, [2]string{cfg.verifyAPIA, cfg.verifyAPIB}, watches[document.Namespace], document, result)
		if err != nil {
			return err
		}
		receipt := translationCapacityPush{
			Sequence: result.sequence, OfferedAt: result.at.Add(-result.latency),
			AcknowledgedAt: result.at, Commit: result.commit, Documents: 1, Observations: observations,
		}
		if callbacks != nil && callbacks.observed != nil {
			callbacks.observed(receipt)
		}
		mu.Lock()
		receipts = append(receipts, receipt)
		mu.Unlock()
		return nil
	})
	started := cfg.startAt
	if started.IsZero() {
		started = time.Now()
	}
	offered, dropped, results := runCapacityPushScheduleAt(ctx, started, cfg.duration, cfg.interval, cfg.workers, func(ctx context.Context, sequence int) capacityPushResult {
		document := documents[sequence]
		pool := free[document.Namespace]
		worker := <-pool
		defer func() { pool <- worker }()
		result := worker.pushTranslation(ctx, sequence, document)
		if result.acknowledged {
			if callbacks != nil && callbacks.acknowledged != nil {
				callbacks.acknowledged(result)
			}
			oracle.offer(result)
		}
		return result
	})
	verified, failed, unverified := oracle.finish(keepWatches)
	var watchError error
	for _, watch := range watches {
		if !keepWatches {
			watch.close()
		}
		watch.mu.Lock()
		if watch.err != nil {
			watchError = watch.err
		}
		watch.mu.Unlock()
	}
	if watchError != nil {
		return offered, dropped, results, receipts, watchError
	}
	offers := make(map[int]time.Time, len(results))
	for _, result := range results {
		offers[result.sequence] = result.at.Add(-result.latency)
	}
	for index := range receipts {
		receipts[index].OfferedAt = offers[receipts[index].Sequence]
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Sequence < receipts[j].Sequence })
	if ctx.Err() != nil || time.Now().Before(started.Add(cfg.duration)) {
		return offered, dropped, results, receipts, fmt.Errorf("translation capacity: incomplete offered window")
	}
	if offered != planned || dropped != 0 || len(results) != planned || verified != planned || failed != 0 || unverified != 0 {
		return offered, dropped, results, receipts, fmt.Errorf("translation capacity: incomplete schedule: offered=%d/%d dropped=%d completed=%d verified=%d failed=%d unverified=%d",
			offered, planned, dropped, len(results), verified, failed, unverified)
	}
	return offered, dropped, results, receipts, nil
}

func TestTranslationNativeCapacityPushSchedule(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CAPACITY_DRIVER") != "1" {
		t.Skip("owned connected Translation push schedule not selected")
	}
	fixture, gitB := ownedTranslationNativeFixture(t)
	for _, api := range fixture.apis {
		require.NoError(t, verifyTranslationIntegrationEmptyNamespace(t, fixture, api))
	}
	var namespaces [10]string
	namespaces[0] = fixture.namespace
	for index := 1; index < len(namespaces); index++ {
		namespaces[index] = fmt.Sprintf("%s-unused-%d", fixture.namespace, index)
	}
	selected := []string{fixture.namespace}
	all := os.Getenv("TRANSLATION_TEST_CAPACITY_NAMESPACES")
	second := os.Getenv("TRANSLATION_TEST_CAPACITY_SECOND_NAMESPACE")
	if all != "" {
		require.Empty(t, second, "select either the complete namespace list or one second namespace")
		selected = strings.Split(all, ",")
		require.Len(t, selected, len(namespaces))
		require.Equal(t, fixture.namespace, selected[0])
		copy(namespaces[:], selected)
	} else if second != "" {
		namespaces[1] = second
		selected = append(selected, second)
	}
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	root := t.TempDir()
	cfg := capacityPushConfig{
		runID: "probe", kind: "translation", duration: time.Duration(max(3, len(selected))) * time.Second,
		interval: time.Second, workers: 2,
		verifyAPIA: fixture.apis[0], verifyAPIB: fixture.apis[1],
	}
	reader, credential, controllerToken := newTranslationCapacityReader(t, fixture)
	before := make(map[string][2]translationCapacityBaseline)
	targetsBefore := make(map[string][2]translationIntegrationTargetSample)
	watches := make(map[string]*capacityPushWatch)
	var workers []*capacityPushWorker
	var readTargets []translationCapacityReadTarget
	for scope, namespace := range selected {
		currentFixture := fixture
		currentFixture.namespace = namespace
		for _, api := range fixture.apis {
			require.NoError(t, verifyTranslationIntegrationEmptyNamespace(t, currentFixture, api))
		}
		current := cfg
		current.namespace = namespace
		current.remote = fixture.gitURL + "/" + namespace + "/gitstore-system"
		setup, err := newCapacityPushWorker(t.Context(), current, root, fixture.token, 32+scope)
		require.NoError(t, err)
		targets := dataset.Targets[namespace]
		targets["images/laptop.png"], err = translationIntegrationImage()
		require.NoError(t, err)
		_, err = commitTranslationIntegrationSources(t.Context(), setup, targets)
		require.NoError(t, err)
		require.True(t, setup.pushCommit(t.Context(), 0, time.Now()).acknowledged)
		controller := currentFixture
		controller.token, err = credential.current(t.Context())
		require.NoError(t, err)
		rbacNativeEventually(t, func() bool {
			for name, source := range targets {
				if strings.HasPrefix(name, "images/") {
					continue
				}
				for index, api := range fixture.apis {
					sample, found := readTranslationIntegrationTarget(t, currentFixture, controller, api, source)
					if !found {
						return false
					}
					key := namespace + "/" + name
					samples := targetsBefore[key]
					if index > 0 && !reflect.DeepEqual(samples[0], sample) {
						return false
					}
					samples[index] = sample
					targetsBefore[key] = samples
				}
			}
			return true
		}, "all targets must be observable through both replicas before scheduled pushes")
		seed := dataset.Documents[0]
		if scope > 0 {
			seed = dataset.Documents[999+scope]
		}
		_, err = commitTranslationIntegrationSources(t.Context(), setup, map[string][]byte{seed.Path: seed.Source})
		require.NoError(t, err)
		accepted := setup.pushCommit(t.Context(), 0, time.Now())
		require.True(t, accepted.acknowledged)
		revision := "main@sha1:" + accepted.commit
		awaitTranslationIntegrationResources(t, currentFixture, map[string][]byte{seed.Name: seed.Source}, revision, true)
		readTargets = append(readTargets, translationCapacityReadTarget{Document: seed, Revision: revision})
		samples := [2]translationCapacityBaseline{}
		for index, api := range fixture.apis {
			samples[index], err = reader.baseline(t.Context(), api, namespace, 10, 2)
			require.NoError(t, err)
		}
		before[namespace] = samples
		watch, err := newCapacityPushWatch(t.Context(), current, fixture.token)
		require.NoError(t, err)
		t.Cleanup(watch.close)
		watches[namespace] = watch
		for index := range cfg.workers {
			clone := current
			if index%2 != 0 {
				clone.remote = gitB + "/" + namespace + "/gitstore-system"
			}
			worker, err := newCapacityPushWorker(t.Context(), clone, root, fixture.token, scope*cfg.workers+index)
			require.NoError(t, err)
			workers = append(workers, worker)
		}
	}
	documents := make([]translationCapacityDocument, int(cfg.duration/cfg.interval))
	for index := range documents {
		template := dataset.Documents[index%3]
		if scope := index % len(selected); scope > 0 {
			template = dataset.Documents[999+scope]
		}
		documents[index], err = renderTranslationCapacityPush(template, cfg.runID, index)
		require.NoError(t, err)
	}
	cfg.startAt = time.Now().Add(100 * time.Millisecond)
	type readOutcome struct {
		result translationCapacityReadResults
		err    error
	}
	readDone := make(chan readOutcome, 1)
	loadContext, cancelLoad := context.WithCancel(t.Context())
	defer cancelLoad()
	go func() {
		result, err := reader.load(loadContext, [2]string{fixture.apis[0], fixture.apis[1]},
			cfg.startAt, cfg.duration, 20, 2, readTargets)
		readDone <- readOutcome{result: result, err: err}
	}()
	offered, dropped, results, receipts, err := runTranslationCapacityPushes(t.Context(), cfg, reader,
		watches, workers, documents, false, nil)
	readResult := <-readDone
	require.NoError(t, err, "diagnostic probe is not a full sustained acceptance window")
	require.NoError(t, readResult.err)
	require.Equal(t, len(documents), offered)
	require.Zero(t, dropped)
	require.Len(t, results, offered)
	require.Len(t, receipts, offered)
	for _, receipt := range receipts {
		require.Len(t, receipt.Observations, 2)
		require.False(t, receipt.AcknowledgedAt.Before(receipt.OfferedAt))
	}
	reads := readResult.result
	require.Equal(t, int(cfg.duration/time.Second)*20, reads.Offered, "reads and pushes must cover the same complete offered window")
	require.Zero(t, reads.Dropped)
	require.Zero(t, reads.UnexpectedErrors)
	require.Equal(t, reads.Offered, reads.Completed)
	require.Len(t, reads.Latencies, reads.Offered)
	require.NoError(t, waitSecretCapacityUntil(t.Context(), credential.expires.Add(-14*time.Second)))
	renewed, err := credential.current(t.Context())
	require.NoError(t, err)
	require.True(t, renewed != controllerToken, "normal renewal must replace the initial short-lived credential")
	_, err = reader.observe(t.Context(), fixture.apis[1], readTargets[0].Document, readTargets[0].Revision)
	require.NoError(t, err, "renewed enrolled authority must authenticate a real current-source snapshot")
	for namespace, samples := range before {
		currentFixture, controller := fixture, fixture
		currentFixture.namespace, controller.namespace = namespace, namespace
		controller.token, err = credential.current(t.Context())
		require.NoError(t, err)
		for index, api := range fixture.apis {
			after, err := reader.baseline(t.Context(), api, namespace, 10, 2)
			require.NoError(t, err)
			require.Equal(t, samples[index], after, "scheduled Translation pushes cannot change Product material or aggregates")
			for name, source := range dataset.Targets[namespace] {
				if strings.HasPrefix(name, "images/") {
					continue
				}
				target, found := readTranslationIntegrationTarget(t, currentFixture, controller, api, source)
				require.True(t, found)
				require.Equal(t, targetsBefore[namespace+"/"+name][index], target,
					"seeded and scheduled Translations cannot change any target kind")
			}
		}
	}
}

func newTranslationCapacityReader(t *testing.T, fixture rbacNativeFixture) (translationCapacityReader, *translationCapacityControllerCredential, string) {
	t.Helper()
	key, err := newSecretCapacityOwnedKey()
	require.NoError(t, err)
	controllerName := getEnv("TRANSLATION_TEST_CONTROLLER_SERVICEACCOUNT", "gitstore-controller-manager")
	controllerUID := secretTestRotate(t, fixture.apis[0], fixture.token, controllerName, []secretCapacityOwnedKey{key}, nil)
	t.Cleanup(func() {
		secretTestRotate(t, fixture.apis[0], fixture.token, controllerName, nil, []string{key.ID})
	})
	credential := &translationCapacityControllerCredential{
		client: &http.Client{Timeout: 5 * time.Second}, api: fixture.apis[0], key: key,
		fixture: secretCapacityOwnedFixture{Name: controllerName, UID: controllerUID},
	}
	controllerToken, err := credential.current(t.Context())
	require.NoError(t, err)
	reader := translationCapacityReader{
		client: &http.Client{Timeout: 5 * time.Second}, token: fixture.token, controllerToken: controllerToken,
		controllerCredential: credential.current,
		query:                translationIntegrationQuery(t, "translation"), snapshot: translationIntegrationQuery(t, "snapshot"),
		baselineQuery: translationIntegrationQuery(t, "baseline"),
	}
	return reader, credential, controllerToken
}

type translationCapacityReadCase struct {
	Query     string          `json:"query"`
	Variables map[string]any  `json:"variables"`
	Expected  json.RawMessage `json:"expected"`
}

type translationCapacityLoadBarrier struct {
	RunID   string    `json:"runId"`
	StartAt time.Time `json:"startAt"`
}

type translationCapacityConnectedConfig struct {
	Mode               string
	Duration, Interval time.Duration
	Workers            int
}

type translationCapacityRejectionObservation struct {
	Replica                       string
	InvalidAbsent, SentinelAbsent bool
	CheckedAt                     time.Time
}

type translationCapacityRejection struct {
	Case, Reason, Namespace, InvalidName, SentinelName string
	Commit, Parent, BeforeReferences, AfterReferences  string
	SourceDigest, SentinelDigest                       string
	OfferedAt, CompletedAt                             time.Time
	Observations                                       []translationCapacityRejectionObservation
	FinalObservations                                  []translationCapacityRejectionObservation
}

func parseTranslationCapacityConnectedConfig(mode, duration, interval, workers string) (translationCapacityConnectedConfig, error) {
	config := translationCapacityConnectedConfig{Mode: mode, Interval: time.Second, Workers: 2}
	minimum := 10 * time.Second
	switch mode {
	case "diagnostic":
		if duration == "" {
			duration = "10s"
		}
	case "alpha":
		minimum, config.Interval = 30*time.Minute, 6*time.Second
	default:
		return config, fmt.Errorf("translation capacity: connected scheduling supports diagnostic or alpha only; production remains unavailable")
	}
	var err error
	config.Duration, err = time.ParseDuration(duration)
	if err != nil || config.Duration < minimum || config.Duration > 90*time.Minute {
		return config, fmt.Errorf("translation capacity: %s duration must be within [%s, 90m] to retain setup/recovery margin under the two-hour deadline", mode, minimum)
	}
	if interval != "" {
		requested, err := time.ParseDuration(interval)
		if err != nil || requested != config.Interval {
			return config, fmt.Errorf("translation capacity: %s requires exactly %s between candidate pushes; CAPACITY_PUSH_INTERVAL cannot override its rate", mode, config.Interval)
		}
	}
	if workers != "" {
		config.Workers, err = strconv.Atoi(workers)
		if err != nil {
			return config, fmt.Errorf("translation capacity: invalid CAPACITY_PUSH_WORKERS")
		}
	}
	if config.Workers < 1 || config.Workers > 2 {
		return config, fmt.Errorf("translation capacity: CAPACITY_PUSH_WORKERS must be within [1, 2]")
	}
	return config, nil
}

func (config translationCapacityConnectedConfig) scheduledOffers() int {
	return int((config.Duration-1)/config.Interval) + 1
}

func (config translationCapacityConnectedConfig) receipt(start, finished time.Time, offered, dropped, completed, verified int) (map[string]any, error) {
	_, err := parseTranslationCapacityConnectedConfig(config.Mode, config.Duration.String(), config.Interval.String(), strconv.Itoa(config.Workers))
	if err != nil {
		return nil, err
	}
	planned := config.scheduledOffers()
	if start.IsZero() || finished.Before(start.Add(config.Duration)) ||
		offered != planned || dropped != 0 || completed != planned || verified != planned {
		return nil, fmt.Errorf("translation capacity: incomplete connected scheduling receipt")
	}
	return map[string]any{
		"mode": config.Mode, "evidenceClass": config.Mode,
		"start": start, "end": start.Add(config.Duration), "intervalNanos": config.Interval.Nanoseconds(),
		"completedAt": finished,
		"workers":     config.Workers, "scheduledOffers": planned,
		"offered": offered, "dropped": dropped, "completed": completed, "verified": verified,
	}, nil
}

func readTranslationCapacityLoadBarrier(raw []byte, runID string, now time.Time) (time.Time, error) {
	var barrier translationCapacityLoadBarrier
	if json.Unmarshal(raw, &barrier) != nil || runID == "" || barrier.RunID != runID ||
		!barrier.StartAt.After(now) || barrier.StartAt.After(now.Add(10*time.Minute)) {
		return time.Time{}, fmt.Errorf("translation capacity: stale, missed or unbounded load barrier")
	}
	return barrier.StartAt, nil
}

func readTranslationCapacityPreparationBarrier(raw []byte, runID string, now time.Time) (time.Time, time.Time, error) {
	start, err := readTranslationCapacityLoadBarrier(raw, runID, now)
	var preparation struct {
		WarmupStartAt time.Time
	}
	if err != nil || json.Unmarshal(raw, &preparation) != nil ||
		!preparation.WarmupStartAt.After(now) || !start.Equal(preparation.WarmupStartAt.Add(30*time.Second)) {
		return time.Time{}, time.Time{}, fmt.Errorf("translation capacity: stale, missed or mismatched preparation barrier")
	}
	return start, preparation.WarmupStartAt, nil
}

func TestTranslationNativeCapacityConnectedPushes(t *testing.T) {
	if os.Getenv("CAPACITY_TRANSLATION_PUSHES") != "1" {
		t.Skip("public connected Translation push companion not selected")
	}
	schedule, err := parseTranslationCapacityConnectedConfig(os.Getenv("MODE"), os.Getenv("CAPACITY_DURATION"),
		os.Getenv("CAPACITY_PUSH_INTERVAL"), os.Getenv("CAPACITY_PUSH_WORKERS"))
	require.NoError(t, err)
	fixture, gitB := ownedTranslationNativeFixture(t)
	evidence := os.Getenv("CAPACITY_EVIDENCE_DIR")
	require.NotEmpty(t, evidence)
	var metadata struct {
		RunID, SourceStateSha256, ConfigDigest, EnvironmentDigest string
		GitRevision                                               string
	}
	raw, err := os.ReadFile(filepath.Join(evidence, "metadata.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &metadata))
	require.True(t, translationCapacityHex(metadata.SourceStateSha256, 64))
	require.True(t, translationCapacityHex(metadata.GitRevision, 40), "connected receipts require the verifier revision as well as its working-tree digest")
	fixturePath := os.Getenv("CAPACITY_TRANSLATION_FIXTURE_FILE")
	info, err := os.Stat(fixturePath)
	require.NoError(t, err)
	require.Zero(t, info.Mode().Perm()&0o077)
	require.LessOrEqual(t, info.Size(), int64(16<<20))
	raw, err = os.ReadFile(fixturePath)
	require.NoError(t, err)
	fixtureDigest := fmt.Sprintf("%x", sha256.Sum256(raw))
	var captured struct {
		RunID, Scope, CorpusSha256 string
		VerifiedDocuments          int
		References                 map[string]string
		Cases                      []translationCapacityReadCase
	}
	require.NoError(t, json.Unmarshal(raw, &captured))
	require.Equal(t, metadata.RunID, captured.RunID)
	require.Equal(t, "verified-retained-source-cases", captured.Scope)
	require.Equal(t, 1009, captured.VerifiedDocuments)
	require.Len(t, captured.Cases, 10)
	require.Len(t, captured.References, 10)
	var namespaces [10]string
	for index, sample := range captured.Cases {
		var data struct {
			Translation translationIntegrationResource
		}
		require.NoError(t, json.Unmarshal(sample.Expected, &data))
		namespaces[index] = data.Translation.Metadata.Namespace
	}
	require.Equal(t, fixture.namespace, namespaces[0])
	raw, err = os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(raw)), captured.CorpusSha256)
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	cfg := capacityPushConfig{
		runID: metadata.RunID, kind: "translation", duration: schedule.Duration, interval: schedule.Interval, workers: schedule.Workers,
		verifyAPIA: fixture.apis[0], verifyAPIB: fixture.apis[1],
	}
	warmupConfig := cfg
	warmupConfig.runID = fmt.Sprintf("warmup-%x", sha256.Sum256([]byte(cfg.runID)))[:39]
	warmupConfig.duration, warmupConfig.interval = 20*time.Second, time.Second
	_, err = renderTranslationCapacityPush(dataset.Documents[0], cfg.runID, 0)
	require.NoError(t, err)
	reader, credential, _ := newTranslationCapacityReader(t, fixture)
	reader.baselinePause = 50 * time.Millisecond
	captureMaterial := func() (string, int64) {
		material := make(map[string]any)
		var products int64
		for _, namespace := range namespaces {
			current, controller := fixture, fixture
			current.namespace, controller.namespace = namespace, namespace
			controller.token, err = credential.current(t.Context())
			require.NoError(t, err)
			for replica, api := range fixture.apis {
				baseline, err := reader.baseline(t.Context(), api, namespace, 10, 100_000)
				require.NoError(t, err)
				material[fmt.Sprintf("%s/%d/products", namespace, replica)] = baseline
				if replica == 0 {
					products += baseline.Products
				}
				for name, source := range dataset.Targets[namespace] {
					require.NoError(t, waitSecretCapacityUntil(t.Context(), time.Now().Add(25*time.Millisecond)))
					controller.token, err = credential.current(t.Context())
					require.NoError(t, err)
					target, found := readTranslationIntegrationTarget(t, current, controller, api, source)
					require.True(t, found)
					material[fmt.Sprintf("%s/%d/%s", namespace, replica, name)] = target
				}
			}
		}
		serialized, err := json.Marshal(material)
		require.NoError(t, err)
		return fmt.Sprintf("%x", sha256.Sum256(serialized)), products
	}
	before, products := captureMaterial()
	root := t.TempDir()
	watches := make(map[string]*capacityPushWatch)
	var workers []*capacityPushWorker
	originalHeads := make(map[string]string)
	for scope, namespace := range namespaces {
		current := cfg
		current.namespace = namespace
		current.remote = fixture.gitURL + "/" + namespace + "/gitstore-system"
		references, err := readTranslationCapacityReferences(t.Context(), current.remote, fixture.token)
		require.NoError(t, err)
		require.Equal(t, captured.References[namespace], references, "retained source preparation cannot be reused after canonical refs change")
		template := dataset.Documents[0]
		if scope > 0 {
			template = dataset.Documents[999+scope]
		}
		var source struct {
			Translation translationIntegrationResource
		}
		require.NoError(t, json.Unmarshal(captured.Cases[scope].Expected, &source))
		for _, api := range fixture.apis {
			observation, err := reader.observe(t.Context(), api, template, source.Translation.Metadata.Revision)
			require.NoError(t, err)
			require.Equal(t, source.Translation.Metadata.UID, observation.UID)
			require.Equal(t, source.Translation.Metadata.ResourceVersion, observation.Version)
		}
		preparation := warmupConfig
		preparation.namespace = namespace
		watch, err := newCapacityPushWatch(t.Context(), current, fixture.token, preparation)
		require.NoError(t, err)
		t.Cleanup(watch.close)
		watches[namespace] = watch
		for index := range cfg.workers {
			clone := current
			if index%2 != 0 {
				clone.remote = gitB + "/" + namespace + "/gitstore-system"
			}
			worker, err := newCapacityPushWorker(t.Context(), clone, root, fixture.token, scope*cfg.workers+index)
			require.NoError(t, err)
			workers = append(workers, worker)
			if index == 0 {
				head, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "HEAD")
				require.NoError(t, err)
				originalHeads[namespace] = strings.TrimSpace(string(head))
			}
		}
	}
	documents, err := translationCapacityScheduledDocuments(dataset, cfg.runID, schedule.scheduledOffers())
	require.NoError(t, err)
	expected := make(map[int]translationCapacityContent, len(documents))
	sourceSizes := make(map[int]int, len(documents))
	for sequence, document := range documents {
		expected[sequence] = documents[sequence].translationCapacityContent
		sourceSizes[sequence] = len(document.Source)
	}
	var rejectionCorpus struct {
		Namespace           string
		Valid               map[string]string
		Negative            []translationIntegrationNegative
		NegativeGitMessages map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &rejectionCorpus))
	negativeSources := make(map[string][]byte, len(rejectionCorpus.Valid))
	for name, source := range rejectionCorpus.Valid {
		negativeSources["translations/"+name+".md"], err = translationIntegrationSource(
			[]byte(source), rejectionCorpus.Namespace, fixture.namespace)
		require.NoError(t, err)
	}
	var negatives []translationIntegrationNegative
	for _, negative := range rejectionCorpus.Negative {
		if negative.Name == "locale-underscore" {
			negatives = append(negatives, negative)
		}
	}
	require.Len(t, negatives, 1)
	rejections := testTranslationNativeRejections(t, fixture, negativeSources, negatives,
		rejectionCorpus.NegativeGitMessages, cfg.runID)
	require.Len(t, rejections, 1)
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(evidence, "translation-ready.json"),
		map[string]any{
			"runId": cfg.runID, "sourceStateSha256": metadata.SourceStateSha256, "fixtureSha256": fixtureDigest,
			"sourceRevision": metadata.GitRevision,
			"mode":           schedule.Mode, "evidenceClass": schedule.Mode, "durationNanos": cfg.duration.Nanoseconds(),
			"intervalNanos": cfg.interval.Nanoseconds(), "workers": cfg.workers, "scheduledOffers": schedule.scheduledOffers(),
			"warmupSeconds": 30, "cooldownSeconds": 15, "warmupPushes": 20,
		}))
	barrierPath := filepath.Join(evidence, "translation-load-start.json")
	var warmupStart time.Time
	deadline := time.Now().Add(10 * time.Minute)
	for {
		raw, err = os.ReadFile(barrierPath)
		if err == nil {
			cfg.startAt, warmupStart, err = readTranslationCapacityPreparationBarrier(raw, cfg.runID, time.Now())
			require.NoError(t, err)
			break
		}
		require.True(t, os.IsNotExist(err))
		require.True(t, time.Now().Before(deadline), "public runner did not release its bounded load barrier")
		require.NoError(t, waitSecretCapacityUntil(t.Context(), time.Now().Add(20*time.Millisecond)))
	}
	warmupConfig.startAt = warmupStart
	warmupDocuments, err := translationCapacityScheduledDocuments(dataset, warmupConfig.runID, 20)
	require.NoError(t, err)
	warmupExpected := make(map[int]translationCapacityContent, len(warmupDocuments))
	for sequence := range warmupDocuments {
		warmupExpected[sequence] = warmupDocuments[sequence].translationCapacityContent
	}
	warmupOffered, warmupDropped, warmupResults, warmupPushes, err := runTranslationCapacityPushes(
		t.Context(), warmupConfig, reader, watches, workers, warmupDocuments, true, nil)
	require.NoError(t, err)
	warmupSchedule := translationCapacityConnectedConfig{
		Mode: "diagnostic", Duration: warmupConfig.duration, Interval: warmupConfig.interval, Workers: cfg.workers,
	}
	preparation, err := warmupSchedule.receipt(warmupStart, time.Now(), warmupOffered, warmupDropped,
		len(warmupResults), len(warmupPushes))
	require.NoError(t, err)
	preparation["documentRunId"], preparation["pushes"], preparation["expected"] = warmupConfig.runID, warmupPushes, warmupExpected
	preparation["warmupSeconds"], preparation["cooldownSeconds"] = 30, 15
	require.True(t, time.Now().Before(cfg.startAt), "warmup must complete before the entire measured window")
	var callbacks *translationCapacityPushCallbacks
	var callbackFault *translationCapacityCallbackFault
	if stage := os.Getenv("CAPACITY_TRANSLATION_CALLBACK_STAGE"); stage != "" {
		var config struct {
			Services struct{ TranslationFaults map[string]string }
		}
		raw, err := os.ReadFile(filepath.Join(evidence, "config.json"))
		require.NoError(t, err)
		require.Equal(t, metadata.ConfigDigest, fmt.Sprintf("%x", sha256.Sum256(raw)))
		require.NoError(t, json.Unmarshal(raw, &config))
		require.Equal(t, map[string]string{stage: "simulated"}, config.Services.TranslationFaults)
		callbackFault, err = newTranslationCapacityCallbackFault(t.Context(), cfg, reader, documents[0], stage,
			fixture.gitURL+"/"+documents[0].Namespace+"/gitstore-system")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, callbackFault.clear()) })
		callbacks = &callbackFault.callbacks
	}
	offered, dropped, results, pushes, err := runTranslationCapacityPushes(t.Context(), cfg, reader, watches, workers, documents, true, callbacks)
	require.NoError(t, err)
	var recovered map[string]any
	if callbackFault != nil {
		recovered, err = callbackFault.finish(t.Context())
		require.NoError(t, err, "declared callback recovery must finish within measured domain traffic")
	}
	after, productsAfter := captureMaterial()
	require.Equal(t, before, after, "scheduled Translations cannot modify target material or the retained Product baseline")
	require.Equal(t, products, productsAfter)
	for index, namespace := range namespaces {
		worker := workers[index*cfg.workers]
		_, err := secretCapacityGit(t.Context(), worker.directory, fixture.token, "fetch", "--quiet", "origin", "main")
		require.NoError(t, err)
		_, err = secretCapacityGit(t.Context(), worker.directory, "", "merge-base", "--is-ancestor", originalHeads[namespace], "FETCH_HEAD")
		require.NoError(t, err, "every original canonical history must remain reachable after scheduled pushes")
	}
	require.NoError(t, waitSecretCapacityUntil(t.Context(), cfg.startAt.Add(cfg.duration+15*time.Second)))
	for index := range rejections {
		rejection := &rejections[index]
		current := fixture
		current.namespace = rejection.Namespace
		for _, api := range fixture.apis {
			_, invalidFound := readTranslationIntegrationResource(t, current, api, rejection.InvalidName)
			_, sentinelFound := readTranslationIntegrationResource(t, current, api, rejection.SentinelName)
			require.False(t, invalidFound, "rejected source must remain absent after measured traffic and cooldown")
			require.False(t, sentinelFound, "a rejected candidate cannot publish its valid sentinel later")
			rejection.FinalObservations = append(rejection.FinalObservations, translationCapacityRejectionObservation{
				Replica: api, InvalidAbsent: !invalidFound, SentinelAbsent: !sentinelFound, CheckedAt: time.Now().UTC(),
			})
		}
	}
	receipt, err := schedule.receipt(cfg.startAt, time.Now(), offered, dropped, len(results), len(pushes))
	require.NoError(t, err)
	for field, value := range map[string]any{
		"schemaVersion": 1, "scope": "connected-translation",
		"runId": cfg.runID, "sourceStateSha256": metadata.SourceStateSha256, "fixtureSha256": fixtureDigest,
		"sourceRevision": metadata.GitRevision,
		"configDigest":   metadata.ConfigDigest, "environmentDigest": metadata.EnvironmentDigest,
		"expected": expected, "sourceSizes": sourceSizes, "pushes": pushes, "materialBefore": before, "materialAfter": after,
		"baselineProducts": products, "verifiedRetainedDocuments": captured.VerifiedDocuments,
		"originalHeads": originalHeads,
		"preparation":   preparation,
		"expectedRejectionTraffic": map[string]any{
			"phase": "preparation", "offered": len(negatives), "completed": len(rejections),
			"rejected": len(rejections), "pushes": rejections,
		},
	} {
		receipt[field] = value
	}
	if recovered != nil {
		receipt["faults"], receipt["faultWindows"] = []map[string]any{recovered}, []map[string]any{callbackFault.window}
	}
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(evidence, "translation-connected.json"), receipt))
}

func TestTranslationNativeCapacityReadFixture(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CAPACITY_READ_FIXTURE") != "1" {
		t.Skip("owned retained Translation read-fixture capture not selected")
	}
	fixture, _ := ownedTranslationNativeFixture(t)
	destination, runID := os.Getenv("CAPACITY_TRANSLATION_FIXTURE_FILE"), os.Getenv("CAPACITY_RUN_ID")
	require.NotEmpty(t, destination)
	_, err := os.Lstat(destination)
	require.True(t, os.IsNotExist(err), "retain existing source fixtures; select a fresh private destination")
	names := strings.Split(os.Getenv("TRANSLATION_TEST_CAPACITY_NAMESPACES"), ",")
	require.Len(t, names, 10)
	require.Equal(t, fixture.namespace, names[0])
	var namespaces [10]string
	copy(namespaces[:], names)
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	_, err = renderTranslationCapacityPush(dataset.Documents[0], runID, 0)
	require.NoError(t, err)
	reader, credential, _ := newTranslationCapacityReader(t, fixture)
	root := t.TempDir()
	references := make(map[string]string, len(names))
	baselines := make(map[string][2]translationCapacityBaseline, len(names))
	targets := make(map[string][2]translationIntegrationTargetSample)
	cases := make([]translationCapacityReadCase, 0, len(names))
	verified := 0
	for index, namespace := range namespaces {
		currentFixture, controller := fixture, fixture
		currentFixture.namespace, controller.namespace = namespace, namespace
		controller.token, err = credential.current(t.Context())
		require.NoError(t, err)
		var baseline [2]translationCapacityBaseline
		for replica, api := range fixture.apis {
			baseline[replica], err = reader.baseline(t.Context(), api, namespace, 10, 2)
			require.NoError(t, err)
			for name, source := range dataset.Targets[namespace] {
				if strings.HasPrefix(name, "images/") {
					continue
				}
				sample, found := readTranslationIntegrationTarget(t, currentFixture, controller, api, source)
				require.True(t, found, "every retained target must remain present")
				key := namespace + "/" + name
				samples := targets[key]
				samples[replica] = sample
				targets[key] = samples
			}
		}
		require.Equal(t, baseline[0], baseline[1], "complete Product material and aggregates must agree through both APIs")
		baselines[namespace] = baseline
		remote := fixture.gitURL + "/" + namespace + "/gitstore-system"
		references[namespace], err = readTranslationCapacityReferences(t.Context(), remote, fixture.token)
		require.NoError(t, err)
		worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote, namespace: namespace}, root, fixture.token, index)
		require.NoError(t, err)
		captured := false
		for _, document := range dataset.Documents {
			if document.Namespace != namespace {
				continue
			}
			revision, err := readTranslationCapacityRetainedDocument(t.Context(), worker, document)
			require.NoError(t, err)
			var previous translationCapacityObservation
			var expected json.RawMessage
			var variables map[string]any
			for replica, api := range fixture.apis {
				observation, err := reader.observe(t.Context(), api, document, revision)
				require.NoError(t, err, "every retained source must join independent Git bytes and current controller evidence")
				if replica > 0 {
					require.Equal(t, previous.UID, observation.UID)
					require.Equal(t, previous.Version, observation.Version)
				}
				previous = observation
				if captured {
					continue
				}
				variables = map[string]any{"by": map[string]any{"namespacePath": map[string]any{
					"namespace": namespace, "name": document.Name,
				}}}
				response := fixture.queryBounded(t, api, reader.query, variables, 8<<20)
				require.Empty(t, response.Errors)
				var data struct {
					Translation *translationIntegrationResource
				}
				require.NoError(t, json.Unmarshal(response.Data, &data))
				require.NotNil(t, data.Translation)
				require.NoError(t, verifyTranslationIntegrationResource(document.Source, revision, *data.Translation, true))
				require.Equal(t, observation.UID, data.Translation.Metadata.UID)
				require.Equal(t, observation.Version, data.Translation.Metadata.ResourceVersion)
				if replica > 0 {
					var before, after any
					require.NoError(t, json.Unmarshal(expected, &before))
					require.NoError(t, json.Unmarshal(response.Data, &after))
					require.Equal(t, before, after, "the public read fixture must agree through both replicas")
				}
				expected = append(json.RawMessage(nil), response.Data...)
			}
			if !captured {
				cases = append(cases, translationCapacityReadCase{Query: reader.query, Variables: variables, Expected: expected})
				captured = true
			}
			verified++
		}
		require.True(t, captured)
	}
	require.Equal(t, len(dataset.Documents), verified)
	require.Len(t, cases, len(names))
	for namespace, before := range references {
		after, err := readTranslationCapacityReferences(t.Context(), fixture.gitURL+"/"+namespace+"/gitstore-system", fixture.token)
		require.NoError(t, err)
		require.Equal(t, before, after, "read-only fixture capture cannot reuse moving canonical refs/history")
		currentFixture, controller := fixture, fixture
		currentFixture.namespace, controller.namespace = namespace, namespace
		controller.token, err = credential.current(t.Context())
		require.NoError(t, err)
		for replica, api := range fixture.apis {
			baseline, err := reader.baseline(t.Context(), api, namespace, 10, 2)
			require.NoError(t, err)
			require.Equal(t, baselines[namespace][replica], baseline, "retained Product material and aggregates must remain unchanged")
			for name, source := range dataset.Targets[namespace] {
				if strings.HasPrefix(name, "images/") {
					continue
				}
				sample, found := readTranslationIntegrationTarget(t, currentFixture, controller, api, source)
				require.True(t, found)
				require.Equal(t, targets[namespace+"/"+name][replica], sample, "retained targets must remain unchanged")
				require.Equal(t, targets[namespace+"/"+name][0], sample, "retained targets must agree through both APIs")
			}
		}
	}
	require.NoError(t, writeSecretCapacityPrivateJSON(destination, struct {
		SchemaVersion int                                              `json:"schemaVersion"`
		RunID         string                                           `json:"runId"`
		Scope         string                                           `json:"scope"`
		CorpusDigest  string                                           `json:"corpusSha256"`
		Documents     int                                              `json:"verifiedDocuments"`
		References    map[string]string                                `json:"references"`
		Cases         []translationCapacityReadCase                    `json:"cases"`
		Baselines     map[string][2]translationCapacityBaseline        `json:"baselines"`
		Targets       map[string][2]translationIntegrationTargetSample `json:"targets"`
	}{
		SchemaVersion: 1, RunID: runID, Scope: "verified-retained-source-cases",
		CorpusDigest: fmt.Sprintf("%x", sha256.Sum256(raw)), Documents: verified, References: references, Cases: cases,
		Baselines: baselines, Targets: targets,
	}))
	t.Logf("Verified %d retained authored sources on both APIs and captured %d protected read cases without source resubmission", verified, len(cases))
}

func TestTranslationNativeCapacityDataset(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CAPACITY_DATASET") != "1" {
		t.Skip("owned connected Translation capacity dataset not selected")
	}
	fixture, gitB := ownedTranslationNativeFixture(t)
	names := strings.Split(os.Getenv("TRANSLATION_TEST_CAPACITY_NAMESPACES"), ",")
	require.Len(t, names, 10, "prepare ten fresh owned namespaces and certified target coverage")
	var namespaces [10]string
	copy(namespaces[:], names)
	require.Equal(t, fixture.namespace, namespaces[0])
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	controller := fixture
	controller.token = rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_CONTROLLER_TOKEN_FILE"))
	imageBytes, err := translationIntegrationImage()
	require.NoError(t, err)
	root := t.TempDir()
	before := make(map[string]translationIntegrationTargetSample)
	verified := 0
	for index, namespace := range namespaces {
		passed := t.Run(namespace, func(t *testing.T) {
			current := fixture
			current.namespace = namespace
			for _, api := range current.apis {
				require.NoError(t, verifyTranslationIntegrationEmptyNamespace(t, current, api))
			}
			worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{
				remote: current.gitURL + "/" + namespace + "/gitstore-system",
			}, root, current.token, index)
			require.NoError(t, err)
			git := rbacNativeGit{fixture: current, dir: worker.directory}
			format, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "--show-object-format")
			require.NoError(t, err)
			targets := make(map[string][]byte, 6)
			for name, source := range dataset.Targets[namespace] {
				requireTranslationIntegrationTargetAbsent(t, current, source)
				targets[name] = source
			}
			targets["images/laptop.png"] = imageBytes
			pushTranslationIntegrationSources(t, git, worker, strings.TrimSpace(string(format)), targets)
			rbacNativeEventually(t, func() bool {
				for name, source := range dataset.Targets[namespace] {
					for i, api := range current.apis {
						sample, found := readTranslationIntegrationTarget(t, current, controller, api, source)
						if !found {
							return false
						}
						key := namespace + "/" + name
						if i == 0 {
							before[key] = sample
						} else if !reflect.DeepEqual(sample, before[key]) {
							return false
						}
					}
				}
				return true
			}, "both APIs must agree on original target material before Translation seeding")
			var watches []*translationIntegrationWatch
			for _, api := range current.apis {
				cursor := capacityBootstrapCursor(t, api, current.token)
				connection, err := dialCapacitySubscription(api, current.token, "translation-integration",
					translationIntegrationQuery(t, "watch"), map[string]any{"namespace": namespace, "cursor": cursor}, 10*time.Second)
				require.NoError(t, err)
				watch := &translationIntegrationWatch{connection: connection, cursor: cursor}
				t.Cleanup(func() { _ = watch.connection.Close() })
				watches = append(watches, watch)
			}
			paths, sources := map[string][]byte{}, map[string][]byte{}
			push := func() {
				if len(paths) == 0 {
					return
				}
				ingress := current.gitURL
				if verified%40 >= 20 {
					ingress = gitB
				}
				_, err := git.command(t, "remote", "set-url", "origin", ingress+"/"+namespace+"/gitstore-system")
				require.NoError(t, err)
				revision := pushTranslationIntegrationSources(t, git, worker, strings.TrimSpace(string(format)), paths)
				awaitTranslationIntegrationWatches(t, current, watches, sources, revision)
				awaitTranslationIntegrationResources(t, current, sources, revision, true)
				verified += len(paths)
				clear(paths)
				clear(sources)
			}
			for _, document := range dataset.Documents {
				if document.Namespace != namespace {
					continue
				}
				paths[document.Path], sources[document.Name] = document.Source, document.Source
				if len(paths) == 20 {
					push()
				}
			}
			push()
		})
		if !passed {
			return
		}
	}
	require.Equal(t, len(dataset.Documents), verified)
	for _, namespace := range namespaces {
		current := fixture
		current.namespace = namespace
		for name, source := range dataset.Targets[namespace] {
			for _, api := range current.apis {
				sample, found := readTranslationIntegrationTarget(t, current, controller, api, source)
				require.True(t, found)
				require.Equal(t, before[namespace+"/"+name], sample, "Translation seeding cannot change target material or resolved aggregates")
			}
		}
	}
}

func TestTranslationNativeCapacityRetainedSource(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CAPACITY_RETAINED_SOURCE") != "1" {
		t.Skip("owned retained Translation source verification not selected")
	}
	fixture, _ := ownedTranslationNativeFixture(t)
	names := strings.Split(os.Getenv("TRANSLATION_TEST_CAPACITY_NAMESPACES"), ",")
	require.Len(t, names, 10)
	var namespaces [10]string
	copy(namespaces[:], names)
	require.Equal(t, fixture.namespace, namespaces[0])
	expectedUID, expectedRevision := os.Getenv("TRANSLATION_TEST_CAPACITY_SOURCE_UID"), os.Getenv("TRANSLATION_TEST_CAPACITY_SOURCE_REVISION")
	require.NotEmpty(t, expectedUID, "retain the source identity observed before replacement")
	require.NotEmpty(t, expectedRevision, "retain the accepted source revision observed before replacement")
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	dataset, err := buildTranslationCapacityDataset(raw, namespaces)
	require.NoError(t, err)
	document := dataset.Documents[999]
	remote := fixture.gitURL + "/" + fixture.namespace + "/gitstore-system"
	references, err := readTranslationCapacityReferences(t.Context(), remote, fixture.token)
	require.NoError(t, err)
	worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{remote: remote}, t.TempDir(), fixture.token, 0)
	require.NoError(t, err)
	last, err := secretCapacityGit(t.Context(), worker.directory, "", "log", "-1", "--format=%H", "--", document.Path)
	require.NoError(t, err)
	commit := strings.TrimSpace(string(last))
	require.True(t, translationCapacityHex(commit, 40))
	require.Equal(t, expectedRevision, "main@sha1:"+commit)
	stored, err := secretCapacityGitBounded(t.Context(), worker.directory, "", (1<<20)+1, "cat-file", "blob", commit+":"+document.Path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(document.Source, stored), "retained Git bytes must independently match the full authored source")
	reader := translationCapacityReader{
		client: &http.Client{Timeout: 5 * time.Second}, token: fixture.token,
		controllerToken: rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_CONTROLLER_TOKEN_FILE")),
		query:           translationIntegrationQuery(t, "translation"), snapshot: translationIntegrationQuery(t, "snapshot"),
	}
	for _, api := range fixture.apis {
		observation, err := reader.observe(t.Context(), api, document, expectedRevision)
		require.NoError(t, err)
		require.Equal(t, expectedUID, observation.UID)
		require.EqualValues(t, 1, observation.Generation)
		require.Equal(t, document.translationCapacityContent, observation.translationCapacityContent)
		require.True(t, observation.Ready)
	}
	after, err := readTranslationCapacityReferences(t.Context(), remote, fixture.token)
	require.NoError(t, err)
	require.Equal(t, references, after, "verification cannot resubmit a source or change retained history")
}

type translationCapacityObservation struct {
	translationCapacityContent
	Replica, UID, Revision, LastAppliedRevision, Version, WatchVersion, WatchCursor string
	Generation, ObservedGeneration                                                  int64
	Ready, Historical, WatchHistorical                                              bool
	VisibleAt, ReadyAt                                                              time.Time
	ReadVisibleAt, ReadReadyAt, WatchAt                                             time.Time
}

func (watch *capacityPushWatch) joinTranslation(index int, document translationCapacityDocument, current translationCapacityObservation, deadline time.Time) (translationCapacityObservation, bool, error) {
	watch.mu.Lock()
	defer watch.mu.Unlock()
	if watch.err != nil {
		return translationCapacityObservation{}, false, watch.err
	}
	if index < 0 || index >= len(watch.seen) {
		return translationCapacityObservation{}, false, fmt.Errorf("translation capacity: invalid watch replica")
	}
	record, found := watch.seen[index][document.Name]
	if !found || !current.Ready || current.Historical || current.UID == "" || current.Version == "" ||
		current.Generation < 1 || current.ObservedGeneration != current.Generation ||
		current.translationCapacityContent != document.translationCapacityContent ||
		record.uid != current.UID || record.version != current.Version || record.revision != current.Revision ||
		record.namespace != document.Namespace || record.generation != current.Generation || record.cursor == "" ||
		record.content.BodyDigest != current.BodyDigest || record.content.PatchDigest != current.PatchDigest ||
		record.at.IsZero() || record.at.After(deadline) || current.VisibleAt.IsZero() ||
		current.ReadyAt.Before(current.VisibleAt) || current.ReadyAt.After(deadline) {
		return translationCapacityObservation{}, false, nil
	}
	current.WatchVersion, current.WatchCursor, current.WatchHistorical = record.version, record.cursor, record.historical
	current.WatchAt = record.at
	if record.at.After(current.VisibleAt) {
		current.VisibleAt = record.at
	}
	if record.at.After(current.ReadyAt) {
		current.ReadyAt = record.at
	}
	return current, true, nil
}

type translationCapacityReader struct {
	client                 *http.Client
	token, controllerToken string
	controllerCredential   func(context.Context) (string, error)
	query, snapshot        string
	baselineQuery          string
	baselinePause          time.Duration
}

type translationCapacityControllerCredential struct {
	mu      sync.Mutex
	client  *http.Client
	api     string
	fixture secretCapacityOwnedFixture
	key     secretCapacityOwnedKey
	token   string
	expires time.Time
}

func (credential *translationCapacityControllerCredential) current(ctx context.Context) (string, error) {
	credential.mu.Lock()
	defer credential.mu.Unlock()
	if credential.token != "" && time.Until(credential.expires) > 15*time.Second {
		return credential.token, nil
	}
	assertion, err := credential.fixture.assertion(credential.key, credential.fixture.Name)
	if err != nil {
		return "", fmt.Errorf("translation capacity: enrolled controller assertion unavailable")
	}
	var data struct {
		IssueServiceAccountToken *struct {
			TokenRequest *struct {
				Status *struct {
					Token               string
					ExpirationTimestamp time.Time
				}
			}
		}
	}
	err = secretCapacityGraphQL(ctx, credential.client, credential.api, assertion,
		`mutation($input:IssueServiceAccountTokenInput!){issueServiceAccountToken(input:$input){tokenRequest{status{token expirationTimestamp}}}}`,
		map[string]any{"input": map[string]any{
			"metadata": map[string]string{"name": credential.fixture.Name},
			"spec":     map[string]any{"audiences": []string{"gitstore-api"}, "expirationSeconds": 60},
		}}, &data)
	if err != nil || data.IssueServiceAccountToken == nil || data.IssueServiceAccountToken.TokenRequest == nil ||
		data.IssueServiceAccountToken.TokenRequest.Status == nil {
		return "", fmt.Errorf("translation capacity: controller token issuance unavailable")
	}
	status := data.IssueServiceAccountToken.TokenRequest.Status
	var claims jwt.MapClaims
	_, _, err = jwt.NewParser().ParseUnverified(status.Token, &claims)
	expires, expiryError := claims.GetExpirationTime()
	audience, audienceError := claims.GetAudience()
	if err != nil || expiryError != nil || audienceError != nil || expires == nil ||
		claims["sub"] != "system:serviceaccount:"+credential.fixture.Name || claims["sa_uid"] != credential.fixture.UID ||
		credential.fixture.UID == "" || len(audience) != 1 || audience[0] != "gitstore-api" ||
		!expires.Time.Equal(status.ExpirationTimestamp.Truncate(time.Second)) ||
		!status.ExpirationTimestamp.After(time.Now().Add(15*time.Second)) || status.ExpirationTimestamp.After(time.Now().Add(65*time.Second)) {
		return "", fmt.Errorf("translation capacity: issued controller token identity/lifetime mismatch")
	}
	credential.token, credential.expires = status.Token, status.ExpirationTimestamp
	return credential.token, nil
}

func translationCapacityObservedContent(source translationIntegrationResource, sourceDigest string) (translationCapacityContent, error) {
	patches := []byte("[]")
	if len(source.Spec.Patches) > 0 {
		var err error
		patches, err = json.Marshal(source.Spec.Patches)
		if err != nil {
			return translationCapacityContent{}, fmt.Errorf("translation capacity: invalid observed patches")
		}
	}
	var body []byte
	if source.Body != nil {
		body = []byte(*source.Body)
	}
	return translationCapacityContent{
		SourceDigest: sourceDigest,
		BodyDigest:   fmt.Sprintf("%x", sha256.Sum256(body)), PatchDigest: fmt.Sprintf("%x", sha256.Sum256(patches)),
	}, nil
}

type translationCapacityBaseline struct {
	Products int64
	Digest   string
}

func readTranslationCapacityRetainedDocument(ctx context.Context, worker *capacityPushWorker, document translationCapacityDocument) (string, error) {
	if worker == nil || worker.cfg.namespace != document.Namespace || !translationIntegrationNamespace.MatchString(document.Name) ||
		document.Path != "translations/"+document.Name+".md" || len(document.Source) > 1<<20 ||
		fmt.Sprintf("%x", sha256.Sum256(document.Source)) != document.SourceDigest {
		return "", fmt.Errorf("translation capacity: retained source scope or authored fingerprint mismatch")
	}
	raw, err := secretCapacityGit(ctx, worker.directory, "", "log", "-1", "--format=%H", "HEAD", "--", document.Path)
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(raw))
	format := "sha1"
	if translationCapacityHex(commit, 64) {
		format = "sha256"
	} else if !translationCapacityHex(commit, 40) {
		return "", fmt.Errorf("translation capacity: retained source has no authored Git revision")
	}
	stored, err := secretCapacityGitBounded(ctx, worker.directory, "", (1<<20)+1, "cat-file", "blob", commit+":"+document.Path)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(stored, document.Source) {
		return "", fmt.Errorf("translation capacity: retained Git bytes differ from the independent corpus")
	}
	return "main@" + format + ":" + commit, nil
}

func readTranslationCapacityReferences(ctx context.Context, remote, token string) (string, error) {
	_, digest, err := readTranslationCapacityReferenceSnapshot(ctx, remote, token)
	return digest, err
}

func readTranslationCapacityReferenceSnapshot(ctx context.Context, remote, token string) (string, string, error) {
	if validateSecretCapacityGitEndpoint(remote) != nil && (!filepath.IsAbs(remote) || token != "") {
		return "", "", fmt.Errorf("translation capacity: an explicit credential-free HTTP remote or unauthenticated local fixture is required")
	}
	raw, err := secretCapacityGit(ctx, "", token, "ls-remote", "--refs", "--sort=refname", "--", remote)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(raw) > 4<<20 || len(lines) > 4096 {
		return "", "", fmt.Errorf("translation capacity: baseline reference budget exceeded")
	}
	main, previous := false, ""
	var canonical strings.Builder
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/") || fields[1] <= previous ||
			(!translationCapacityHex(fields[0], 40) && !translationCapacityHex(fields[0], 64)) {
			return "", "", fmt.Errorf("translation capacity: invalid baseline Git references")
		}
		main = main || fields[1] == "refs/heads/main"
		previous = fields[1]
		_, _ = fmt.Fprintf(&canonical, "%s\t%s\n", fields[0], fields[1])
	}
	if !main {
		return "", "", fmt.Errorf("translation capacity: baseline canonical main reference is missing")
	}
	return canonical.String(), fmt.Sprintf("%x", sha256.Sum256([]byte(canonical.String()))), nil
}

func (reader translationCapacityReader) baseline(ctx context.Context, api, namespace string, first, maximumPages int) (translationCapacityBaseline, error) {
	var result translationCapacityBaseline
	if !translationIntegrationNamespace.MatchString(namespace) || first < 1 || first > 100 ||
		maximumPages < 1 || maximumPages > 100_000 || reader.baselineQuery == "" ||
		reader.baselinePause < 0 || reader.baselinePause > time.Minute {
		return result, fmt.Errorf("translation capacity: invalid bounded baseline plan")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(namespace + "\n"))
	var after *string
	identities := make(map[[32]byte]bool)
	cursors := make(map[string]bool)
	for page := range maximumPages {
		if page > 0 && reader.baselinePause > 0 {
			timer := time.NewTimer(reader.baselinePause)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, ctx.Err()
			case <-timer.C:
			}
		}
		var data struct {
			Products *struct {
				Edges    []struct{ Node json.RawMessage }
				PageInfo *struct {
					HasNextPage *bool
					EndCursor   *string
				}
			}
		}
		if err := secretCapacityGraphQLBounded(ctx, reader.client, api, reader.token, reader.baselineQuery,
			map[string]any{"namespace": namespace, "first": first, "after": after}, &data, 8<<20); err != nil {
			return result, err
		}
		if data.Products == nil || data.Products.Edges == nil || data.Products.PageInfo == nil ||
			data.Products.PageInfo.HasNextPage == nil || len(data.Products.Edges) > first {
			return result, fmt.Errorf("translation capacity: incomplete baseline page")
		}
		for _, edge := range data.Products.Edges {
			var source struct {
				APIVersion, Kind string
				Metadata         struct {
					UID, Name, Namespace, Revision, ResourceVersion string
					Generation                                      int64
				}
				Spec map[string]any
			}
			if json.Unmarshal(edge.Node, &source) != nil || source.APIVersion != "catalog.gitstore.dev/v1beta1" ||
				source.Kind != "Product" || source.Metadata.Namespace != namespace || source.Metadata.Name == "" ||
				source.Metadata.UID == "" || source.Metadata.Revision == "" || source.Metadata.ResourceVersion == "" ||
				source.Metadata.Generation < 1 || source.Spec == nil {
				return result, fmt.Errorf("translation capacity: invalid baseline source")
			}
			identity := sha256.Sum256([]byte(source.Metadata.UID))
			if identities[identity] {
				return result, fmt.Errorf("translation capacity: repeated baseline identity")
			}
			identities[identity] = true
			var material map[string]any
			if json.Unmarshal(edge.Node, &material) != nil {
				return result, fmt.Errorf("translation capacity: invalid baseline material")
			}
			canonical, err := json.Marshal(material)
			if err != nil {
				return result, fmt.Errorf("translation capacity: invalid baseline material")
			}
			_, _ = digest.Write(canonical)
			_, _ = digest.Write([]byte("\n"))
			result.Products++
		}
		progress := data.Products.PageInfo
		if !*progress.HasNextPage {
			result.Digest = hex.EncodeToString(digest.Sum(nil))
			return result, nil
		}
		if progress.EndCursor == nil || *progress.EndCursor == "" || cursors[*progress.EndCursor] {
			return result, fmt.Errorf("translation capacity: baseline cursor did not advance")
		}
		cursors[*progress.EndCursor] = true
		after = progress.EndCursor
	}
	return result, fmt.Errorf("translation capacity: baseline page budget exhausted")
}

func (reader translationCapacityReader) read(ctx context.Context, api string, document translationCapacityDocument, revision string) (translationCapacityObservation, error) {
	observation := translationCapacityObservation{Replica: api}
	var data struct {
		Translation *translationIntegrationResource
	}
	if err := secretCapacityGraphQLBounded(ctx, reader.client, api, reader.token, reader.query,
		map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": document.Namespace, "name": document.Name}}},
		&data, 8<<20); err != nil {
		return observation, err
	}
	if data.Translation == nil {
		return observation, fmt.Errorf("translation capacity: source is not visible")
	}
	source := *data.Translation
	if err := verifyTranslationIntegrationAuthored(document.Source, revision, source); err != nil {
		return observation, err
	}
	observation.VisibleAt = time.Now()
	observation.ReadVisibleAt = observation.VisibleAt
	if err := verifyTranslationIntegrationResource(document.Source, revision, source, true); err != nil {
		return observation, err
	}
	if source.Status.Resolved == nil || source.Status.Resolved.CanonicalLocale != strings.ToLower(source.Spec.Locale) {
		return observation, fmt.Errorf("translation capacity: current canonical locale is missing")
	}
	content, err := translationCapacityObservedContent(source, "")
	if err != nil {
		return observation, err
	}
	observation.translationCapacityContent = content
	observation.UID, observation.Version = source.Metadata.UID, source.Metadata.ResourceVersion
	observation.Generation, observation.ObservedGeneration = source.Metadata.Generation, source.Status.ObservedGeneration
	observation.Revision, observation.LastAppliedRevision = source.Metadata.Revision, source.Status.LastAppliedRevision
	observation.Ready, observation.ReadyAt = true, time.Now()
	return observation, nil
}

func (reader translationCapacityReader) observe(ctx context.Context, api string, document translationCapacityDocument, revision string) (translationCapacityObservation, error) {
	observation, err := reader.read(ctx, api, document, revision)
	if err != nil {
		return observation, err
	}
	controllerToken := reader.controllerToken
	if reader.controllerCredential != nil {
		controllerToken, err = reader.controllerCredential(ctx)
		if err != nil || controllerToken == "" {
			return observation, fmt.Errorf("translation capacity: current controller credential unavailable")
		}
	}
	var snapshot struct {
		TranslationValidationSnapshot *struct {
			SourceVersion, SourceDigest string
			Observation                 *struct {
				ObservedGeneration  int64
				LastAppliedRevision string
				Conditions          []translationIntegrationCondition
				Resolved            json.RawMessage
			}
		}
	}
	if err := secretCapacityGraphQLBounded(ctx, reader.client, api, controllerToken, reader.snapshot,
		map[string]any{"namespace": document.Namespace, "name": document.Name,
			"sourceID": observation.UID, "sourceVersion": observation.Version}, &snapshot, 8<<20); err != nil {
		return observation, err
	}
	proof := snapshot.TranslationValidationSnapshot
	if proof == nil || proof.SourceVersion != observation.Version || proof.SourceDigest != document.SourceDigest ||
		proof.Observation == nil || proof.Observation.ObservedGeneration != observation.Generation ||
		proof.Observation.LastAppliedRevision != revision || len(proof.Observation.Resolved) == 0 ||
		bytes.Equal(bytes.TrimSpace(proof.Observation.Resolved), []byte("null")) {
		return observation, fmt.Errorf("translation capacity: snapshot does not join the current authored source")
	}
	ready := false
	for _, condition := range proof.Observation.Conditions {
		if condition.Type == "Ready" {
			if ready || condition.Status != "TRUE" || condition.ObservedGeneration != observation.Generation {
				return observation, fmt.Errorf("translation capacity: snapshot Ready proof is not current")
			}
			ready = true
		}
	}
	if !ready {
		return observation, fmt.Errorf("translation capacity: snapshot Ready proof is missing")
	}
	observation.SourceDigest = proof.SourceDigest
	observation.ReadyAt = time.Now()
	observation.ReadReadyAt = observation.ReadyAt
	return observation, nil
}

func (reader translationCapacityReader) converge(parent context.Context, apis [2]string, watch *capacityPushWatch, document translationCapacityDocument, push capacityPushResult) ([]translationCapacityObservation, error) {
	if !push.acknowledged || !translationCapacityHex(push.commit, 40) || push.at.IsZero() ||
		watch == nil || apis[0] == "" || apis[1] == "" || apis[0] == apis[1] {
		return nil, fmt.Errorf("translation capacity: acknowledged commit and two independent watch/read replicas required")
	}
	deadline := push.at.Add(30 * time.Second)
	ctx, cancel := context.WithDeadline(parent, deadline)
	defer cancel()
	revision := "main@sha1:" + push.commit
	firstVisible := [2]time.Time{}
	firstReady := [2]translationCapacityObservation{}
	for {
		observations := make([]translationCapacityObservation, 0, len(apis))
		for index, api := range apis {
			current, err := reader.observe(ctx, api, document, revision)
			if firstVisible[index].IsZero() && !current.VisibleAt.IsZero() {
				firstVisible[index] = current.VisibleAt
			}
			if err != nil {
				continue
			}
			current.VisibleAt = firstVisible[index]
			current.ReadVisibleAt = firstVisible[index]
			if firstReady[index].UID != current.UID || firstReady[index].Version != current.Version {
				firstReady[index] = current
			}
			current.ReadReadyAt = firstReady[index].ReadReadyAt
			current, joined, err := watch.joinTranslation(index, document, current, deadline)
			if err != nil {
				return nil, err
			}
			if joined {
				observations = append(observations, current)
			}
		}
		if len(observations) == len(apis) {
			if observations[0].UID != observations[1].UID {
				return nil, fmt.Errorf("translation capacity: replica source identities disagree")
			}
			if observations[0].Version == observations[1].Version {
				return observations, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("translation capacity: guarded source/watch convergence incomplete: %w", ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type translationCapacityReadTarget struct {
	Document                translationCapacityDocument
	Revision                string
	SourceID, SourceVersion string
}

type translationCapacityReadResults struct {
	Offered, Dropped, Completed, UnexpectedErrors int
	Latencies                                     []time.Duration
	DispatchDelays                                []time.Duration
	CompletedSequences                            []int
}

func (reader translationCapacityReader) load(ctx context.Context, apis [2]string, started time.Time, duration time.Duration, rate, workers int, targets []translationCapacityReadTarget) (translationCapacityReadResults, error) {
	var summary translationCapacityReadResults
	if apis[0] == "" || apis[1] == "" || apis[0] == apis[1] || duration <= 0 || duration > 2*time.Hour ||
		rate < 1 || rate > 1000 || workers < 1 || workers > 32 || len(targets) < 1 || len(targets) > 100_000 {
		return summary, fmt.Errorf("translation capacity: invalid bounded read plan")
	}
	for _, target := range targets {
		if (target.SourceID == "") != (target.SourceVersion == "") {
			return summary, fmt.Errorf("translation capacity: incomplete recovery source identity/version")
		}
	}
	if started.IsZero() {
		started = time.Now()
	}
	offered, dropped, results := runCapacityPushScheduleAt(ctx, started, duration, time.Second/time.Duration(rate), workers,
		func(ctx context.Context, sequence int) capacityPushResult {
			target := targets[sequence%len(targets)]
			observation, err := reader.read(ctx, apis[sequence%len(apis)], target.Document, target.Revision)
			if err == nil && target.SourceID != "" &&
				(observation.UID != target.SourceID || observation.Version != target.SourceVersion) {
				err = fmt.Errorf("translation capacity: recovery source identity/version changed")
			}
			return capacityPushResult{sequence: sequence, acknowledged: err == nil, at: time.Now()}
		})
	summary.Offered, summary.Dropped, summary.Completed = offered, dropped, len(results)
	summary.UnexpectedErrors = dropped
	for _, result := range results {
		summary.Latencies = append(summary.Latencies, result.latency)
		summary.DispatchDelays = append(summary.DispatchDelays, result.dispatchDelay)
		summary.CompletedSequences = append(summary.CompletedSequences, result.sequence)
		if !result.acknowledged {
			summary.UnexpectedErrors++
		}
	}
	if offered != int((duration-1)/(time.Second/time.Duration(rate)))+1 {
		return summary, fmt.Errorf("translation capacity: offered read window was interrupted")
	}
	return summary, nil
}

type translationCapacityPush struct {
	Sequence                  int
	OfferedAt, AcknowledgedAt time.Time
	Commit                    string
	Documents                 int
	Observations              []translationCapacityObservation
}

type translationCapacityFault struct {
	Name, Kind, ProcessBefore, ProcessAfter, Commit string
	At, RestoredAt, TrafficFrom, TrafficUntil       time.Time
	CorrectReads                                    int
	Restored, ReplayedWithoutResubmission           bool
	Observations                                    []translationCapacityObservation
}

// Local evidence checks are not a deployed runner or a capability certificate.
// The connected collector must derive these inputs from independent observations.
type translationCapacityReceipt struct {
	RunID, Scope, SourceDigest, ConfigDigest, TopologyDigest string
	Start, End                                               time.Time
	Offered, Completed, Dropped, Reads, UnexpectedErrors     int
	ReadSamples                                              int
	ReadP95, ReadP99                                         time.Duration
	ConvergenceP95, ConvergenceP99                           time.Duration
	Documents, Namespaces, MaximumDependents                 int
	Locales, TargetKinds                                     []string
	FieldCases, BodyCases, CombinedCases                     int
	NearLimit, ExpectedRejections                            int
	TargetsBefore, TargetsAfter                              string
	BaselineBefore, BaselineAfter                            string
	BaselineProducts                                         int64
	Pushes                                                   []translationCapacityPush
	Faults                                                   []translationCapacityFault
	Resources                                                []translationCapacityResourceEpoch
}

func verifyTranslationCapacityReceipt(plan translationCapacityPlan, receipt translationCapacityReceipt) error {
	window := plan.End.Sub(plan.Start)
	minimumWindow, maximumInterval, minimumReadRate := 30*time.Minute, 6*time.Second, 20
	switch plan.Mode {
	case "alpha":
		if plan.Workers < 1 || plan.Workers > 2 {
			return fmt.Errorf("alpha requires one or two bounded push workers")
		}
	case "production":
		minimumWindow, maximumInterval, minimumReadRate = time.Hour, 1200*time.Millisecond, 100
		if plan.Workers < 5 || plan.Workers > 32 || receipt.BaselineProducts < 5_000_000 {
			return fmt.Errorf("production requires bounded workers and retained Product baseline")
		}
	default:
		return fmt.Errorf("diagnostic evidence cannot certify acceptance")
	}
	if plan.Start.IsZero() || window < minimumWindow || window > 2*time.Hour ||
		plan.PushInterval < 50*time.Millisecond || plan.PushInterval > maximumInterval || plan.ReadRate < minimumReadRate {
		return fmt.Errorf("incomplete full-window/rate plan")
	}
	identities := map[string]bool{}
	for _, identity := range []string{plan.APIs[0], plan.APIs[1], plan.Controllers[0], plan.Controllers[1], plan.Git} {
		if identity == "" || identities[identity] {
			return fmt.Errorf("independent two-API/two-controller/singleton-Git identities required")
		}
		identities[identity] = true
	}
	if plan.RunID == "" || receipt.RunID != plan.RunID || receipt.Scope != "connected-translation" ||
		receipt.SourceDigest != plan.SourceDigest || receipt.ConfigDigest != plan.ConfigDigest ||
		receipt.TopologyDigest != plan.TopologyDigest || !receipt.Start.Equal(plan.Start) || !receipt.End.Equal(plan.End) {
		return fmt.Errorf("evidence does not join source/configuration/topology/full offered window")
	}
	for _, digest := range []string{plan.SourceDigest, plan.ConfigDigest, plan.TopologyDigest,
		receipt.TargetsBefore, receipt.TargetsAfter, receipt.BaselineBefore, receipt.BaselineAfter} {
		if !translationCapacityHex(digest, 64) {
			return fmt.Errorf("missing independent digest evidence")
		}
	}
	if receipt.TargetsBefore != receipt.TargetsAfter || receipt.BaselineBefore != receipt.BaselineAfter {
		return fmt.Errorf("target or retained baseline changed")
	}
	planned := int((window-1)/plan.PushInterval) + 1
	if receipt.Offered != planned || receipt.Completed != planned || receipt.Dropped != 0 ||
		len(receipt.Pushes) != planned || len(plan.Expected) != planned {
		return fmt.Errorf("offered/completed/expected-source joins are incomplete")
	}
	minimumReads := int(math.Ceil(window.Seconds() * float64(plan.ReadRate)))
	if receipt.Reads < minimumReads || receipt.ReadSamples != receipt.Reads || receipt.UnexpectedErrors < 0 ||
		float64(receipt.UnexpectedErrors)/float64(receipt.Reads) >= .001 ||
		receipt.ReadP95 <= 0 || receipt.ReadP99 < receipt.ReadP95 ||
		receipt.ReadP95 >= time.Second || receipt.ReadP99 >= 3*time.Second {
		return fmt.Errorf("incomplete read samples or failed read correctness/latency gates")
	}
	if receipt.Documents < 1000 || receipt.Namespaces < 10 || receipt.MaximumDependents < 1000 ||
		receipt.FieldCases < 1 || receipt.BodyCases < 1 || receipt.CombinedCases < 1 ||
		receipt.NearLimit < 1 || receipt.ExpectedRejections < 1 {
		return fmt.Errorf("incomplete connected corpus")
	}
	locales := map[string]bool{}
	for _, locale := range receipt.Locales {
		if locale == "" || locales[locale] {
			return fmt.Errorf("invalid locale corpus")
		}
		locales[locale] = true
	}
	kinds := map[string]bool{}
	for _, kind := range receipt.TargetKinds {
		if kinds[kind] {
			return fmt.Errorf("duplicate target kind does not extend coverage")
		}
		kinds[kind] = true
	}
	if len(locales) < 2 || len(kinds) != 5 || !kinds["Product"] || !kinds["ProductVariant"] ||
		!kinds["CategoryTaxonomy"] || !kinds["Collection"] || !kinds["File"] {
		return fmt.Errorf("missing delivered target/locale coverage")
	}
	sequences := make(map[int]bool, planned)
	commits := make(map[string]bool, planned)
	sourceUIDs := make(map[string]bool, planned)
	var visibility, convergence, acknowledgement []time.Duration
	componentClocks := 0
	for _, push := range receipt.Pushes {
		expected, ok := plan.Expected[push.Sequence]
		if !ok || push.Sequence < 0 || push.Sequence >= planned || sequences[push.Sequence] ||
			!push.OfferedAt.Equal(plan.Start.Add(time.Duration(push.Sequence)*plan.PushInterval)) ||
			push.AcknowledgedAt.Before(push.OfferedAt) || !translationCapacityHex(push.Commit, 40) || commits[push.Commit] {
			return fmt.Errorf("invalid scheduled-offer/acknowledged-source join")
		}
		sequences[push.Sequence] = true
		commits[push.Commit] = true
		// This oracle joins one document per push. Batches need per-document proofs,
		// not a document count beside a single successful resource observation.
		if push.Documents != 1 || len(push.Observations) != 2 {
			return fmt.Errorf("each document requires both replica observations")
		}
		for _, digest := range []string{expected.SourceDigest, expected.BodyDigest, expected.PatchDigest} {
			if !translationCapacityHex(digest, 64) {
				return fmt.Errorf("missing independently authored byte digest")
			}
		}
		replicas, uid, version := map[string]bool{}, "", ""
		for _, observed := range push.Observations {
			if (observed.Replica != plan.APIs[0] && observed.Replica != plan.APIs[1]) || replicas[observed.Replica] ||
				observed.UID == "" || observed.Version == "" || observed.WatchCursor == "" || observed.Historical ||
				observed.WatchVersion != observed.Version || !observed.Ready ||
				observed.Generation < 1 || observed.ObservedGeneration != observed.Generation ||
				observed.Revision != "main@sha1:"+push.Commit || observed.LastAppliedRevision != observed.Revision ||
				observed.translationCapacityContent != expected ||
				observed.VisibleAt.Before(push.AcknowledgedAt) || observed.ReadyAt.Before(observed.VisibleAt) ||
				observed.ReadyAt.After(plan.End.Add(30*time.Second)) {
				return fmt.Errorf("source/body/patch/current status/watch observation is incomplete")
			}
			if uid != "" && (uid != observed.UID || version != observed.Version) {
				return fmt.Errorf("replica resource identities/versions disagree")
			}
			if !observed.ReadVisibleAt.IsZero() || !observed.ReadReadyAt.IsZero() || !observed.WatchAt.IsZero() {
				componentClocks++
				combinedVisible := observed.ReadVisibleAt
				if observed.WatchAt.After(combinedVisible) {
					combinedVisible = observed.WatchAt
				}
				if observed.ReadVisibleAt.Before(push.AcknowledgedAt) ||
					observed.ReadReadyAt.Before(observed.ReadVisibleAt) || observed.ReadReadyAt.After(observed.ReadyAt) ||
					observed.WatchAt.Before(push.OfferedAt) || observed.WatchAt.After(observed.VisibleAt) ||
					!observed.VisibleAt.Equal(combinedVisible) {
					return fmt.Errorf("independent read/watch clocks do not join combined boundaries")
				}
			}
			replicas[observed.Replica], uid, version = true, observed.UID, observed.Version
			visibility = append(visibility, observed.VisibleAt.Sub(push.AcknowledgedAt))
			convergence = append(convergence, observed.ReadyAt.Sub(push.AcknowledgedAt))
		}
		if sourceUIDs[uid] {
			return fmt.Errorf("independent authored documents alias a source UID")
		}
		sourceUIDs[uid] = true
		acknowledgement = append(acknowledgement, push.AcknowledgedAt.Sub(push.OfferedAt))
	}
	if componentClocks != 0 && componentClocks != planned*2 {
		return fmt.Errorf("incomplete independent read/watch clock coverage")
	}
	percentile := func(values []time.Duration, percent int) time.Duration {
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		return values[(len(values)*percent+99)/100-1]
	}
	if percentile(visibility, 95) > time.Second || percentile(visibility, 99) > 3*time.Second ||
		percentile(convergence, 95) > time.Second || percentile(convergence, 99) > 3*time.Second ||
		percentile(acknowledgement, 99) > 30*time.Second ||
		receipt.ConvergenceP95 != percentile(convergence, 95) || receipt.ConvergenceP99 != percentile(convergence, 99) {
		return fmt.Errorf("failed or inconsistent visibility/convergence/push latency gates")
	}
	if len(receipt.Faults) != len(plan.Faults) {
		return fmt.Errorf("missing declared fault evidence")
	}
	faults := map[string]bool{}
	for _, fault := range receipt.Faults {
		kind, declared := plan.Faults[fault.Name]
		if !declared || faults[fault.Name] || (kind != "physical" && kind != "simulated") || fault.Kind != kind ||
			!fault.Restored || !fault.ReplayedWithoutResubmission || fault.At.Before(plan.Start) || !fault.At.Before(plan.End) ||
			fault.RestoredAt.Before(fault.At) || fault.RestoredAt.After(fault.TrafficFrom) ||
			fault.TrafficFrom.Before(fault.At) || fault.TrafficUntil.After(fault.At.Add(30*time.Second)) ||
			fault.TrafficUntil.After(plan.End) || fault.TrafficUntil.Sub(fault.TrafficFrom) < 5*time.Second ||
			fault.CorrectReads < int(math.Ceil(fault.TrafficUntil.Sub(fault.TrafficFrom).Seconds()*float64(plan.ReadRate))) {
			return fmt.Errorf("unjoined fault/restoration/sustained domain recovery evidence")
		}
		if kind == "physical" && (fault.Name == "api-restart" || fault.Name == "controller-restart") &&
			(fault.ProcessBefore == "" || fault.ProcessAfter == "" || fault.ProcessBefore == fault.ProcessAfter) {
			return fmt.Errorf("physical replacement requires distinct process identities")
		}
		var source *translationCapacityPush
		for i := range receipt.Pushes {
			if receipt.Pushes[i].Commit == fault.Commit {
				source = &receipt.Pushes[i]
				break
			}
		}
		if source == nil || source.AcknowledgedAt.After(fault.At) || len(fault.Observations) != 2 {
			return fmt.Errorf("fault recovery must join an acknowledged measured source and both API reads")
		}
		replicas := map[string]bool{}
		for _, observed := range fault.Observations {
			var accepted *translationCapacityObservation
			for i := range source.Observations {
				if source.Observations[i].Replica == observed.Replica {
					accepted = &source.Observations[i]
					break
				}
			}
			if accepted == nil || replicas[observed.Replica] || observed.Historical || !observed.Ready ||
				observed.UID != accepted.UID || observed.Version != accepted.Version ||
				observed.Generation != accepted.Generation || observed.ObservedGeneration != observed.Generation ||
				observed.Revision != accepted.Revision || observed.LastAppliedRevision != observed.Revision ||
				observed.translationCapacityContent != accepted.translationCapacityContent ||
				observed.VisibleAt.Before(fault.RestoredAt) || observed.ReadyAt.Before(observed.VisibleAt) ||
				observed.ReadyAt.After(fault.TrafficFrom) {
				return fmt.Errorf("recovery reads do not join the exact current source after restoration")
			}
			replicas[observed.Replica] = true
		}
		faults[fault.Name] = true
	}
	return verifyTranslationCapacityResources(plan, receipt)
}

func verifyTranslationCapacityResources(plan translationCapacityPlan, receipt translationCapacityReceipt) error {
	finiteNonnegative := func(value float64) bool {
		return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
	}
	if plan.ResourceInterval < time.Second || plan.ResourceInterval > 5*time.Second ||
		len(plan.ResourceLimits) != 4 || len(receipt.Resources) < 4 || len(receipt.Resources) > 16 ||
		len(plan.ResourceGaps) > len(plan.Faults) {
		return fmt.Errorf("bounded full-window resource plan and four replica limits required")
	}
	replicas := map[string][]translationCapacityResourceEpoch{}
	for _, replica := range []string{plan.APIs[0], plan.APIs[1], plan.Controllers[0], plan.Controllers[1]} {
		limit, ok := plan.ResourceLimits[replica]
		if !ok || !finiteNonnegative(limit.CPUCores) || limit.CPUCores <= 0 ||
			!finiteNonnegative(limit.MemoryBytes) || limit.MemoryBytes <= 0 {
			return fmt.Errorf("finite positive configured CPU/memory ceilings required per API/controller")
		}
		replicas[replica] = nil
	}
	processes := map[string]bool{}
	for _, epoch := range receipt.Resources {
		limit, known := plan.ResourceLimits[epoch.Replica]
		if !known || epoch.Process == "" || processes[epoch.Process] ||
			epoch.Start.Before(plan.Start) || epoch.End.After(plan.End) || !epoch.End.After(epoch.Start) ||
			len(epoch.Samples) < 2 || len(epoch.Samples) > 7201 ||
			!epoch.Samples[0].At.Equal(epoch.Start) || !epoch.Samples[len(epoch.Samples)-1].At.Equal(epoch.End) {
			return fmt.Errorf("independent process epochs require bounded samples and exact endpoints")
		}
		processes[epoch.Process] = true
		baseline := epoch.Samples[0]
		if baseline.RetainedBytes <= 0 || baseline.Goroutines <= 0 {
			return fmt.Errorf("positive per-process retained-memory/goroutine baselines required")
		}
		for index, sample := range epoch.Samples {
			for _, value := range []float64{sample.CPUCores, sample.ResidentBytes, sample.RetainedBytes, sample.Goroutines} {
				if !finiteNonnegative(value) {
					return fmt.Errorf("resource observations must be finite and nonnegative")
				}
			}
			if sample.At.Before(epoch.Start) || sample.At.After(epoch.End) ||
				(index > 0 && (!sample.At.After(epoch.Samples[index-1].At) ||
					sample.At.Sub(epoch.Samples[index-1].At) > plan.ResourceInterval)) {
				return fmt.Errorf("resource observations contain unordered, duplicate or missing scrapes")
			}
			if sample.CPUCores/limit.CPUCores >= .8 || sample.ResidentBytes > limit.MemoryBytes ||
				sample.RetainedBytes > sample.ResidentBytes || sample.RetainedBytes/baseline.RetainedBytes >= 1.1 ||
				math.Abs(sample.Goroutines-baseline.Goroutines)/baseline.Goroutines > .05 {
				return fmt.Errorf("per-process CPU/memory ceiling, retained growth or goroutine drift gate failed")
			}
		}
		replicas[epoch.Replica] = append(replicas[epoch.Replica], epoch)
	}
	usedFaults, usedGaps := map[string]bool{}, map[int]bool{}
	for replica, epochs := range replicas {
		sort.Slice(epochs, func(i, j int) bool { return epochs[i].Start.Before(epochs[j].Start) })
		if len(epochs) == 0 || !epochs[0].Start.Equal(plan.Start) || !epochs[len(epochs)-1].End.Equal(plan.End) {
			return fmt.Errorf("resources do not cover every API/controller's full offered window")
		}
		for index := 1; index < len(epochs); index++ {
			before, after := epochs[index-1], epochs[index]
			if after.Start.Before(before.End) {
				return fmt.Errorf("overlapping resource process epochs")
			}
			faultName := "controller-restart"
			if replica == plan.APIs[0] || replica == plan.APIs[1] {
				faultName = "api-restart"
			}
			matched := false
			for _, fault := range receipt.Faults {
				if fault.Name == faultName && fault.Kind == "physical" && !usedFaults[fault.Name] &&
					fault.ProcessBefore == before.Process && fault.ProcessAfter == after.Process &&
					fault.At.Equal(before.End) && !after.Start.After(fault.RestoredAt) {
					usedFaults[fault.Name], matched = true, true
					break
				}
			}
			if !matched {
				return fmt.Errorf("resource epoch change does not join a declared physical replacement")
			}
			if after.Start.Equal(before.End) {
				continue
			}
			matched = false
			for gapIndex, gap := range plan.ResourceGaps {
				if !usedGaps[gapIndex] && gap.Replica == replica && gap.Fault == faultName &&
					gap.Start.Equal(before.End) && gap.End.Equal(after.Start) &&
					gap.End.Sub(gap.Start) <= 30*time.Second {
					usedGaps[gapIndex], matched = true, true
					break
				}
			}
			if !matched {
				return fmt.Errorf("undeclared resource scrape gap across process replacement")
			}
		}
	}
	if len(usedGaps) != len(plan.ResourceGaps) {
		return fmt.Errorf("declared resource gaps must exactly join observed replacement windows")
	}
	for _, fault := range receipt.Faults {
		if fault.Kind == "physical" && (fault.Name == "api-restart" || fault.Name == "controller-restart") &&
			!usedFaults[fault.Name] {
			return fmt.Errorf("physical replacement lacks full-window resource epochs")
		}
	}
	return nil
}

func translationCapacityHex(value string, length int) bool {
	if len(value) != length || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
