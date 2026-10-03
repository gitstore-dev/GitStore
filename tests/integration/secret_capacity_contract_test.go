// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSecretCapacityProductionEvidence(t *testing.T) {
	require.NoError(t, validateSecretCapacityEvidence(secretCapacityEvidenceFixture()))
	cases := []struct {
		name   string
		change func(*secretCapacityEvidence)
	}{
		{"missing scenario", func(e *secretCapacityEvidence) { e.scenario = "" }},
		{"diagnostic cannot pass", func(e *secretCapacityEvidence) { e.mode = capacityModeDiagnostic }},
		{"alpha cannot certify production", func(e *secretCapacityEvidence) { e.mode = capacityModeAlpha }},
		{"repository invariants missing", func(e *secretCapacityEvidence) { e.repositoryLifecyclePassed = false }},
		{"undersized dataset", func(e *secretCapacityEvidence) {
			e.dataset.rows--
			e.dataset.titled = e.dataset.rows
			e.dataset.acknowledged = e.dataset.rows
		}},
		{"untitled Products", func(e *secretCapacityEvidence) { e.dataset.titled-- }},
		{"unacknowledged fixtures", func(e *secretCapacityEvidence) { e.dataset.acknowledged-- }},
		{"count query is not offline proof", func(e *secretCapacityEvidence) { e.dataset.method = "graphql-count" }},
		{"counting during load", func(e *secretCapacityEvidence) { e.dataset.completedBeforeLoad = false }},
		{"missing pagination", func(e *secretCapacityEvidence) { e.dataset.pages = 0 }},
		{"unbounded page", func(e *secretCapacityEvidence) { e.dataset.maxPageRows = e.dataset.pageLimit + 1 }},
		{"impossible count", func(e *secretCapacityEvidence) { e.dataset.pages = 1 }},
		{"short load", func(e *secretCapacityEvidence) { e.duration -= time.Second }},
		{"short baseline", func(e *secretCapacityEvidence) { e.baseline -= time.Second }},
		{"short post-load", func(e *secretCapacityEvidence) { e.postLoad -= time.Second }},
		{"wrong File pool", func(e *secretCapacityEvidence) { e.filePool-- }},
		{"wrong worker count", func(e *secretCapacityEvidence) { e.workers++ }},
		{"unbounded queue", func(e *secretCapacityEvidence) { e.queueLimit++ }},
		{"observed queue exceeded bound", func(e *secretCapacityEvidence) { e.queuePeak++ }},
		{"observed worker count exceeded bound", func(e *secretCapacityEvidence) { e.pushWorkersPeak++ }},
		{"missing worker measurement", func(e *secretCapacityEvidence) { e.pushWorkersPeak = 0 }},
		{"empty queue", func(e *secretCapacityEvidence) { e.queueLimit = 0 }},
		{"oversized File batch", func(e *secretCapacityEvidence) { e.maxFilesPerPush++ }},
		{"oversized push", func(e *secretCapacityEvidence) { e.maxBytesPerPush++ }},
		{"empty pushes", func(e *secretCapacityEvidence) { e.maxFilesPerPush = 0 }},
		{"reduced offered rate", func(e *secretCapacityEvidence) { e.pushInterval += time.Millisecond }},
		{"missing sustained offer", func(e *secretCapacityEvidence) { e.sustainedOffered-- }},
		{"missed scheduled offer", func(e *secretCapacityEvidence) { e.missedSchedules++ }},
		{"missing minute burst", func(e *secretCapacityEvidence) { e.bursts = e.bursts[:59] }},
		{"duplicate burst minute", func(e *secretCapacityEvidence) { e.bursts[1].at = e.bursts[0].at }},
		{"reduced burst", func(e *secretCapacityEvidence) { e.bursts[0].offered-- }},
		{"slow burst", func(e *secretCapacityEvidence) { e.bursts[0].offerDuration += time.Nanosecond }},
		{"slow drain", func(e *secretCapacityEvidence) { e.bursts[0].drain += time.Nanosecond }},
		{"metadata-only load", func(e *secretCapacityEvidence) { e.gitPushes = 0 }},
		{"missing acknowledgments", func(e *secretCapacityEvidence) { e.acknowledged-- }},
		{"healthy errors at threshold", func(e *secretCapacityEvidence) {
			e.failed = 42
			e.acknowledged -= 42
		}},
		{"dropped offers", func(e *secretCapacityEvidence) { e.dropped++ }},
		{"projection loss", func(e *secretCapacityEvidence) { e.projectionLoss++ }},
		{"cross-namespace success", func(e *secretCapacityEvidence) { e.crossNamespaceSuccess++ }},
		{"credential leakage", func(e *secretCapacityEvidence) { e.leakedArtifacts++ }},
		{"untyped File traffic", func(e *secretCapacityEvidence) { e.typedFiles = 0 }},
		{"no reference samples", func(e *secretCapacityEvidence) { e.reference.samples = 0 }},
		{"partial reference measurement", func(e *secretCapacityEvidence) { e.reference.samples-- }},
		{"reference p95", func(e *secretCapacityEvidence) { e.reference.p95 += time.Nanosecond }},
		{"reference p99", func(e *secretCapacityEvidence) { e.reference.p99 += time.Nanosecond }},
		{"no resolution samples", func(e *secretCapacityEvidence) { e.resolution.samples = 0 }},
		{"healthy resolver errors at threshold", func(e *secretCapacityEvidence) { e.localHealthyFailures = 42 }},
		{"negative resolver error count", func(e *secretCapacityEvidence) { e.localHealthyFailures = -1 }},
		{"resolution p95", func(e *secretCapacityEvidence) { e.resolution.p95 += time.Nanosecond }},
		{"resolution p99", func(e *secretCapacityEvidence) { e.resolution.p99 += time.Nanosecond }},
		{"push p95", func(e *secretCapacityEvidence) { e.push.p95 += time.Nanosecond }},
		{"push p99", func(e *secretCapacityEvidence) { e.push.p99 += time.Nanosecond }},
		{"negative timing", func(e *secretCapacityEvidence) { e.push.p95 = -1 }},
		{"inverted percentiles", func(e *secretCapacityEvidence) { e.push.p99 = time.Second }},
		{"partial push measurement", func(e *secretCapacityEvidence) { e.push.samples-- }},
		{"insufficient contention", func(e *secretCapacityEvidence) { e.resolverCallers-- }},
		{"excess provider concurrency", func(e *secretCapacityEvidence) { e.providerPeak++ }},
		{"saturation not reached", func(e *secretCapacityEvidence) { e.providerPeak-- }},
		{"provider queue", func(e *secretCapacityEvidence) { e.providerQueued++ }},
		{"provider retries", func(e *secretCapacityEvidence) { e.providerRetries++ }},
		{"missing saturation rejection", func(e *secretCapacityEvidence) { e.saturationDenied = 0 }},
		{"missing deadline rejection", func(e *secretCapacityEvidence) { e.deadlineDenied = 0 }},
		{"single API", func(e *secretCapacityEvidence) {
			e.processes = e.processes[1:]
			e.scannedProcesses = int64(len(e.processes))
		}},
		{"single controller", func(e *secretCapacityEvidence) {
			e.processes = append(e.processes[:2], e.processes[3:]...)
			e.controllers = e.controllers[1:]
			e.restartTargetID = e.controllers[0].id
			e.scannedProcesses = int64(len(e.processes))
		}},
		{"multiple Git services", func(e *secretCapacityEvidence) {
			git := e.processes[4]
			git.id = "second-git"
			e.processes = append(e.processes, git)
			e.scannedProcesses = int64(len(e.processes))
		}},
		{"duplicate process identity", func(e *secretCapacityEvidence) { e.processes[1].id = e.processes[0].id }},
		{"CPU at limit", func(e *secretCapacityEvidence) { e.processes[0].cpu = .8 }},
		{"nonfinite CPU", func(e *secretCapacityEvidence) { e.processes[0].cpu = math.NaN() }},
		{"RSS at limit", func(e *secretCapacityEvidence) { e.processes[0].rssAfter = 110 }},
		{"goroutines above limit", func(e *secretCapacityEvidence) { e.processes[0].goroutinesAfter = 111 }},
		{"missing resource samples", func(e *secretCapacityEvidence) { e.processes[0].samples = 0 }},
		{"missing warmed RSS", func(e *secretCapacityEvidence) { e.processes[0].rssBefore = 0 }},
		{"missing goroutines", func(e *secretCapacityEvidence) { e.processes[0].goroutinesBefore = 0 }},
		{"long-lived tokens", func(e *secretCapacityEvidence) { e.controllers[0].tokenTTL += time.Second }},
		{"parallel identity exchanges", func(e *secretCapacityEvidence) { e.controllers[0].exchangePeak++ }},
		{"retry above cap", func(e *secretCapacityEvidence) { e.controllers[0].maxRetry += time.Nanosecond }},
		{"missing fresh token", func(e *secretCapacityEvidence) { e.controllers[0].freshTokens = 0 }},
		{"no repeated token exchange", func(e *secretCapacityEvidence) { e.controllers[0].freshTokens = 1 }},
		{"no reconciler progress", func(e *secretCapacityEvidence) { e.controllers[0].reconciliations = 0 }},
		{"recovery too slow", func(e *secretCapacityEvidence) { e.controllers[1].recovery += time.Nanosecond }},
		{"missing controller evidence", func(e *secretCapacityEvidence) { e.controllers = e.controllers[:1] }},
		{"unmeasured controller", func(e *secretCapacityEvidence) { e.controllers[0].id = "unknown" }},
		{"wrong outage schedule", func(e *secretCapacityEvidence) { e.outageAt += time.Minute }},
		{"outage shorter than required", func(e *secretCapacityEvidence) { e.outageDuration -= time.Second }},
		{"readiness extended past expiry", func(e *secretCapacityEvidence) { e.expiredUnready = false }},
		{"peer affected", func(e *secretCapacityEvidence) { e.peerFailures++ }},
		{"missing classified outage failure", func(e *secretCapacityEvidence) { e.classifiedOutageFailures = 0 }},
		{"wrong rotation schedule", func(e *secretCapacityEvidence) { e.rotationAt += time.Minute }},
		{"missing overlap renewal", func(e *secretCapacityEvidence) { e.controllers[0].overlapRenewals = 0 }},
		{"missing old-key denial", func(e *secretCapacityEvidence) { e.retiredKeyDenied = 0 }},
		{"missing wrong-subject denial", func(e *secretCapacityEvidence) { e.wrongSubjectDenied = 0 }},
		{"wrong restart schedule", func(e *secretCapacityEvidence) { e.restartAt += time.Minute }},
		{"unconfirmed restart", func(e *secretCapacityEvidence) { e.restartConfirmed = false }},
		{"restart aimed at API", func(e *secretCapacityEvidence) { e.restartTargetID = e.processes[0].id }},
		{"replacement never authenticated", func(e *secretCapacityEvidence) { e.replacementFreshTokens = 0 }},
		{"replacement made no progress", func(e *secretCapacityEvidence) { e.replacementReconciliations = 0 }},
		{"replacement recovery too slow", func(e *secretCapacityEvidence) { e.replacementRecovery += time.Nanosecond }},
		{"unchanged replacement identity", func(e *secretCapacityEvidence) { e.replacementID = e.controllers[0].id }},
		{"missing replacement identity", func(e *secretCapacityEvidence) { e.replacementID = "" }},
		{"API replacement loses fields", func(e *secretCapacityEvidence) { e.crossReplicaProjectionChecks = 0 }},
		{"unscanned telemetry", func(e *secretCapacityEvidence) { e.scannedProcesses = 0 }},
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
	e.failed = 41
	e.localHealthyFailures = 41
	e.acknowledged -= 41
	e.processes[0].goroutinesAfter = 110
	require.NoError(t, validateSecretCapacityEvidence(e), "healthy errors below 0.1%% and goroutines at +10%% are permitted")
}

