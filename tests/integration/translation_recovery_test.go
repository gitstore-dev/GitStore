// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func readTranslationRecoveryPrivateJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 {
		return fmt.Errorf("translation recovery: invalid protected receipt")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

type translationRolloutMount struct {
	Source, Destination string
	RW                  bool
}

type translationRolloutEpoch struct {
	ID, Image string
	StartedAt time.Time
	Mounts    []translationRolloutMount
}

type translationRolloutContainer struct {
	ID, Image string
	Config    struct {
		Labels map[string]string
		Env    []string
	}
	State struct {
		Running, OOMKilled bool
		StartedAt          time.Time
		FinishedAt         time.Time
	}
	Mounts []translationRolloutMount
}

func validateTranslationRolloutReplacement(root string, old translationRolloutEpoch, stopped, current translationRolloutContainer) error {
	if old.ID == "" || old.Image == "" || stopped.ID != old.ID || stopped.Image != old.Image ||
		stopped.State.Running || stopped.State.FinishedAt.IsZero() || old.StartedAt.IsZero() ||
		stopped.State.FinishedAt.Before(old.StartedAt) || current.ID == "" || current.ID == old.ID ||
		current.Image == "" || current.Image == old.Image || !current.State.Running || current.State.OOMKilled ||
		current.State.StartedAt.Before(stopped.State.FinishedAt) {
		return fmt.Errorf("translation rollout: invalid replacement lifetime")
	}
	writable := func(mounts []translationRolloutMount) []translationRolloutMount {
		var result []translationRolloutMount
		for _, mount := range mounts {
			if mount.RW {
				result = append(result, mount)
			}
		}
		return result
	}
	previous, next := writable(old.Mounts), writable(current.Mounts)
	if !reflect.DeepEqual(previous, writable(stopped.Mounts)) || !reflect.DeepEqual(previous, next) {
		return fmt.Errorf("translation rollout: writable storage changed")
	}
	for _, mount := range next {
		relative, err := filepath.Rel(root, mount.Source)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("translation rollout: storage is outside the owned fixture")
		}
	}
	return nil
}

func TestTranslationRolloutReplacementRejectsUnsafeLifetimesAndStorage(t *testing.T) {
	root := t.TempDir()
	start := time.Now().UTC().Add(-time.Minute)
	mounts := []translationRolloutMount{{Source: filepath.Join(root, "checkpoints"), Destination: "/checkpoints", RW: true}}
	old := translationRolloutEpoch{ID: "old", Image: "previous", StartedAt: start, Mounts: mounts}
	stopped := translationRolloutContainer{ID: old.ID, Image: old.Image, Mounts: slices.Clone(mounts)}
	stopped.State.StartedAt, stopped.State.FinishedAt = start, start.Add(time.Second)
	current := translationRolloutContainer{ID: "current", Image: "capable", Mounts: slices.Clone(mounts)}
	current.State.Running, current.State.StartedAt = true, start.Add(2*time.Second)
	require.NoError(t, validateTranslationRolloutReplacement(root, old, stopped, current))
	for _, fault := range []string{"overlap", "still-running", "checkpoint-replaced", "unowned-storage", "wrong-predecessor"} {
		t.Run(fault, func(t *testing.T) {
			before, after, original := stopped, current, old
			after.Mounts = slices.Clone(current.Mounts)
			switch fault {
			case "overlap":
				after.State.StartedAt = start
			case "still-running":
				before.State.Running = true
			case "checkpoint-replaced":
				after.Mounts[0].Source = filepath.Join(root, "new-checkpoints")
			case "unowned-storage":
				original.Mounts = []translationRolloutMount{{Source: filepath.Dir(root), Destination: "/checkpoints", RW: true}}
				before.Mounts, after.Mounts = original.Mounts, original.Mounts
			case "wrong-predecessor":
				before.ID = "unrelated"
			}
			require.Error(t, validateTranslationRolloutReplacement(root, original, before, after))
		})
	}
}

func readTranslationRolloutJSON(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 8<<20 {
		return fmt.Errorf("translation rollout: invalid protected capture")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, (8<<20)+1))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("translation rollout: trailing capture content")
	}
	return nil
}

func TestTranslationNativeCapableRollback(t *testing.T) {
	root := os.Getenv("TRANSLATION_TEST_ROLLOUT_DIR")
	if root == "" {
		t.Skip("owned finite rollout captures not selected")
	}
	require.True(t, filepath.IsAbs(root))
	info, err := os.Lstat(root)
	require.NoError(t, err)
	require.True(t, info.IsDir() && info.Mode().Perm()&0o077 == 0)
	owner := os.Getenv("TRANSLATION_TEST_OWNER")
	require.NotEmpty(t, owner)
	fixture, _ := ownedTranslationNativeFixture(t)
	var manifest struct {
		RunID, Project, Namespace, Phase string
		APIA, APIB                       string
		Containers                       map[string]struct{ ID, Image string }
		VerifiedTargetRepair             struct{ DescriptorDigest string }
	}
	require.NoError(t, readTranslationRolloutJSON(filepath.Join(root, "fixture.json"), &manifest))
	require.Equal(t, "capable-rollback", manifest.Phase)
	require.Equal(t, fixture.namespace, manifest.Namespace)
	require.Equal(t, fixture.apis, []string{manifest.APIA, manifest.APIB})
	require.True(t, strings.HasPrefix(manifest.Project, "gitstore-rollout-"))
	require.NotEmpty(t, manifest.RunID)
	require.Len(t, manifest.Containers, 5)
	capture := func(pattern string, value any) {
		t.Helper()
		paths, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		require.Len(t, paths, 1, "select exactly one attributable rollout receipt")
		require.NoError(t, readTranslationRolloutJSON(paths[0], value))
	}
	var rollback struct {
		RunID, Scope string
		Replacements []struct {
			Role         string
			Old, Current translationRolloutEpoch
		}
	}
	capture("rollback-proof-*.json", &rollback)
	require.Equal(t, manifest.RunID, rollback.RunID)
	require.Equal(t, "finite-capable-only-rollback", rollback.Scope)
	require.Len(t, rollback.Replacements, 4)
	docker := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "docker",
			append([]string{"--context", getEnv("TRANSLATION_TEST_DOCKER_CONTEXT", "default")}, args...)...)
		output, err := command.Output()
		require.NoError(t, err, "owned topology command failed")
		require.LessOrEqual(t, len(output), 8<<20)
		return output
	}
	inspect := func(id string) translationRolloutContainer {
		t.Helper()
		var containers []translationRolloutContainer
		require.NoError(t, json.Unmarshal(docker("inspect", id), &containers))
		require.Len(t, containers, 1)
		return containers[0]
	}
	liveIDs := strings.Fields(string(docker("ps", "-q", "--no-trunc", "--filter",
		"label=com.docker.compose.project="+manifest.Project)))
	expectedIDs := make([]string, 0, 5)
	for _, container := range manifest.Containers {
		expectedIDs = append(expectedIDs, container.ID)
	}
	require.ElementsMatch(t, expectedIDs, liveIDs, "every live fixture role must be declared")
	checkOwner := func(container translationRolloutContainer) {
		t.Helper()
		require.Equal(t, owner, container.Config.Labels["gitstore.test.session"])
		require.Equal(t, manifest.RunID, container.Config.Labels["gitstore.test.fixture"])
	}
	roles, checkpoints := map[string]bool{}, []string{}
	for _, replacement := range rollback.Replacements {
		require.Contains(t, []string{"api-a", "api-b", "controller-a", "controller-b"}, replacement.Role)
		require.False(t, roles[replacement.Role])
		roles[replacement.Role] = true
		require.Equal(t, manifest.Containers[replacement.Role].ID, replacement.Current.ID)
		require.Equal(t, manifest.Containers[replacement.Role].Image, replacement.Current.Image)
		stopped, current := inspect(replacement.Old.ID), inspect(replacement.Current.ID)
		checkOwner(stopped)
		checkOwner(current)
		require.NoError(t, validateTranslationRolloutReplacement(root, replacement.Old, stopped, current))
		require.Equal(t, replacement.Current.StartedAt, current.State.StartedAt, "no unrecorded restart")
		component := "API"
		if strings.HasPrefix(replacement.Role, "controller-") {
			component = "CONTROLLER"
			for _, mount := range current.Mounts {
				if mount.RW {
					checkpoints = append(checkpoints, mount.Source)
				}
			}
		}
		require.Contains(t, current.Config.Env, "GITSTORE_"+component+"__TRANSLATION__ENABLED=true")
		require.NotEmpty(t, manifest.VerifiedTargetRepair.DescriptorDigest)
		require.Contains(t, current.Config.Env, "GITSTORE_"+component+"__TRANSLATION__DESCRIPTOR_DIGEST="+manifest.VerifiedTargetRepair.DescriptorDigest)
	}
	require.Len(t, checkpoints, 2)
	require.NotEqual(t, checkpoints[0], checkpoints[1])
	for _, pair := range [][2]string{{checkpoints[0], checkpoints[1]}, {checkpoints[1], checkpoints[0]}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		require.NoError(t, err)
		require.True(t, relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)),
			"controller writable checkpoint directories must not overlap")
	}
	var preactivation struct {
		RunID    string
		Topology []struct {
			Role string
			translationRolloutEpoch
		}
	}
	capture("preactivation-proof-*.json", &preactivation)
	require.Equal(t, manifest.RunID, preactivation.RunID)
	git := inspect(manifest.Containers["git"].ID)
	checkOwner(git)
	require.True(t, git.State.Running)
	require.False(t, git.State.OOMKilled)
	var gitBefore *translationRolloutEpoch
	for _, role := range preactivation.Topology {
		if role.Role == "git" {
			require.Nil(t, gitBefore)
			epoch := role.translationRolloutEpoch
			gitBefore = &epoch
		}
	}
	require.NotNil(t, gitBefore)
	require.Equal(t, *gitBefore, translationRolloutEpoch{
		ID: git.ID, Image: git.Image, StartedAt: git.State.StartedAt, Mounts: git.Mounts})
	type authority struct {
		Namespace string
		APIs      map[string]struct {
			Translations, Snapshots, Targets, Observations map[string]json.RawMessage
		}
		ReferencesSha256 string
	}
	var before, after authority
	require.NoError(t, readTranslationRolloutJSON(filepath.Join(root, "rollback-authority-before.json"), &before))
	require.NoError(t, readTranslationRolloutJSON(filepath.Join(root, "rollback-authority-after.json"), &after))
	require.Equal(t, before, after, "accepted sources, bindings, target material and refs must survive rollback")
	require.Equal(t, fixture.namespace, after.Namespace)
	require.Len(t, after.APIs, 2)
	require.Equal(t, after.APIs["apiA"], after.APIs["apiB"])
	require.NotEmpty(t, after.APIs["apiA"].Translations)
	targetKinds := make([]string, 0, 5)
	for kind := range after.APIs["apiA"].Targets {
		targetKinds = append(targetKinds, kind)
	}
	require.ElementsMatch(t, []string{"Product", "ProductVariant", "CategoryTaxonomy", "Collection", "File"}, targetKinds)
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct{ Targets map[string]string }
	require.NoError(t, json.Unmarshal(raw, &corpus))
	targets := map[string]translationIntegrationResource{}
	for kind, source := range corpus.Targets {
		header, _, err := translationIntegrationDocument([]byte(source))
		require.NoError(t, err)
		var authored translationIntegrationResource
		require.NoError(t, yaml.Unmarshal(header, &authored))
		targets[kind] = authored
	}
	_, credential, _ := newTranslationCapacityReader(t, fixture)
	for index, api := range fixture.apis {
		expected := after.APIs[[]string{"apiA", "apiB"}[index]]
		read := func(key string, variables map[string]any, field string, controller bool) json.RawMessage {
			t.Helper()
			client := fixture
			if controller {
				client.token, err = credential.current(t.Context())
				require.NoError(t, err)
			}
			response := client.queryBounded(t, api, translationIntegrationQuery(t, key), variables, 8<<20)
			require.Empty(t, response.Errors, "live capable reader must serve stored authority")
			var data map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(response.Data, &data))
			require.Contains(t, data, field)
			return data[field]
		}
		for name, source := range expected.Translations {
			current := read("translation", map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": name}}}, "translation", false)
			require.JSONEq(t, string(source), string(current))
			var resource translationIntegrationResource
			require.NoError(t, json.Unmarshal(current, &resource))
			snapshot := read("snapshot", map[string]any{"namespace": fixture.namespace, "name": name,
				"sourceID": resource.Metadata.UID, "sourceVersion": resource.Metadata.ResourceVersion},
				"translationValidationSnapshot", true)
			require.Contains(t, expected.Snapshots, name)
			require.JSONEq(t, string(expected.Snapshots[name]), string(snapshot))
		}
		for kind, target := range expected.Targets {
			resource := targets[kind]
			require.NotEmpty(t, resource.Metadata.Name)
			current := read("target."+kind, map[string]any{"by": map[string]any{"namespacePath": map[string]any{"namespace": fixture.namespace, "name": resource.Metadata.Name}}}, "target", true)
			require.JSONEq(t, string(target), string(current))
			observation := read("targetObservation", map[string]any{"namespace": fixture.namespace,
				"apiVersion": resource.APIVersion, "kind": kind, "name": resource.Metadata.Name}, "translationTargetObservation", true)
			require.Contains(t, expected.Observations, kind)
			require.JSONEq(t, string(expected.Observations[kind]), string(observation))
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	references, err := secretCapacityGit(ctx, t.TempDir(), fixture.token, "ls-remote", "--refs",
		fixture.gitURL+"/"+fixture.namespace+"/gitstore-system")
	require.NoError(t, err)
	require.Equal(t, after.ReferencesSha256, fmt.Sprintf("%x", sha256.Sum256(references)))
	t.Log("Finite capable rollback verified against live sources, snapshots, bindings, five targets, refs and owned process/storage lifetimes; not sustained acceptance")
}

type translationCapacityCallbackFault struct {
	root, runID string
	window      map[string]any
	callbacks   translationCapacityPushCallbacks
	mu          sync.Mutex
	done        chan translationCapacityCallbackOutcome
}

type translationCapacityCallbackOutcome struct {
	fault map[string]any
	err   error
}

type translationCapacityCallbackInterception struct {
	RunID             string    `json:"runId"`
	Kind              string    `json:"kind"`
	Stage             string    `json:"stage"`
	NewCommit         string    `json:"newCommit"`
	OldCommit         string    `json:"oldCommit"`
	Ref               string    `json:"ref"`
	At                time.Time `json:"at"`
	UpstreamCompleted bool      `json:"upstreamCompleted"`
}

func (fault *translationCapacityCallbackFault) awaitDelivery(ctx context.Context, commit, stage string, notBefore time.Time) (translationCapacityCallbackInterception, error) {
	var intercepted translationCapacityCallbackInterception
	for {
		paths, err := filepath.Glob(filepath.Join(fault.root, "intercepted-*.json"))
		if err != nil || len(paths) > 256 {
			return intercepted, fmt.Errorf("translation recovery: callback receipt budget exceeded")
		}
		for _, path := range paths {
			if err := readTranslationRecoveryPrivateJSON(path, &intercepted); err != nil {
				return intercepted, err
			}
			if intercepted.NewCommit == commit && intercepted.Stage == stage && !intercepted.At.Before(notBefore) {
				return intercepted, nil
			}
		}
		if err := waitSecretCapacityUntil(ctx, time.Now().Add(20*time.Millisecond)); err != nil {
			return intercepted, err
		}
	}
}

func (fault *translationCapacityCallbackFault) clear() error {
	fault.mu.Lock()
	defer fault.mu.Unlock()
	return writeSecretCapacityPrivateJSON(filepath.Join(fault.root, "fault.json"),
		map[string]string{"runId": fault.runID, "stage": ""})
}

func (fault *translationCapacityCallbackFault) finish(ctx context.Context) (map[string]any, error) {
	select {
	case result := <-fault.done:
		return result.fault, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (reader translationCapacityReader) recoveryReads(ctx context.Context, apis [2]string,
	document translationCapacityDocument, commit, uid, version string,
) ([]translationCapacityObservation, error) {
	result := make([]translationCapacityObservation, 0, len(apis))
	for _, api := range apis {
		// Protected recovery receipts also need the authorized source fingerprint.
		observation, err := reader.observe(ctx, api, document, "main@sha1:"+commit)
		if err != nil {
			return nil, err
		}
		if observation.UID != uid || observation.Version != version {
			return nil, fmt.Errorf("translation recovery: current source identity/version changed after restoration")
		}
		result = append(result, observation)
	}
	return result, nil
}

func newTranslationCapacityCallbackFault(parent context.Context, cfg capacityPushConfig, reader translationCapacityReader,
	document translationCapacityDocument, stage, remote string) (*translationCapacityCallbackFault, error) {
	root := os.Getenv("TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR")
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	if (stage != "callback-loss" && stage != "reply-loss") || !filepath.IsAbs(root) ||
		!info.IsDir() || info.Mode().Perm()&0o077 != 0 || cfg.startAt.Before(time.Now()) ||
		cfg.duration < 20*time.Second || cfg.duration > 30*time.Second ||
		os.Getenv("TRANSLATION_TEST_CHAOS_PROFILE") != "" || os.Getenv("TRANSLATION_TEST_POST_ACK_RESTART") == "1" {
		return nil, fmt.Errorf("translation recovery: select one bounded simulated connected fault")
	}
	var ready struct{ RunID, Scope, EvidenceClass, Upstream string }
	if err := readTranslationRecoveryPrivateJSON(filepath.Join(root, "ready.json"), &ready); err != nil {
		return nil, err
	}
	if ready.RunID != cfg.runID || ready.Scope != "owned-callback-fault-proxy" || ready.EvidenceClass != "diagnostic" ||
		ready.Upstream != os.Getenv("TRANSLATION_TEST_CALLBACK_UPSTREAM") {
		return nil, fmt.Errorf("translation recovery: callback proxy belongs to another run")
	}
	lock, err := os.OpenFile(filepath.Join(root, "workload.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock.Close(); err != nil {
		return nil, err
	}
	declaredAt := time.Now().UTC()
	fault := &translationCapacityCallbackFault{root: root, runID: cfg.runID,
		done: make(chan translationCapacityCallbackOutcome, 1),
		window: map[string]any{"name": stage, "kind": "simulated", "declaredAt": declaredAt,
			"start": cfg.startAt, "end": cfg.startAt.Add(cfg.duration)}}
	if err := writeSecretCapacityPrivateJSON(filepath.Join(root, "window-"+stage+".json"), fault.window); err != nil {
		return nil, err
	}
	if err := writeSecretCapacityPrivateJSON(filepath.Join(root, "fault.json"),
		map[string]string{"runId": cfg.runID, "stage": stage}); err != nil {
		return nil, err
	}
	acknowledged := make(chan capacityPushResult, 1)
	observed := make(chan translationCapacityPush, 1)
	fault.callbacks.acknowledged = func(result capacityPushResult) {
		if result.sequence == 0 {
			acknowledged <- result
		}
	}
	fault.callbacks.observed = func(result translationCapacityPush) {
		if result.Sequence == 0 {
			observed <- result
		}
	}
	go func() {
		ctx, cancel := context.WithDeadline(parent, cfg.startAt.Add(cfg.duration))
		defer cancel()
		result, err := fault.recover(ctx, cfg, reader, document, stage, remote, acknowledged, observed)
		if clearErr := fault.clear(); err == nil {
			err = clearErr
		}
		fault.done <- translationCapacityCallbackOutcome{fault: result, err: err}
	}()
	return fault, nil
}

func (fault *translationCapacityCallbackFault) recover(ctx context.Context, cfg capacityPushConfig, reader translationCapacityReader,
	document translationCapacityDocument, stage, remote string, acknowledgements <-chan capacityPushResult,
	observations <-chan translationCapacityPush) (map[string]any, error) {
	var acknowledged capacityPushResult
	select {
	case acknowledged = <-acknowledgements:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	intercepted, err := fault.awaitDelivery(ctx, acknowledged.commit, stage, cfg.startAt)
	if err != nil {
		return nil, err
	}
	if intercepted.RunID != cfg.runID || intercepted.Kind != "simulated" || intercepted.Ref != "refs/heads/main" ||
		!translationCapacityHex(intercepted.OldCommit, 40) || intercepted.At.Before(cfg.startAt) ||
		intercepted.At.After(time.Now()) || intercepted.UpstreamCompleted != (stage == "reply-loss") {
		return nil, fmt.Errorf("translation recovery: interception does not join the actual measured receive")
	}
	beforeSnapshot, before, err := readTranslationCapacityReferenceSnapshot(ctx, remote, reader.token)
	if err != nil {
		return nil, err
	}
	if err := waitSecretCapacityUntil(ctx, acknowledged.at.Add(2*time.Second)); err != nil {
		return nil, err
	}
	restored := time.Now().UTC()
	if err := fault.clear(); err != nil {
		return nil, err
	}
	afterSnapshot, after, err := readTranslationCapacityReferenceSnapshot(ctx, remote, reader.token)
	if err != nil || before != after {
		return nil, fmt.Errorf("translation recovery: restoration changed canonical references")
	}
	delivery, err := fault.awaitDelivery(ctx, acknowledged.commit, "", restored)
	if err != nil {
		return nil, err
	}
	if delivery.RunID != cfg.runID || delivery.Kind != "forwarded" || !delivery.UpstreamCompleted ||
		delivery.OldCommit != intercepted.OldCommit || delivery.Ref != intercepted.Ref {
		return nil, fmt.Errorf("translation recovery: restored replay does not join the intercepted receive")
	}
	var current translationCapacityPush
	select {
	case current = <-observations:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if current.Commit != acknowledged.commit || len(current.Observations) != 2 {
		return nil, fmt.Errorf("translation recovery: current source proof does not join the acknowledged receive")
	}
	recoveryReads, err := reader.recoveryReads(ctx, [2]string{cfg.verifyAPIA, cfg.verifyAPIB},
		document, acknowledged.commit, current.Observations[0].UID, current.Observations[0].Version)
	if err != nil {
		return nil, err
	}
	trafficFrom := time.Now().UTC().Add(500 * time.Millisecond)
	deadline := intercepted.At.Add(30 * time.Second)
	traffic, err := reader.load(ctx, [2]string{cfg.verifyAPIA, cfg.verifyAPIB}, trafficFrom, 5*time.Second, 20, 4,
		[]translationCapacityReadTarget{{Document: document, Revision: "main@sha1:" + acknowledged.commit,
			SourceID: current.Observations[0].UID, SourceVersion: current.Observations[0].Version}})
	checkedAt := time.Now().UTC()
	proof := map[string]any{"schemaVersion": 1, "runId": cfg.runID, "evidenceClass": "diagnostic",
		"faultAt": intercepted.At, "trafficFrom": trafficFrom, "trafficUntil": trafficFrom.Add(5 * time.Second),
		"observed": checkedAt, "recoveryDeadline": deadline, "rate": 20, "workers": 4, "traffic": traffic,
		"commit": acknowledged.commit, "sourceSha256": document.SourceDigest,
		"sourceID": current.Observations[0].UID, "sourceVersion": current.Observations[0].Version,
		"recoveryReads": recoveryReads}
	if writeErr := writeSecretCapacityPrivateJSON(filepath.Join(fault.root, "traffic-"+stage+".json"), proof); writeErr != nil {
		return nil, writeErr
	}
	if err != nil || traffic.Offered != 100 || traffic.Completed != 100 || traffic.Dropped != 0 ||
		traffic.UnexpectedErrors != 0 || checkedAt.After(deadline) || checkedAt.After(cfg.startAt.Add(cfg.duration)) {
		return nil, fmt.Errorf("translation recovery: incomplete five-second measured domain recovery")
	}
	summary := map[string]any{"stage": stage, "kind": "simulated", "started": current.OfferedAt,
		"faultAt": intercepted.At, "restored": restored, "observed": checkedAt,
		"trafficFrom": trafficFrom, "trafficUntil": trafficFrom.Add(5 * time.Second), "recoveryDeadline": deadline,
		"correctReads": traffic.Completed, "commit": acknowledged.commit, "sourceSha256": document.SourceDigest,
		"referencesBeforeRestoration": before, "referencesAfterRestoration": after, "referencesSha256": before,
		"referenceSnapshotBeforeRestoration": beforeSnapshot, "referenceSnapshotAfterRestoration": afterSnapshot,
		"interception": intercepted, "delivery": delivery,
		"physicalFault": nil, "physicalRestart": nil, "window": fault.window, "trafficProof": proof}
	return summary, writeSecretCapacityPrivateJSON(filepath.Join(fault.root, "recovery-"+stage+".json"), summary)
}

func TestTranslationNativeCallbackTransportRecovery(t *testing.T) {
	const (
		recoveryBudget  = 30 * time.Second
		trafficDuration = 5 * time.Second
		trafficLead     = 500 * time.Millisecond
		trafficWorkers  = 4
	)
	root := os.Getenv("TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR")
	if root == "" {
		t.Skip("owned callback transport proxy not selected")
	}
	fixture, gitB := ownedTranslationNativeFixture(t)
	require.True(t, filepath.IsAbs(root))
	info, err := os.Lstat(root)
	require.NoError(t, err)
	require.True(t, info.IsDir())
	require.Zero(t, info.Mode().Perm()&0o077)
	readPrivate := func(t *testing.T, path string, value any) {
		t.Helper()
		require.NoError(t, readTranslationRecoveryPrivateJSON(path, value))
	}
	var ready struct{ RunID, Scope, EvidenceClass, Upstream string }
	readPrivate(t, filepath.Join(root, "ready.json"), &ready)
	require.NotEmpty(t, ready.RunID)
	require.Equal(t, os.Getenv("TRANSLATION_TEST_CALLBACK_RUN_ID"), ready.RunID)
	require.Equal(t, "owned-callback-fault-proxy", ready.Scope)
	require.Equal(t, "diagnostic", ready.EvidenceClass)
	require.Equal(t, os.Getenv("TRANSLATION_TEST_CALLBACK_UPSTREAM"), ready.Upstream)
	physicalProfile := os.Getenv("TRANSLATION_TEST_CHAOS_PROFILE")
	if physicalProfile != "" {
		require.Contains(t, []string{"api-restart", "controller-restart", "journal-interruption"}, physicalProfile)
		require.Equal(t, "1", os.Getenv("CHAOS_CONFIRM"))
		require.NotEmpty(t, os.Getenv("CHAOS_OWNER"))
		require.NotEqual(t, "1", os.Getenv("TRANSLATION_TEST_POST_ACK_RESTART"), "select one physical fault per diagnostic")
	}
	_, err = os.Lstat(filepath.Join(root, "fault.json"))
	require.True(t, os.IsNotExist(err), "another owner already selected a fault")
	lock, err := os.OpenFile(filepath.Join(root, "workload.lock"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err, "one finite workload per owned proxy run")
	require.NoError(t, lock.Close())
	setFault := func(t *testing.T, stage string) {
		t.Helper()
		require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "fault.json"),
			map[string]string{"runId": ready.RunID, "stage": stage}))
	}
	t.Cleanup(func() { setFault(t, "") })
	reader, _, _ := newTranslationCapacityReader(t, fixture)
	reader.baselinePause = 50 * time.Millisecond
	before := [2]translationCapacityBaseline{}
	for replica, api := range fixture.apis {
		before[replica], err = reader.baseline(t.Context(), api, fixture.namespace, 10, 100_000)
		require.NoError(t, err)
	}
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Valid     map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source, err := translationIntegrationSource([]byte(corpus.Valid["product-combined"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	template, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	summaries := make([]map[string]any, 0, 2)
	for index, stage := range []string{"callback-loss", "reply-loss"} {
		if !t.Run(stage, func(t *testing.T) {
			worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{
				remote:    []string{fixture.gitURL, gitB}[index] + "/" + fixture.namespace + "/gitstore-system",
				namespace: fixture.namespace,
			}, t.TempDir(), fixture.token, index)
			require.NoError(t, err)
			document, err := renderTranslationCapacityPush(translationCapacityDocument{
				Namespace: fixture.namespace, Name: template.Metadata.Name, Source: source,
			}, uniqueName("transport"), index)
			require.NoError(t, err)
			oldHead, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "HEAD")
			require.NoError(t, err)
			commit, err := commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{document.Path: document.Source})
			require.NoError(t, err)
			declaredAt := time.Now().UTC()
			window := map[string]any{
				"name": stage, "kind": "simulated", "declaredAt": declaredAt,
				"start": declaredAt, "end": declaredAt.Add(recoveryBudget),
			}
			require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "window-"+stage+".json"), window))
			setFault(t, stage)
			defer setFault(t, "")
			started := time.Now().UTC()
			_, err = secretCapacityGit(t.Context(), worker.directory, fixture.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
			require.NoError(t, err, "accepted push must succeed despite post-acknowledgement transport loss")
			var intercepted translationCapacityCallbackInterception
			awaitDelivery := func(expectedStage string, notBefore time.Time) {
				t.Helper()
				require.Eventually(t, func() bool {
					paths, err := filepath.Glob(filepath.Join(root, "intercepted-*.json"))
					require.NoError(t, err)
					require.LessOrEqual(t, len(paths), 256)
					for _, path := range paths {
						readPrivate(t, path, &intercepted)
						if intercepted.NewCommit == commit && intercepted.Stage == expectedStage && !intercepted.At.Before(notBefore) {
							require.Equal(t, ready.RunID, intercepted.RunID)
							kind := "simulated"
							if expectedStage == "" {
								kind = "forwarded"
							}
							require.Equal(t, kind, intercepted.Kind)
							require.Equal(t, strings.TrimSpace(string(oldHead)), intercepted.OldCommit)
							require.Equal(t, "refs/heads/main", intercepted.Ref)
							require.Equal(t, expectedStage != "callback-loss", intercepted.UpstreamCompleted)
							return true
						}
					}
					return false
				}, 15*time.Second, 50*time.Millisecond, "delivery must identify this actual accepted receive and epoch")
			}
			awaitDelivery(stage, started)
			interception := intercepted
			faultAt := intercepted.At
			recoveryDeadline := faultAt.Add(recoveryBudget)
			remote := fixture.gitURL + "/" + fixture.namespace + "/gitstore-system"
			beforeSnapshot, references, err := readTranslationCapacityReferenceSnapshot(t.Context(), remote, fixture.token)
			require.NoError(t, err)
			time.Sleep(time.Second)
			var appliedBeforeRestoration translationIntegrationResource
			for _, api := range fixture.apis {
				if stage == "callback-loss" {
					_, found := readTranslationIntegrationResource(t, fixture, api, document.Name)
					require.False(t, found, "a dropped admission callback cannot have projected the new source")
				} else {
					remaining := time.Until(recoveryDeadline.Add(-trafficDuration - trafficLead))
					require.Positive(t, remaining, "replica visibility must leave five seconds inside the recovery deadline")
					require.Eventually(t, func() bool {
						observed, found := readTranslationIntegrationResource(t, fixture, api, document.Name)
						if !found {
							return false
						}
						require.NoError(t, verifyTranslationIntegrationAuthored(document.Source, "main@sha1:"+commit, observed))
						if appliedBeforeRestoration.Metadata.UID != "" {
							require.Equal(t, appliedBeforeRestoration.Metadata.UID, observed.Metadata.UID)
							require.Equal(t, appliedBeforeRestoration.Metadata.Generation, observed.Metadata.Generation)
						}
						appliedBeforeRestoration = observed
						return true
					}, remaining, 50*time.Millisecond, "a lost reply follows actual upstream admission, not immediate cross-replica visibility")
				}
			}
			var physical map[string]any
			if stage == "callback-loss" && physicalProfile != "" {
				start := time.Now().UnixMilli()
				commandContext, cancel := context.WithTimeout(t.Context(), 35*time.Second)
				defer cancel()
				command := exec.CommandContext(commandContext, "make", "chaos", "CHAOS_PROFILE="+physicalProfile)
				command.Dir, err = filepath.Abs("../..")
				require.NoError(t, err)
				directory := filepath.Join(root, "physical")
				command.Env = append(os.Environ(), "CHAOS_TRANSLATION=1", "CHAOS_RBAC=0",
					"CHAOS_RUN_ID="+ready.RunID, "CHAOS_EVIDENCE_DIR="+directory,
					"CHAOS_WINDOW_START_MILLIS="+strconv.FormatInt(start, 10),
					"CHAOS_WINDOW_END_MILLIS="+strconv.FormatInt(start+30_000, 10))
				output, err := command.CombinedOutput()
				require.NoError(t, err, "owned physical fault/restoration failed: %s", output)
				physicalDirectory := filepath.Join(directory, physicalProfile, ready.RunID)
				readPrivate(t, filepath.Join(physicalDirectory, "metadata.json"), &physical)
				captures := map[string]string{"targetBefore": "target-before.json", "targetAfter": "target-after.json"}
				if physicalProfile == "journal-interruption" {
					captures["targetDuring"] = "target-during.json"
				} else {
					captures["peerBefore"] = "peer-before.json"
					captures["peerAfter"] = "peer-after.json"
				}
				for field, filename := range captures {
					var capture []map[string]any
					readPrivate(t, filepath.Join(physicalDirectory, filename), &capture)
					require.Len(t, capture, 1, "physical recovery must retain the independently observed target or peer lifetime")
					physical[field] = capture
				}
				require.Equal(t, ready.RunID, physical["runId"])
				require.Equal(t, "physical", physical["kind"])
				require.Equal(t, "translation", physical["domain"])
				require.Equal(t, physicalProfile, physical["profile"])
				require.Equal(t, os.Getenv("CHAOS_TARGET"), physical["target"])
				require.Equal(t, true, physical["injected"])
				require.Equal(t, true, physical["restored"])
				require.Equal(t, float64(0), physical["exitCode"])
				require.Equal(t, false, physical["domainRecoveryVerified"], "injector restoration alone is not domain recovery")
				window, ok := physical["faultWindow"].(map[string]any)
				require.True(t, ok)
				require.Equal(t, true, window["confirmed"])
				require.Equal(t, float64(start), window["startMillis"])
				require.Equal(t, float64(start+30_000), window["endMillis"])
				completed, ok := window["completedMillis"].(float64)
				require.True(t, ok)
				require.GreaterOrEqual(t, completed, float64(start))
				require.LessOrEqual(t, completed, float64(start+30_000))
				awaitDelivery(stage, time.UnixMilli(int64(completed)))
			}
			var replacement *struct {
				RunID, Kind, NewCommit string
				BeforePID, AfterPID    int
				At                     time.Time
			}
			if stage == "callback-loss" && os.Getenv("TRANSLATION_TEST_POST_ACK_RESTART") == "1" {
				beforePID, err := strconv.Atoi(os.Getenv("TRANSLATION_TEST_GIT_PID"))
				require.NoError(t, err)
				require.Greater(t, beforePID, 1)
				command, err := exec.CommandContext(t.Context(), "ps", "-p", strconv.Itoa(beforePID), "-o", "comm=").Output()
				require.NoError(t, err)
				require.True(t, strings.HasSuffix(strings.TrimSpace(string(command)), "git-service"))
				requested := time.Now().UTC()
				require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "restart-request.json"), map[string]any{
					"runId": ready.RunID, "beforePID": beforePID, "newCommit": commit, "at": requested,
				}))
				require.Eventually(t, func() bool {
					_, err := os.Lstat(filepath.Join(root, "git-restored.json"))
					if os.IsNotExist(err) {
						return false
					}
					require.NoError(t, err)
					readPrivate(t, filepath.Join(root, "git-restored.json"), &replacement)
					return true
				}, 30*time.Second, 100*time.Millisecond, "owned coordinator must restore the singleton on retained storage")
				require.NotNil(t, replacement)
				require.Equal(t, ready.RunID, replacement.RunID)
				require.Equal(t, "physical", replacement.Kind)
				require.Equal(t, commit, replacement.NewCommit)
				require.Equal(t, beforePID, replacement.BeforePID)
				require.Greater(t, replacement.AfterPID, 1)
				require.NotEqual(t, replacement.BeforePID, replacement.AfterPID)
				require.False(t, replacement.At.Before(requested))
				require.False(t, replacement.At.After(time.Now().UTC()))
				require.Error(t, exec.CommandContext(t.Context(), "ps", "-p", strconv.Itoa(beforePID), "-o", "pid=").Run(),
					"the acknowledged writer must be gone before restoration is accepted")
				command, err = exec.CommandContext(t.Context(), "ps", "-p", strconv.Itoa(replacement.AfterPID), "-o", "comm=").Output()
				require.NoError(t, err)
				require.True(t, strings.HasSuffix(strings.TrimSpace(string(command)), "git-service"))
				awaitDelivery(stage, replacement.At)
			}
			restored := time.Now().UTC()
			setFault(t, "")
			awaitDelivery("", restored)
			current := awaitTranslationIntegrationResources(t, fixture, map[string][]byte{document.Name: document.Source},
				"main@sha1:"+commit, true)[document.Name]
			if stage == "reply-loss" {
				require.Equal(t, appliedBeforeRestoration.Metadata.UID, current.Metadata.UID)
				require.Equal(t, appliedBeforeRestoration.Metadata.Generation, current.Metadata.Generation,
					"ordinary replay cannot increment the already accepted source generation")
			}
			afterSnapshot, afterReferences, err := readTranslationCapacityReferenceSnapshot(t.Context(), remote, fixture.token)
			require.NoError(t, err)
			require.Equal(t, references, afterReferences, "recovery must not require another push or alter canonical history")
			trafficContext, cancelTraffic := context.WithDeadline(t.Context(), recoveryDeadline)
			defer cancelTraffic()
			recoveryReads, err := reader.recoveryReads(trafficContext, [2]string{fixture.apis[0], fixture.apis[1]},
				document, commit, current.Metadata.UID, current.Metadata.ResourceVersion)
			require.NoError(t, err)
			trafficFrom := time.Now().UTC().Add(trafficLead)
			traffic, err := reader.load(trafficContext, [2]string{fixture.apis[0], fixture.apis[1]},
				trafficFrom, trafficDuration, 20, trafficWorkers,
				[]translationCapacityReadTarget{{Document: document, Revision: "main@sha1:" + commit,
					SourceID: current.Metadata.UID, SourceVersion: current.Metadata.ResourceVersion}})
			observed := time.Now().UTC()
			trafficProof := map[string]any{
				"schemaVersion": 1, "runId": ready.RunID, "evidenceClass": "diagnostic",
				"faultAt": faultAt, "trafficFrom": trafficFrom, "trafficUntil": trafficFrom.Add(trafficDuration),
				"observed": observed, "recoveryDeadline": recoveryDeadline,
				"rate": 20, "workers": trafficWorkers, "traffic": traffic,
				"namespace": document.Namespace, "name": document.Name, "commit": commit,
				"sourceSha256": fmt.Sprintf("%x", sha256.Sum256(document.Source)),
				"sourceID":     current.Metadata.UID, "sourceVersion": current.Metadata.ResourceVersion,
				"recoveryReads": recoveryReads,
			}
			require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "traffic-"+stage+".json"), trafficProof))
			require.NoError(t, err)
			require.Equal(t, 100, traffic.Offered)
			require.Equal(t, 100, traffic.Completed)
			require.Zero(t, traffic.Dropped)
			require.Zero(t, traffic.UnexpectedErrors)
			require.False(t, observed.After(recoveryDeadline), "five seconds of correct domain traffic must fit within the recovery deadline")
			summary := map[string]any{
				"stage": stage, "kind": "simulated", "started": started, "restored": restored,
				"observed": observed, "commit": commit, "source": current,
				"sourceSha256": fmt.Sprintf("%x", sha256.Sum256(document.Source)), "referencesSha256": references,
				"referencesBeforeRestoration": references, "referencesAfterRestoration": afterReferences,
				"referenceSnapshotBeforeRestoration": beforeSnapshot, "referenceSnapshotAfterRestoration": afterSnapshot,
				"window": window, "trafficProof": trafficProof,
				"interception": interception, "delivery": intercepted,
				"physicalRestart": replacement,
				"physicalFault":   physical,
				"faultAt":         faultAt, "trafficFrom": trafficFrom, "trafficUntil": trafficFrom.Add(trafficDuration),
				"correctReads": traffic.Completed, "recoveryDeadline": recoveryDeadline,
			}
			require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "recovery-"+stage+".json"), map[string]any{
				"schemaVersion": 1, "runId": ready.RunID, "evidenceClass": "diagnostic", "fault": summary,
			}))
			summaries = append(summaries, summary)
		}) {
			return
		}
	}
	for replica, api := range fixture.apis {
		after, err := reader.baseline(t.Context(), api, fixture.namespace, 10, 100_000)
		require.NoError(t, err)
		require.Equal(t, before[replica], after, "transport recovery cannot mutate the retained Product baseline")
	}
	require.NoError(t, writeSecretCapacityPrivateJSON(filepath.Join(root, "recovery.json"), map[string]any{
		"schemaVersion": 1, "runId": ready.RunID, "scope": "native-simulated-callback-transport",
		"evidenceClass": "diagnostic", "namespace": fixture.namespace, "baseline": before, "faults": summaries,
		"measurementScope": "two actual ingress pushes; simulated callback/reply loss and any explicitly joined physical fault; exact both-API recovery without resubmission followed by five seconds of correct domain reads within 30 seconds; diagnostic, not sustained acceptance",
	}))
}