type secretCapacityDatasetProof struct {
	method                        string
	rows, titled, acknowledged    int64
	pages, pageLimit, maxPageRows int64
	completedBeforeLoad           bool
}

type secretCapacityLatency struct {
	samples  int64
	p95, p99 time.Duration
}

type secretCapacityBurst struct {
	at, offerDuration, drain time.Duration
	offered                  int64
}

type secretCapacityProcessProof struct {
	role, id                          string
	samples                           int64
	cpu                               float64
	rssBefore, rssAfter               uint64
	goroutinesBefore, goroutinesAfter int64
}

type secretCapacityControllerProof struct {
	id                                            string
	tokenTTL, maxRetry, recovery                  time.Duration
	exchangePeak                                  int
	freshTokens, reconciliations, overlapRenewals int64
}

// These assertions are shared by the eventual deployed collector and its
// negative fixtures. They do not enable the guarded capacity scenario.
type secretCapacityEvidence struct {
	scenario                                 string
	mode                                     capacityMode
	repositoryLifecyclePassed                bool
	dataset                                  secretCapacityDatasetProof
	duration, baseline, postLoad             time.Duration
	filePool, workers, queueLimit            int
	queuePeak, pushWorkersPeak               int
	maxFilesPerPush, maxBytesPerPush         int
	pushInterval                             time.Duration
	sustainedOffered, missedSchedules        int64
	bursts                                   []secretCapacityBurst
	gitPushes, acknowledged, failed, dropped int64
	typedFiles                               int64
	projectionLoss, crossNamespaceSuccess    int64
	leakedArtifacts, scannedProcesses        int64
	reference, resolution, push              secretCapacityLatency
	localHealthyFailures                     int64
	resolverCallers, providerPeak            int
	providerQueued, providerRetries          int64
	saturationDenied, deadlineDenied         int64
	processes                                []secretCapacityProcessProof
	controllers                              []secretCapacityControllerProof
	outageAt, outageDuration                 time.Duration
	expiredUnready                           bool
	peerFailures, classifiedOutageFailures   int64
	rotationAt                               time.Duration
	retiredKeyDenied, wrongSubjectDenied     int64
	restartAt                                time.Duration
	restartConfirmed                         bool
	restartTargetID, replacementID           string
	replacementFreshTokens                   int64
	replacementReconciliations               int64
	replacementRecovery                      time.Duration
	crossReplicaProjectionChecks             int64
}

func validateSecretCapacityEvidence(e secretCapacityEvidence) error {
	var failures []error
	check := func(ok bool, name string) {
		if !ok {
			failures = append(failures, fmt.Errorf("secret capacity: %s", name))
		}
	}
	check(e.scenario == "repository-lifecycle-secrets/v1", "missing scenario identity")
	check(e.mode == capacityModeProduction, "only production evidence can satisfy this contract")
	check(e.repositoryLifecyclePassed, "existing Repository lifecycle invariants must also pass")
	d := e.dataset
	check(d.method == "offline-paginated" && d.completedBeforeLoad, "dataset must be verified offline before load")
	check(d.rows >= 5_000_000 && d.rows == d.titled && d.rows == d.acknowledged, "dataset needs at least five million acknowledged titled Products")
	check(d.pageLimit > 0 && d.pageLimit <= 1000 && d.maxPageRows > 0 && d.maxPageRows <= d.pageLimit &&
		d.rows > 0 && d.pages > 0 && d.pages >= (d.rows-1)/d.maxPageRows+1, "missing or inconsistent bounded pagination proof")
	check(e.duration == 60*time.Minute && e.baseline >= 5*time.Minute && e.postLoad >= 10*time.Minute, "load and stabilization durations do not meet the contract")
	check(e.filePool == 100 && e.workers == 32 && e.queueLimit > 0 && e.queueLimit <= 256, "File pool, workers or queue outside bounds")
	check(e.queuePeak >= 0 && e.queuePeak <= e.queueLimit && e.pushWorkersPeak > 0 && e.pushWorkersPeak <= e.workers, "observed client concurrency or queue outside bounds")
	check(e.maxFilesPerPush > 0 && e.maxFilesPerPush <= 10 && e.maxBytesPerPush > 0 && e.maxBytesPerPush <= 128*1024, "push batch outside bounds")
	check(e.pushInterval == 100*time.Millisecond && e.sustainedOffered == 36_000 && e.missedSchedules == 0, "sustained offered load was reduced")
	check(len(e.bursts) == 60, "one burst per minute is required")
	for i, burst := range e.bursts {
		check(burst.at >= 0 && burst.at/time.Minute == time.Duration(i) && burst.at+burst.offerDuration <= e.duration &&
			burst.offered == 100 && burst.offerDuration > 0 && burst.offerDuration <= time.Second &&
			burst.drain >= 0 && burst.drain <= 30*time.Second, "burst schedule, offered count or drain outside bounds")
	}
	check(e.gitPushes == 42_000 && e.acknowledged >= 0 && e.failed >= 0 && e.failed <= e.gitPushes &&
		e.acknowledged == e.gitPushes-e.failed && e.dropped == 0, "all offered batches must execute real Git pushes and have accounted outcomes")
	check(e.failed >= 0 && e.failed < 42, "unexpected healthy push errors must be below 0.1 percent")
	check(e.typedFiles >= e.gitPushes && e.typedFiles <= e.gitPushes*10, "every push must contain typed File traffic")
	check(e.projectionLoss == 0 && e.crossNamespaceSuccess == 0 && e.leakedArtifacts == 0, "integrity, namespace isolation or credential scanning failed")
	check(e.crossReplicaProjectionChecks >= 2, "typed projections must survive API replacement and cross-replica reads")
	latency := func(value secretCapacityLatency, p95, p99 time.Duration) bool {
		return value.samples > 0 && value.p95 >= 0 && value.p99 >= value.p95 && value.p95 <= p95 && value.p99 <= p99
	}
	check(latency(e.reference, 5*time.Millisecond, 20*time.Millisecond) && e.reference.samples == e.typedFiles, "per-File reference latency evidence missing or over threshold")
	check(latency(e.resolution, 10*time.Millisecond, 50*time.Millisecond), "healthy local resolution latency evidence missing or over threshold")
	check(e.localHealthyFailures >= 0 && e.resolution.samples > 0 &&
		float64(e.localHealthyFailures)/float64(e.resolution.samples) < .001, "unexpected healthy resolver errors must be below 0.1 percent")
	check(latency(e.push, 2*time.Second, 30*time.Second) && e.push.samples == e.gitPushes, "push latency must cover sustained and burst traffic")
	check(e.resolverCallers == 32 && e.providerPeak == 16 && e.providerQueued == 0 && e.providerRetries == 0 &&
		e.saturationDenied > 0 && e.deadlineDenied > 0, "bounded resolver contention/deadline proof missing")

	identities := make(map[string]string, len(e.processes))
	roles := make(map[string]int)
	for _, process := range e.processes {
		_, duplicate := identities[process.id]
		check(process.id != "" && !duplicate, "process identities must be present and unique")
		identities[process.id] = process.role
		roles[process.role]++
		check(process.role == "api" || process.role == "controller" || process.role == "git", "unknown measured process role")
		check(process.samples > 0 && process.cpu >= 0 && process.cpu < .8, "normalized CPU must be measured and below 80 percent")
		check(process.rssBefore > 0 && process.rssAfter > 0 &&
			float64(process.rssAfter)/float64(process.rssBefore) < 1.1, "warmed RSS growth must be below 10 percent")
		if process.role != "git" {
			check(process.goroutinesBefore > 0 && process.goroutinesAfter > 0 &&
				float64(process.goroutinesAfter)/float64(process.goroutinesBefore) <= 1.1, "post-load goroutines must return within 10 percent")
		}
	}
	check(roles["api"] >= 2 && roles["controller"] >= 2 && roles["git"] == 1, "requires two APIs/two controllers and exactly one Git service")
	check(e.scannedProcesses == int64(len(e.processes)), "all measured processes need telemetry scanning")
	controllerIDs := make(map[string]bool, len(e.controllers))
	for _, controller := range e.controllers {
		check(identities[controller.id] == "controller" && !controllerIDs[controller.id], "controller proof must match distinct measured processes")
		controllerIDs[controller.id] = true
		check(controller.tokenTTL == 60*time.Second && controller.exchangePeak == 1 &&
			controller.maxRetry >= 0 && controller.maxRetry <= 30*time.Second, "identity TTL, single-flight or retry cap not demonstrated")
		check(controller.freshTokens >= 2 && controller.reconciliations > 0 && controller.overlapRenewals > 0,
			"every controller must authenticate, reconcile and renew through overlap")
		check(controller.recovery > 0 && controller.recovery <= 60*time.Second, "each controller needs recovery within 60 seconds")
	}
	check(len(e.controllers) == roles["controller"], "missing controller proof")
	check(e.outageAt/time.Minute == 15 && e.outageDuration == 90*time.Second && e.expiredUnready &&
		e.peerFailures == 0 && e.classifiedOutageFailures > 0, "minute-15 expiry-spanning isolated outage not demonstrated")
	check(e.rotationAt/time.Minute == 30 && e.retiredKeyDenied > 0 && e.wrongSubjectDenied > 0, "minute-30 rotation/retirement and authorization denial missing")
	_, reusedIdentity := identities[e.replacementID]
	check(e.restartAt/time.Minute == 45 && e.restartConfirmed && controllerIDs[e.restartTargetID] &&
		e.replacementID != "" && !reusedIdentity, "minute-45 restart needs an explicit controller target and new process identity")
	check(e.replacementFreshTokens > 0 && e.replacementReconciliations > 0 &&
		e.replacementRecovery > 0 && e.replacementRecovery <= 60*time.Second, "replacement must freshly authenticate and resume reconciliation within 60 seconds")
	return errors.Join(failures...)
}