func TestTranslationNativeDuplicateAndOutOfOrderCallbacks(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CALLBACK_DRIVER") == "" {
		t.Skip("owned signed callback driver not selected")
	}
	if destination := os.Getenv("TRANSLATION_TEST_EVIDENCE_FILE"); destination != "" {
		_, err := os.Lstat(destination)
		require.True(t, os.IsNotExist(err), "retain existing callback receipts; select a fresh evidence file")
	}
	fixture, gitB := ownedTranslationNativeFixture(t)
	reader, _, _ := newTranslationCapacityReader(t, fixture)
	reader.baselinePause = 50 * time.Millisecond
	before := [2]translationCapacityBaseline{}
	for replica, api := range fixture.apis {
		var err error
		before[replica], err = reader.baseline(t.Context(), api, fixture.namespace, 10, 100_000)
		require.NoError(t, err)
	}
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Valid     map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source, err := translationIntegrationSource([]byte(corpus.Valid["product-combined"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	template, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	document, err := renderTranslationCapacityPush(translationCapacityDocument{Namespace: fixture.namespace, Name: template.Metadata.Name, Source: source},
		uniqueName("replica"), 0)
	require.NoError(t, err)
	response := fixture.query(t, fixture.apis[0], `query($namespace:String!){
		repository(by:{namespacePath:{namespace:$namespace,name:"gitstore-system"}}){metadata{uid}}}`,
		map[string]any{"namespace": fixture.namespace})
	require.Empty(t, response.Errors)
	var repository struct {
		Repository struct{ Metadata struct{ UID string } }
	}
	require.NoError(t, json.Unmarshal(response.Data, &repository))
	require.NotEmpty(t, repository.Repository.Metadata.UID)
	repositoryURI, err := base64.StdEncoding.DecodeString(repository.Repository.Metadata.UID)
	require.NoError(t, err)
	const repositoryPrefix = "gid://GitStore/Repository/"
	require.True(t, strings.HasPrefix(string(repositoryURI), repositoryPrefix))
	repositoryUID := strings.TrimPrefix(string(repositoryURI), repositoryPrefix)
	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(fixture.token, claims)
	require.NoError(t, err, "use the owned static-user JWT actor for independently signed callbacks")
	subject, err := claims.GetSubject()
	require.NoError(t, err)
	issuer, err := claims.GetIssuer()
	require.NoError(t, err)
	require.NotEmpty(t, subject)
	root := t.TempDir()
	var commits [3]string
	var current translationIntegrationResource
	requests := make([]map[string]any, 0, 2)
	for index, ingress := range []string{fixture.gitURL, gitB} {
		worker, err := newCapacityPushWorker(t.Context(), capacityPushConfig{
			remote: ingress + "/" + fixture.namespace + "/gitstore-system", namespace: fixture.namespace,
		}, root, fixture.token, index)
		require.NoError(t, err)
		if index == 0 {
			head, err := secretCapacityGit(t.Context(), worker.directory, "", "rev-parse", "HEAD")
			require.NoError(t, err)
			commits[0] = strings.TrimSpace(string(head))
		} else {
			document.Source = append(document.Source, []byte("\nUpdated accepted replica source.\n")...)
		}
		commits[index+1], err = commitTranslationIntegrationSources(t.Context(), worker, map[string][]byte{document.Path: document.Source})
		require.NoError(t, err)
		_, err = secretCapacityGit(t.Context(), worker.directory, fixture.token, "push", "--quiet", "origin", "HEAD:refs/heads/main")
		require.NoError(t, err)
		resources := awaitTranslationIntegrationResources(t, fixture, map[string][]byte{document.Name: document.Source},
			"main@sha1:"+commits[index+1], true)
		previous := current
		current = resources[document.Name]
		if index == 1 {
			require.Equal(t, previous.Metadata.UID, current.Metadata.UID)
			require.Equal(t, previous.Metadata.Generation+1, current.Metadata.Generation)
		}
		ref := map[string]any{"refName": "refs/heads/main", "oldCommitSha": commits[index], "newCommitSha": commits[index+1]}
		requests = append(requests, map[string]any{
			"repositoryId": repositoryUID, "refName": "refs/heads/main",
			"oldCommitSha": commits[index], "newCommitSha": commits[index+1], "changedPaths": []string{document.Path},
			"authorization": map[string]any{
				"repositoryId": repositoryUID, "receiveId": "translation-replay-" + commits[index+1],
				"actor": map[string]any{"subject": subject, "issuer": issuer, "authMethod": "static-users"},
				"refs":  []any{ref},
			},
		})
	}
	callbackFile := filepath.Join(root, "callbacks.json")
	require.NoError(t, writeSecretCapacityPrivateJSON(callbackFile, map[string]any{"namespace": fixture.namespace, "requests": requests}))
	remote := fixture.gitURL + "/" + fixture.namespace + "/gitstore-system"
	references, err := readTranslationCapacityReferences(t.Context(), remote, fixture.token)
	require.NoError(t, err)
	command := exec.CommandContext(t.Context(), os.Getenv("TRANSLATION_TEST_CALLBACK_DRIVER"),
		"-test.v", "-test.timeout=1m", "-test.run=^TestTranslationNativeCallbackDelivery$")
	command.Env = append(os.Environ(), "TRANSLATION_TEST_CALLBACK_RUN=1", "TRANSLATION_TEST_CALLBACK_FILE="+callbackFile)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "signed callback delivery failed: %s", output)
	for replica, api := range fixture.apis {
		observed, found := readTranslationIntegrationResource(t, fixture, api, document.Name)
		require.True(t, found)
		require.NoError(t, verifyTranslationIntegrationResource(document.Source, current.Metadata.Revision, observed, true))
		require.Equal(t, current, observed, "delayed/duplicate callbacks must not advance any accepted source or status field")
		after, err := reader.baseline(t.Context(), api, fixture.namespace, 10, 100_000)
		require.NoError(t, err)
		require.Equal(t, before[replica], after, "callback replay cannot mutate retained Product identity/material/resolved data")
	}
	afterReferences, err := readTranslationCapacityReferences(t.Context(), remote, fixture.token)
	require.NoError(t, err)
	require.Equal(t, references, afterReferences, "callback replay cannot alter canonical refs or reachable history")
	if destination := os.Getenv("TRANSLATION_TEST_EVIDENCE_FILE"); destination != "" {
		require.NoError(t, writeSecretCapacityPrivateJSON(destination, map[string]any{
			"schemaVersion": 1, "scope": "native-duplicate-out-of-order-callbacks",
			"namespace": fixture.namespace, "apis": fixture.apis, "commits": commits,
			"source": current, "sourceSha256": fmt.Sprintf("%x", sha256.Sum256(document.Source)),
			"baseline": before, "referencesSha256": references, "callbacks": 4,
			"measurementScope": "two actual ingress pushes and four signed old/current callback deliveries; no process replacement or sustained acceptance",
		}))
	}
}

type translationNativeBinding struct {
	TranslationID, SourceVersion, BindingVersion, ReferenceEpoch, NamespaceUID string
	BoundUID                                                                   *string
}

func TestTranslationNativeReplicaStatusAndBinding(t *testing.T) {
	fixture, _ := ownedTranslationNativeFixture(t)
	controllers := [2]rbacNativeFixture{fixture, fixture}
	controllers[0].token = rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_CONTROLLER_TOKEN_FILE"))
	controllers[1].token = rbacNativeToken(t, os.Getenv("TRANSLATION_TEST_CONTROLLER_TOKEN_FILE_B"))
	require.True(t, controllers[0].token != controllers[1].token, "use independently issued enrolled-controller credentials")
	watch, err := newCapacityPushWatch(t.Context(), capacityPushConfig{
		kind: "translation", namespace: fixture.namespace, runID: "retained-baseline-probe",
		verifyAPIA: fixture.apis[0], verifyAPIB: fixture.apis[1], duration: time.Minute, interval: time.Second,
	}, fixture.token)
	require.NoError(t, err)
	t.Cleanup(watch.close)
	raw, err := os.ReadFile("../../gitstore-api/internal/cataloggrpc/testdata/translation/corpus.json")
	require.NoError(t, err)
	var corpus struct {
		Namespace string
		Valid     map[string]string
		Targets   map[string]string
	}
	require.NoError(t, json.Unmarshal(raw, &corpus))
	source, err := translationIntegrationSource([]byte(corpus.Valid["product-field"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	expected, err := translationIntegrationExpected(source)
	require.NoError(t, err)
	current, found := readTranslationIntegrationResource(t, fixture, fixture.apis[0], expected.Metadata.Name)
	require.True(t, found, "first run native ingestion in the selected owned namespace")
	require.NoError(t, verifyTranslationIntegrationResource(source, current.Metadata.Revision, current, true))
	reader := translationCapacityReader{
		client: &http.Client{Timeout: 10 * time.Second}, token: fixture.token, controllerToken: controllers[0].token,
		query: translationIntegrationQuery(t, "translation"), snapshot: translationIntegrationQuery(t, "snapshot"),
		baselineQuery: translationIntegrationQuery(t, "baseline"), baselinePause: time.Second,
	}
	document := translationCapacityDocument{
		Namespace: fixture.namespace, Name: current.Metadata.Name, Source: source,
		translationCapacityContent: translationCapacityContent{SourceDigest: fmt.Sprintf("%x", sha256.Sum256(source))},
	}
	for _, api := range fixture.apis {
		observation, err := reader.observe(t.Context(), api, document, current.Metadata.Revision)
		require.NoError(t, err)
		require.Equal(t, current.Metadata.UID, observation.UID)
		require.Equal(t, current.Metadata.ResourceVersion, observation.Version)
	}
	var baseline translationCapacityBaseline
	for index, api := range fixture.apis {
		sample, err := reader.baseline(t.Context(), api, fixture.namespace, 1, 100)
		require.NoError(t, err)
		require.Positive(t, sample.Products)
		if index == 0 {
			baseline = sample
		} else {
			require.Equal(t, baseline, sample)
		}
	}
	baselineRemote := fixture.gitURL + "/" + fixture.namespace + "/gitstore-system"
	baselineRefs, err := readTranslationCapacityReferences(t.Context(), baselineRemote, fixture.token)
	require.NoError(t, err)
	target, err := translationIntegrationSource([]byte(corpus.Targets["Product"]), corpus.Namespace, fixture.namespace)
	require.NoError(t, err)
	before, found := readTranslationIntegrationTarget(t, fixture, controllers[0], fixture.apis[0], target)
	require.True(t, found)
	var snapshot struct {
		TranslationValidationSnapshot struct {
			Binding     translationNativeBinding
			Observation *struct {
				ObservedGeneration  int64
				LastAppliedRevision string
				Conditions          []map[string]any
				Resolved            map[string]any
			}
		}
	}
	response := controllers[0].query(t, fixture.apis[0], translationIntegrationQuery(t, "snapshot"), map[string]any{
		"namespace": fixture.namespace, "name": current.Metadata.Name,
		"sourceID": current.Metadata.UID, "sourceVersion": current.Metadata.ResourceVersion,
	})
	require.Empty(t, response.Errors)
	require.NoError(t, json.Unmarshal(response.Data, &snapshot))
	binding, observation := snapshot.TranslationValidationSnapshot.Binding, snapshot.TranslationValidationSnapshot.Observation
	require.NotNil(t, binding.BoundUID)
	require.NotEmpty(t, *binding.BoundUID)
	require.NotNil(t, observation)
	require.NotNil(t, observation.Resolved)

	var results [2]gqlResponse
	t.Run("idempotent-binding-replicas", func(t *testing.T) {
		for i := range controllers {
			t.Run(fmt.Sprintf("api-%d", i), func(t *testing.T) {
				t.Parallel()
				results[i] = controllers[i].query(t, fixture.apis[i], translationIntegrationQuery(t, "bind"), map[string]any{
					"input": map[string]any{
						"namespace": fixture.namespace, "name": current.Metadata.Name,
						"translationID": binding.TranslationID, "sourceVersion": binding.SourceVersion,
						"bindingVersion": binding.BindingVersion, "referenceEpoch": binding.ReferenceEpoch,
						"targetUID": *binding.BoundUID,
					},
				})
			})
		}
	})
	for _, response := range results {
		require.Empty(t, response.Errors)
		var payload struct {
			BindTranslationTarget struct{ Binding translationNativeBinding }
		}
		require.NoError(t, json.Unmarshal(response.Data, &payload))
		require.Equal(t, binding, payload.BindTranslationTarget.Binding, "duplicate binding must not advance its version or reference epoch")
	}
	for _, field := range []string{"sourceVersion", "bindingVersion", "referenceEpoch", "targetUID"} {
		for i := range controllers {
			input := map[string]any{
				"namespace": fixture.namespace, "name": current.Metadata.Name,
				"translationID": binding.TranslationID, "sourceVersion": binding.SourceVersion,
				"bindingVersion": binding.BindingVersion, "referenceEpoch": binding.ReferenceEpoch,
				"targetUID": *binding.BoundUID,
			}
			input[field] = "00000000-0000-4000-8000-000000000002"
			response := controllers[i].query(t, fixture.apis[i], translationIntegrationQuery(t, "bind"), map[string]any{"input": input})
			rbacNativeErrorCode(t, response, "CONFLICT")
		}
	}
	t.Run("status-CAS-replicas", func(t *testing.T) {
		for i := range controllers {
			t.Run(fmt.Sprintf("api-%d", i), func(t *testing.T) {
				t.Parallel()
				conditions := make([]map[string]any, len(observation.Conditions))
				for index, original := range observation.Conditions {
					conditions[index] = make(map[string]any, len(original))
					for key, value := range original {
						conditions[index][key] = value
					}
					conditions[index]["message"] = fmt.Sprintf("Owned replica %d validated accepted source version %s", i, current.Metadata.ResourceVersion)
				}
				results[i] = controllers[i].query(t, fixture.apis[i], translationIntegrationQuery(t, "status"), map[string]any{
					"input": map[string]any{
						"namespace": fixture.namespace, "name": current.Metadata.Name, "uid": current.Metadata.UID,
						"resourceVersion":    current.Metadata.ResourceVersion,
						"observedGeneration": observation.ObservedGeneration, "lastAppliedRevision": observation.LastAppliedRevision,
						"conditions": conditions, "resolved": observation.Resolved,
					},
				})
			})
		}
	})
	successes := 0
	for _, response := range results {
		if len(response.Errors) == 0 {
			successes++
			var payload struct {
				UpdateTranslationStatus struct {
					Translation *translationIntegrationResource
				}
			}
			require.NoError(t, json.Unmarshal(response.Data, &payload))
			winner := payload.UpdateTranslationStatus.Translation
			require.NotNil(t, winner)
			require.Equal(t, current.Metadata.UID, winner.Metadata.UID)
			require.Equal(t, current.Metadata.Generation, winner.Metadata.Generation)
			require.NotEqual(t, current.Metadata.ResourceVersion, winner.Metadata.ResourceVersion)
			require.Equal(t, current.Metadata.Revision, winner.Status.LastAppliedRevision)
			require.Equal(t, current.Metadata.Generation, winner.Status.ObservedGeneration)
			ready := false
			for _, condition := range winner.Status.Conditions {
				ready = ready || condition.Type == "Ready" && condition.Status == "TRUE"
			}
			require.True(t, ready, "the winning CAS response must retain a guarded current Ready proof")
		} else {
			rbacNativeErrorCode(t, response, "CONFLICT")
		}
	}
	require.Equal(t, 1, successes, "exactly one distinct observation may win the same source-version CAS")
	unchanged := awaitTranslationIntegrationResources(t, fixture, map[string][]byte{current.Metadata.Name: source}, current.Metadata.Revision, true)
	require.Equal(t, current.Metadata.UID, unchanged[current.Metadata.Name].Metadata.UID)
	require.Equal(t, current.Metadata.Generation, unchanged[current.Metadata.Name].Metadata.Generation)
	for i := range controllers {
		response := controllers[i].query(t, fixture.apis[i], translationIntegrationQuery(t, "binding"),
			map[string]any{"namespace": fixture.namespace, "name": current.Metadata.Name})
		require.Empty(t, response.Errors)
		var after struct{ TranslationBinding translationNativeBinding }
		require.NoError(t, json.Unmarshal(response.Data, &after))
		require.Equal(t, binding.TranslationID, after.TranslationBinding.TranslationID)
		require.Equal(t, binding.ReferenceEpoch, after.TranslationBinding.ReferenceEpoch)
		require.Equal(t, binding.BoundUID, after.TranslationBinding.BoundUID)
		observed, found := readTranslationIntegrationTarget(t, fixture, controllers[i], fixture.apis[i], target)
		require.True(t, found)
		require.Equal(t, before, observed, "binding/status races must preserve target authored and resolved material")
		afterBaseline, err := reader.baseline(t.Context(), fixture.apis[i], fixture.namespace, 1, 100)
		require.NoError(t, err)
		require.Equal(t, baseline, afterBaseline, "binding/status races cannot mutate retained Product identity or material")
	}
	afterRefs, err := readTranslationCapacityReferences(t.Context(), baselineRemote, fixture.token)
	require.NoError(t, err)
	require.Equal(t, baselineRefs, afterRefs, "binding/status races cannot change retained canonical refs or reachable Git history")
	_, err = watch.verified("cpd-retained-baseline-probe-unoffered", current.Metadata.UID, "", time.Now())
	require.NoError(t, err, "both continuous Translation watch streams must remain valid")
}