func secretCapacityEvidenceFixture() secretCapacityEvidence {
	e := secretCapacityEvidence{
		scenario: "repository-lifecycle-secrets/v1", mode: capacityModeProduction, repositoryLifecyclePassed: true,
		dataset: secretCapacityDatasetProof{
			method: "offline-paginated", rows: 5_000_000, titled: 5_000_000, acknowledged: 5_000_000,
			pages: 5000, pageLimit: 1000, maxPageRows: 1000, completedBeforeLoad: true,
		},
		duration: time.Hour, baseline: 5 * time.Minute, postLoad: 10 * time.Minute,
		filePool: 100, workers: 32, queueLimit: 256, maxFilesPerPush: 10, maxBytesPerPush: 128 * 1024,
		queuePeak: 256, pushWorkersPeak: 32,
		pushInterval: 100 * time.Millisecond, sustainedOffered: 36_000, gitPushes: 42_000, acknowledged: 42_000,
		typedFiles: 42_000, crossReplicaProjectionChecks: 2,
		reference:       secretCapacityLatency{42_000, 5 * time.Millisecond, 20 * time.Millisecond},
		resolution:      secretCapacityLatency{42_000, 10 * time.Millisecond, 50 * time.Millisecond},
		push:            secretCapacityLatency{42_000, 2 * time.Second, 30 * time.Second},
		resolverCallers: 32, providerPeak: 16, saturationDenied: 16, deadlineDenied: 1,
		outageAt: 15 * time.Minute, outageDuration: 90 * time.Second, expiredUnready: true, classifiedOutageFailures: 1,
		rotationAt: 30 * time.Minute, retiredKeyDenied: 1, wrongSubjectDenied: 1,
		restartAt: 45 * time.Minute, restartConfirmed: true, restartTargetID: "controller-a", replacementID: "controller-replacement",
		replacementFreshTokens: 1, replacementReconciliations: 1, replacementRecovery: time.Minute,
	}
	for i := range 60 {
		e.bursts = append(e.bursts, secretCapacityBurst{time.Duration(i) * time.Minute, time.Second, 30 * time.Second, 100})
	}
	for _, process := range []struct{ role, id string }{
		{"api", "api-a"}, {"api", "api-b"}, {"controller", "controller-a"}, {"controller", "controller-b"}, {"git", "git"},
	} {
		e.processes = append(e.processes, secretCapacityProcessProof{
			role: process.role, id: process.id, samples: 3600, cpu: .79,
			rssBefore: 100, rssAfter: 109, goroutinesBefore: 100, goroutinesAfter: 109,
		})
		if process.role == "controller" {
			e.controllers = append(e.controllers, secretCapacityControllerProof{
				id: process.id, tokenTTL: time.Minute, maxRetry: 30 * time.Second, recovery: time.Minute,
				exchangePeak: 1, freshTokens: 120, reconciliations: 120, overlapRenewals: 1,
			})
		}
	}
	e.scannedProcesses = int64(len(e.processes))
	return e
}
