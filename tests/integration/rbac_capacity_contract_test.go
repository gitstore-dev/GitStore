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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRBACCapacityPropagationUsesTheOriginalAcknowledgementAndBothReplicas(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			var calls [2]atomic.Int64
			acknowledgedAt := time.Now().Add(-time.Second)
			latencies, err := observeRBACCapacityPropagation(t.Context(), acknowledgedAt, allowed,
				func(_ context.Context, replica, attempt int) string {
					calls[replica].Add(1)
					if (attempt >= replica) == allowed {
						return "allow"
					}
					return "deny"
				})
			require.NoError(t, err)
			require.EqualValues(t, 1, calls[0].Load())
			require.EqualValues(t, 2, calls[1].Load(), "one converged replica must not hide another replica's stale verdict")
			for _, latency := range latencies {
				require.GreaterOrEqual(t, latency, time.Second, "readback must not restart the successful-push clock")
				require.LessOrEqual(t, latency, 5*time.Second)
			}
		})
	}
}

func TestRBACCapacityPropagationRejectsLateSuccessAndUnavailableDenials(t *testing.T) {
	for _, outcome := range []string{"unavailable", "wrong", "unknown"} {
		t.Run(outcome, func(t *testing.T) {
			_, err := observeRBACCapacityPropagation(t.Context(), time.Now(), false,
				func(context.Context, int, int) string { return outcome })
			require.Error(t, err, "transport, malformed and fail-closed responses are not successful revocations")
		})
	}
	t.Run("expired-original-clock", func(t *testing.T) {
		_, err := observeRBACCapacityPropagation(t.Context(), time.Now().Add(-6*time.Second), true,
			func(context.Context, int, int) string {
				t.Error("expired propagation work must not start another observation")
				return "allow"
			})
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
	t.Run("late-success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		_, err := observeRBACCapacityPropagation(ctx, time.Now(), true,
			func(context.Context, int, int) string {
				cancel()
				return "allow"
			})
		require.ErrorIs(t, err, context.Canceled)
	})
	t.Run("one-replica-never-converges", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
		defer cancel()
		var calls [2]atomic.Int64
		_, err := observeRBACCapacityPropagation(ctx, time.Now(), true,
			func(_ context.Context, replica, attempt int) string {
				calls[replica].Add(1)
				if replica == 0 {
					return "allow"
				}
				return "deny"
			})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.EqualValues(t, 1, calls[0].Load())
		require.EqualValues(t, 1, calls[1].Load())
	})
	for _, acknowledgedAt := range []time.Time{{}, time.Now().Add(time.Hour)} {
		_, err := observeRBACCapacityPropagation(t.Context(), acknowledgedAt, true,
			func(context.Context, int, int) string {
				t.Error("unattributable admission clocks must fail before observation")
				return "allow"
			})
		require.Error(t, err)
	}
}

func TestRBACCapacityPropagationSelectsAnIndependentOrganizationGrant(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-propagation", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	index, err := rbacCapacityPropagationNamespace(plan, 4, []string{"reader-a", "reader-b"})
	require.NoError(t, err)
	require.Equal(t, 2, index, "skip the protected personal owner and every overlapping Group grantee")
	for _, tc := range []struct {
		count  int
		groups []string
	}{
		{1, nil}, {1001, nil}, {2, []string{"reader-b"}},
		{4, plan.Subjects}, {4, []string{"unknown"}}, {4, []string{"reader-a", "reader-a"}},
	} {
		_, err := rbacCapacityPropagationNamespace(plan, tc.count, tc.groups)
		require.Error(t, err, "a revocation must not assume removal of an independent or protected grant")
	}
}

func TestRBACCapacitySourceModelsRejectTamperingGapsAndUncommittedTails(t *testing.T) {
	resource := json.RawMessage(`{"kind":"Role","metadata":{"name":"owned-role"},"spec":{"rules":[]}}`)
	header := rbacCapacitySourceHeader{ModelVersion: "v1", State: json.RawMessage(`{"tierID":"owned"}`),
		Origin: json.RawMessage(`{"namespace":"owned"}`)}
	canonical, err := json.Marshal(header)
	require.NoError(t, err)
	digest := sha256.Sum256(append(append(canonical, 0), resource...))
	header.ContentDigest = hex.EncodeToString(digest[:])
	var stream bytes.Buffer
	encoder := json.NewEncoder(&stream)
	require.NoError(t, encoder.Encode(map[string]any{"type": "begin", "snapshot": header, "resourceCount": 1}))
	require.NoError(t, encoder.Encode(map[string]any{"type": "resource", "resource": resource}))
	incomplete := stream.String()
	require.NoError(t, encoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
	complete := stream.String()
	t.Run("partial-live-lines", func(t *testing.T) {
		lines := strings.Split(strings.TrimSuffix(complete, "\n"), "\n")
		prefix := ""
		for index, line := range lines {
			partial := complete + prefix + line[:len(line)-1]
			t.Run([]string{"begin", "resource", "end"}[index], func(t *testing.T) {
				accepted := 0
				visit := func(rbacCapacitySourceHeader, []json.RawMessage) error {
					accepted++
					return nil
				}
				require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(partial), visit), io.ErrUnexpectedEOF)
				require.Equal(t, 1, accepted, "an unfinished appended line cannot register another source model")
				for _, suffix := range []string{"\n", "\n" + complete} {
					accepted = 0
					err := walkRBACCapacitySourceModels(strings.NewReader(partial+suffix), visit)
					require.Error(t, err)
					require.NotErrorIs(t, err, io.ErrUnexpectedEOF, "malformed committed or interior lines must still fail")
					require.Equal(t, 1, accepted)
				}
			})
			prefix += line + "\n"
		}
		accepted := 0
		require.NoError(t, walkRBACCapacitySourceModels(strings.NewReader(strings.TrimSuffix(complete, "\n")),
			func(rbacCapacitySourceHeader, []json.RawMessage) error {
				accepted++
				return nil
			}))
		require.Equal(t, 1, accepted, "a complete hashed final model remains valid without a trailing newline")
		err := walkRBACCapacitySourceModels(strings.NewReader(complete+`{"type":!`),
			func(rbacCapacitySourceHeader, []json.RawMessage) error { return nil })
		require.Error(t, err)
		require.NotErrorIs(t, err, io.ErrUnexpectedEOF, "invalid syntax is not an unfinished valid JSON prefix")
	})
	for _, tc := range []struct {
		name, input string
		accepted    int
		wantError   bool
	}{
		{"complete", complete, 1, false},
		{"altered-policy", strings.Replace(complete, `"rules":[]`, `"rules":[{"verbs":["write"]}]`, 1), 0, true},
		{"missing-end", incomplete, 0, true},
		{"missing-resource", strings.Replace(complete, `"resourceCount":1`, `"resourceCount":2`, 1), 0, true},
		{"overlapping-header", incomplete + complete, 0, true},
		{"complete-then-live-tail", complete + incomplete, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			accepted := 0
			err := walkRBACCapacitySourceModels(strings.NewReader(tc.input), func(got rbacCapacitySourceHeader, resources []json.RawMessage) error {
				accepted++
				require.Equal(t, header, got)
				require.Equal(t, []json.RawMessage{resource}, resources)
				return nil
			})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.accepted, accepted, "only complete independently hashed models may reach the policy oracle")
		})
	}
	rejected := errors.New("source policy differs from independently authored history")
	require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(complete), func(rbacCapacitySourceHeader, []json.RawMessage) error {
		return rejected
	}), rejected)
	require.ErrorIs(t, walkRBACCapacitySourceModels(strings.NewReader(incomplete), func(rbacCapacitySourceHeader, []json.RawMessage) error {
		t.Fatal("incomplete evidence cannot register a policy vector")
		return nil
	}), io.ErrUnexpectedEOF)
}

func TestRBACCapacitySeedReadbackBoundsAdmissionWaitAndRejectsLateSuccess(t *testing.T) {
	t.Run("canceled-admission-wait", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, awaitRBACCapacitySeedReadback(ctx, make(chan time.Time), func() bool {
			t.Fatal("canceled work must not start an observation")
			return true
		}), context.Canceled)
	})
	t.Run("late-success", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		slots := make(chan time.Time, 1)
		slots <- time.Now()
		require.ErrorIs(t, awaitRBACCapacitySeedReadback(ctx, slots, func() bool {
			cancel()
			return true
		}), context.Canceled)
	})
	t.Run("retry-before-success", func(t *testing.T) {
		slots := make(chan time.Time, 2)
		slots <- time.Now()
		slots <- time.Now()
		observations := 0
		require.NoError(t, awaitRBACCapacitySeedReadback(t.Context(), slots, func() bool {
			observations++
			return observations == 2
		}))
		require.Equal(t, 2, observations)
	})
}

func TestRBACCapacitySeedAdmissionsPreserveAcknowledgementClocks(t *testing.T) {
	first := time.Unix(1000, 0).UTC()
	record := rbacCapacitySeedReceipt{}
	require.NoError(t, record.acknowledge(strings.Repeat("a", 40), first, 10))
	encoded, err := json.Marshal(record)
	require.NoError(t, err)
	var restored rbacCapacitySeedReceipt
	require.NoError(t, json.Unmarshal(encoded, &restored))
	require.Equal(t, first, restored.Admissions[0].AcknowledgedAt,
		"private receipts must retain admission time, not substitute a later readback time")
	require.NoError(t, restored.acknowledge(strings.Repeat("b", 40), first.Add(time.Second), 20))
	require.Equal(t, first, restored.Admissions[0].AcknowledgedAt)
	require.Equal(t, "main@sha1:"+strings.Repeat("b", 40), restored.GitRevision)
	for _, tc := range []struct {
		name, sha string
		at        time.Time
		resources int
	}{
		{"duplicate-commit", strings.Repeat("a", 40), first.Add(2 * time.Second), 30},
		{"missing-clock", strings.Repeat("c", 40), time.Time{}, 30},
		{"reordered-clock", strings.Repeat("c", 40), first, 30},
		{"repeated-clock", strings.Repeat("c", 40), first.Add(time.Second), 30},
		{"unidentified-commit", "not-a-commit", first.Add(2 * time.Second), 30},
		{"empty-batch", strings.Repeat("c", 40), first.Add(2 * time.Second), 20},
		{"oversized-batch", strings.Repeat("c", 40), first.Add(2 * time.Second), 31},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Error(t, restored.acknowledge(tc.sha, tc.at, tc.resources))
			after, err := json.Marshal(restored)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after), "invalid admission evidence must not alter history")
		})
	}
	for index := 2; index < 6; index++ {
		require.NoError(t, restored.acknowledge(strings.Repeat(string(rune('a'+index)), 40),
			first.Add(time.Duration(index)*time.Second), min(10*(index+1), 59)))
	}
	require.Len(t, restored.Admissions, 6)
	require.Equal(t, 59, restored.Admissions[5].Resources)
	require.Error(t, restored.acknowledge(strings.Repeat("1", 40), first.Add(6*time.Second), 60),
		"the seed receipt must not grow beyond its six declared imports")
}

func TestRBACCapacitySourceBindingRequiresTheAdmittedRoleIncarnation(t *testing.T) {
	for _, tc := range []struct {
		name, role, uid string
		observed        int
		ready, invalid  bool
	}{
		{"resolved", "owned-role", "admitted-uid", 1, true, false},
		{"pending-generation", "owned-role", "admitted-uid", 0, false, false},
		{"pending-resolution", "owned-role", "", 1, false, false},
		{"different-incarnation", "owned-role", "other-uid", 1, false, true},
		{"deliberately-unresolved", "capacity-unresolved", "", 1, true, false},
		{"invented-resolution", "capacity-unresolved", "invented-uid", 1, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resource := map[string]any{
				"metadata": map[string]any{"generation": 1},
				"spec":     map[string]any{"roleRef": map[string]any{"kind": "Role", "name": tc.role}},
				"status":   map[string]any{"observedGeneration": tc.observed},
			}
			if tc.uid != "" {
				resource["status"].(map[string]any)["resolved"] = map[string]any{"roleRef": map[string]any{
					"kind": "Role", "name": tc.role, "uid": tc.uid, "observedGeneration": tc.observed,
				}}
			}
			encoded, err := json.Marshal(resource)
			require.NoError(t, err)
			ready, err := rbacCapacitySeedBindingResolved(encoded, map[string]string{"owned-role": "admitted-uid"}, 1)
			if tc.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.ready, ready)
		})
	}
}

func TestRBACCapacitySourceBindingPinsOriginalReferenceGenerationAcrossSubjectEdits(t *testing.T) {
	for _, generation := range []int{2, 3} {
		for _, reference := range []int{1, 2} {
			t.Run(fmt.Sprintf("binding=%d/reference=%d", generation, reference), func(t *testing.T) {
				resource := map[string]any{
					"metadata": map[string]any{"generation": generation},
					"spec":     map[string]any{"roleRef": map[string]any{"kind": "Role", "name": "owned-role"}},
					"status": map[string]any{
						"observedGeneration": generation,
						"resolved": map[string]any{"roleRef": map[string]any{
							"kind": "Role", "name": "owned-role", "uid": "admitted-uid", "observedGeneration": reference,
						}},
					},
				}
				encoded, err := json.Marshal(resource)
				require.NoError(t, err)
				ready, err := rbacCapacitySeedBindingResolved(encoded, map[string]string{"owned-role": "admitted-uid"}, 1)
				require.NoError(t, err)
				require.Equal(t, reference == 1, ready,
					"subject edits advance the binding generation, not its immutable role reference's pinned resolution generation")
			})
		}
	}
}

func TestRBACCapacityNativeProbeTracesRequireExactRequestAndSourceJoins(t *testing.T) {
	expected := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "product.list", Allowed: true},
	}
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{
		{TierUID: "root-uid", GitRevision: "root-commit", Digest: "root-digest"}:                {Namespace: "root", Root: true},
		{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-digest"}: {Namespace: "owned-namespace"},
	}
	decision := `{"request_id":"owned-request","action":"product.list","subject":"ordinary-reader",
		"resource_namespace":"owned-namespace","resource_kind":"product","outcome":"allow","provider":"opa",
		"authorization_tiers":[
			{"tierID":"root-uid","gitRevision":"root-commit","contentDigest":"root-digest"},
			{"tierID":"namespace-uid","gitRevision":"namespace-commit","contentDigest":"namespace-digest"}]}`
	// Each structured decision occupies one physical log line.
	decision = strings.ReplaceAll(decision, "\n", "") + "\n"
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"complete", "native test preamble\n" + decision, true},
		{"missing-decision", "", false},
		{"different-subject", strings.Replace(decision, "ordinary-reader", "administrator", 1), false},
		{"different-namespace", strings.Replace(decision, "owned-namespace", "other-namespace", 1), false},
		{"different-verdict", strings.Replace(decision, `"outcome":"allow"`, `"outcome":"deny"`, 1), false},
		{"unknown-source", strings.Replace(decision, "namespace-digest", "unverified-digest", 1), false},
		{"duplicate-decision", decision + decision, false},
		{"cross-tier-source", strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(decision,
			"namespace-uid", "root-uid"), "namespace-commit", "root-commit"), "namespace-digest", "root-digest"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkRBACCapacityProbeTraces(strings.NewReader(tc.input), expected, models)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	denied := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "product.list"},
	}
	require.NoError(t, checkRBACCapacityProbeTraces(strings.NewReader(
		strings.Replace(decision, `"outcome":"allow"`, `"outcome":"deny"`, 1)), denied, models))
	role := map[string]rbacCapacityProbeExpectation{
		"owned-request": {Subject: "ordinary-reader", Namespace: "owned-namespace", Action: "role.list", Allowed: true},
	}
	require.NoError(t, checkRBACCapacityProbeTraces(strings.NewReader(strings.ReplaceAll(
		strings.ReplaceAll(decision, "product.list", "role.list"), `"resource_kind":"product"`, `"resource_kind":"role"`)), role, models))
}

func TestRBACCapacityObservedTracesCannotRedefineCommittedPolicyOrRestartClocks(t *testing.T) {
	now := time.Unix(1000, 0)
	root := rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "root-commit", Digest: "verified-root"}
	grant := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "grant", Digest: "verified-grant"}
	revoke := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "revoke", Digest: "verified-revoke"}
	regrant := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "regrant", Digest: "verified-regrant"}
	alias := grant
	alias.Digest = "verified-status-alias"
	oracle := newRBACCapacityOracle(5 * time.Second)
	for index, revision := range []rbacCapacityPolicyRevision{grant, revoke, regrant} {
		require.NoError(t, oracle.commit(rbacCapacityPolicyVector{Cluster: root, Namespace: revision},
			now, now.Add(time.Duration(index)*time.Second), map[string]bool{"reader/product.list": index != 1}))
	}
	require.NoError(t, oracle.commit(rbacCapacityPolicyVector{Cluster: root, Namespace: alias}, now, now,
		map[string]bool{"reader/product.list": true}))
	models := map[rbacCapacityPolicyRevision]rbacCapacityProbeSource{root: {Namespace: "root", Root: true}}
	for _, revision := range []rbacCapacityPolicyRevision{grant, revoke, regrant, alias} {
		models[revision] = rbacCapacityProbeSource{Namespace: "owned-namespace"}
	}
	for _, tc := range []struct {
		name     string
		revision rbacCapacityPolicyRevision
		allowed  bool
		at       time.Time
		valid    bool
	}{
		{"original-grant", grant, true, now, true},
		{"bounded-old-alias", alias, true, now.Add(3 * time.Second), true},
		{"revoke", revoke, false, now.Add(time.Second), true},
		{"regrant", regrant, true, now.Add(3 * time.Second), true},
		{"wrong-verdict-with-matching-HTTP", revoke, true, now.Add(3 * time.Second), false},
		{"late-original-alias", alias, true, now.Add(7 * time.Second), false},
		{"future-policy", regrant, true, now, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome := "deny"
			if tc.allowed {
				outcome = "allow"
			}
			decision, err := json.Marshal(map[string]any{
				"request_id": "observed", "action": "product.list", "subject": "reader",
				"resource_namespace": "owned-namespace", "resource_kind": "product", "outcome": outcome, "provider": "opa",
				"authorization_tiers": []map[string]string{
					{"tierID": root.TierUID, "gitRevision": root.GitRevision, "contentDigest": root.Digest},
					{"tierID": tc.revision.TierUID, "gitRevision": tc.revision.GitRevision, "contentDigest": tc.revision.Digest},
				},
			})
			require.NoError(t, err)
			expected := map[string]rbacCapacityProbeExpectation{"observed": {
				Subject: "reader", Namespace: "owned-namespace", Action: "product.list", Allowed: tc.allowed, ObservedAt: tc.at,
			}}
			called := 0
			err = checkRBACCapacityObservedTraces(bytes.NewReader(append(decision, '\n')), expected, models,
				func(vector rbacCapacityPolicyVector, id string, allowed bool) error {
					called++
					probe := expected[id]
					return oracle.check(vector, probe.Subject+"/"+probe.Action, allowed, probe.ObservedAt)
				})
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err, "matching server and HTTP outcomes cannot author their own expected policy")
			}
			require.Equal(t, 1, called, "every joined ordinary response must reach the independent committed oracle")
		})
	}
}

func TestRBACCapacityDynamicHistoryJoinsSealedSourcesAndSkipsPendingAliases(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-history", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	receipt := rbacCapacitySeedReceipt{
		Namespace: plan.namespaceName(1), NamespaceNodeUID: base64.StdEncoding.EncodeToString([]byte("gid://GitStore/Namespace/namespace-uid")),
		Roles: make(map[string]string), Bindings: make(map[string]string),
	}
	for _, resource := range plan.namespace(1).Resources {
		name := resource["metadata"].(map[string]any)["name"].(string)
		uids := receipt.Roles
		if resource["kind"] == "RoleBinding" {
			uids = receipt.Bindings
		}
		uids[name] = "uid-" + name
	}
	var sources, decisions bytes.Buffer
	sourceEncoder := json.NewEncoder(&sources)
	writeSource := func(origin, state map[string]any, resources []json.RawMessage) rbacCapacityPolicyRevision {
		header := rbacCapacitySourceHeader{ModelVersion: "v1"}
		var err error
		header.Origin, err = json.Marshal(origin)
		require.NoError(t, err)
		header.State, err = json.Marshal(state)
		require.NoError(t, err)
		canonical, err := json.Marshal(header)
		require.NoError(t, err)
		hash := sha256.New()
		_, _ = hash.Write(canonical)
		for _, resource := range resources {
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write(resource)
		}
		header.ContentDigest = hex.EncodeToString(hash.Sum(nil))
		require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "begin", "snapshot": header, "resourceCount": len(resources)}))
		for _, resource := range resources {
			require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "resource", "resource": resource}))
		}
		require.NoError(t, sourceEncoder.Encode(map[string]any{"type": "end", "digest": header.ContentDigest}))
		return rbacCapacityPolicyRevision{
			TierUID: state["tierID"].(string), GitRevision: origin["gitRevision"].(string), Digest: header.ContentDigest,
		}
	}
	root := rbacCapacityRootPolicy{NamespaceUID: "root-uid", BindingUID: "admin-binding-uid", AdminSubject: "bootstrap-admin"}
	var err error
	root.TreeDigest, err = rbacCapacityRootTreeDigest(root.AdminSubject)
	require.NoError(t, err)
	binding := rbacCapacityInitialRootManifest(root.AdminSubject)
	binding["metadata"].(map[string]any)["uid"] = root.BindingUID
	binding["metadata"].(map[string]any)["generation"] = 1
	binding["status"] = map[string]any{"observedGeneration": 1, "resolved": map[string]any{
		"roleRef": map[string]any{"kind": "ClusterRole", "name": "cluster-admin", "uid": "builtin-admin", "observedGeneration": 1},
	}}
	encoded, err := json.Marshal(binding)
	require.NoError(t, err)
	cluster := writeSource(map[string]any{
		"namespace": "gitstore-system", "namespaceUID": root.NamespaceUID, "gitRevision": "root-commit",
		"root": true, "active": true, "treeDigest": root.TreeDigest,
	}, map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": 1}, []json.RawMessage{encoded})
	var history []rbacCapacityAuthoredAdmission
	var pendingRevision, staleGeneration, resolvedRevocation rbacCapacityPolicyRevision
	expected := make(map[string]rbacCapacityProbeExpectation)
	for step, allowed := range []bool{true, false, true} {
		namespace := plan.namespace(1)
		if !allowed {
			namespace.Resources[10]["spec"].(map[string]any)["subjects"] = []any{
				map[string]any{"kind": "User", "name": plan.Prefix + "-filler-propagation"},
			}
		}
		admitted := receipt
		admitted.GitRevision = fmt.Sprintf("main@sha1:%040d", step+1)
		at := time.Unix(1000+int64(step), 0)
		history = append(history, rbacCapacityAuthoredAdmission{
			Namespace: namespace, Receipt: admitted, AcknowledgedAt: at,
			Cases:       map[string]bool{"reader-b/product.list": allowed},
			Generations: map[string]int64{"Role/capacity-role-00": 1, "RoleBinding/capacity-binding-00": int64(step + 1)},
			References:  map[string]int64{"RoleBinding/capacity-binding-00": 1},
		})
		var resources []json.RawMessage
		for _, resource := range namespace.Resources {
			data, err := json.Marshal(resource)
			require.NoError(t, err)
			var source map[string]any
			require.NoError(t, json.Unmarshal(data, &source))
			metadata := source["metadata"].(map[string]any)
			name := metadata["name"].(string)
			generation := 1
			uids := receipt.Roles
			if resource["kind"] == "RoleBinding" {
				uids = receipt.Bindings
				if name == "capacity-binding-00" {
					generation += step
				}
				status := map[string]any{"observedGeneration": generation}
				ref := source["spec"].(map[string]any)["roleRef"].(map[string]any)
				if ref["name"] != "capacity-unresolved" {
					roleUID := receipt.Roles[ref["name"].(string)]
					if ref["kind"] == "ClusterRole" {
						roleUID = "builtin-view"
					}
					status["resolved"] = map[string]any{"roleRef": map[string]any{
						"kind": ref["kind"], "name": ref["name"], "uid": roleUID, "observedGeneration": 1,
					}}
				}
				source["status"] = status
			}
			metadata["uid"], metadata["generation"] = uids[name], generation
			encoded, err := json.Marshal(source)
			require.NoError(t, err)
			resources = append(resources, encoded)
		}
		origin := map[string]any{"namespace": namespace.Name, "namespaceUID": "namespace-uid", "gitRevision": admitted.GitRevision}
		state := map[string]any{"tierID": "namespace-uid", "roleCount": 10, "bindingCount": 50,
			"projection": map[string]any{"gitRevision": admitted.GitRevision}}
		revision := writeSource(origin, state, resources)
		if step == 1 {
			pending := append([]json.RawMessage(nil), resources...)
			var unresolved map[string]any
			require.NoError(t, json.Unmarshal(pending[10], &unresolved))
			unresolved["status"].(map[string]any)["observedGeneration"] = 1
			pending[10], err = json.Marshal(unresolved)
			require.NoError(t, err)
			pendingRevision = writeSource(origin, state, pending)
			resolvedRevocation = revision
			metadata := unresolved["metadata"].(map[string]any)
			metadata["generation"] = 1
			unresolved["status"].(map[string]any)["resolved"].(map[string]any)["roleRef"].(map[string]any)["observedGeneration"] = 1
			pending[10], err = json.Marshal(unresolved)
			require.NoError(t, err)
			staleGeneration = writeSource(origin, state, pending)
		}
		id := fmt.Sprintf("request-%d", step)
		expected[id] = rbacCapacityProbeExpectation{
			Subject: "reader-b", Namespace: namespace.Name, Action: "product.list", Allowed: allowed, ObservedAt: at.Add(time.Millisecond),
		}
		outcome := "deny"
		if allowed {
			outcome = "allow"
		}
		require.NoError(t, json.NewEncoder(&decisions).Encode(map[string]any{
			"request_id": id, "action": "product.list", "subject": "reader-b", "resource_namespace": namespace.Name,
			"resource_kind": "product", "provider": "opa", "outcome": outcome, "authorization_tiers": []map[string]string{
				{"tierID": cluster.TierUID, "gitRevision": cluster.GitRevision, "contentDigest": cluster.Digest},
				{"tierID": revision.TierUID, "gitRevision": revision.GitRevision, "contentDigest": revision.Digest},
			},
		}))
	}
	directory := t.TempDir()
	sourcePath, decisionPath := filepath.Join(directory, "sources.jsonl"), filepath.Join(directory, "decisions.jsonl")
	require.NoError(t, os.WriteFile(sourcePath, sources.Bytes(), 0600))
	require.NoError(t, os.WriteFile(decisionPath, decisions.Bytes(), 0600))
	require.NoError(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected))
	changed := strings.Replace(decisions.String(), resolvedRevocation.Digest, pendingRevision.Digest, 1)
	require.NotEqual(t, decisions.String(), changed)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.Error(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected),
		"a matching Git commit and HTTP denial cannot promote a pending source candidate into a verified policy")
	changed = strings.Replace(decisions.String(), resolvedRevocation.Digest, staleGeneration.Digest, 1)
	require.NotEqual(t, decisions.String(), changed)
	require.NoError(t, os.WriteFile(decisionPath, []byte(changed), 0600))
	require.Error(t, verifyRBACCapacityDynamicTraces(t, sourcePath, decisionPath, history, root, expected),
		"self-consistent binding status cannot certify a generation older than the independently authored admission")
}

func TestRBACCapacityRootSourceRequiresAuthoredGrantAndPinnedIncarnations(t *testing.T) {
	expected := rbacCapacityRootPolicy{
		NamespaceUID: "root-incarnation", BindingUID: "binding-incarnation", AdminSubject: "bootstrap-admin",
	}
	var err error
	expected.TreeDigest, err = rbacCapacityRootTreeDigest(expected.AdminSubject)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, subject, rootUID, bindingUID string
		extra, pending                     bool
	}{
		{name: "authored", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID},
		{name: "different-administrator", subject: "ordinary-reader", rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID},
		{name: "recreated-root", subject: expected.AdminSubject, rootUID: "replacement-root", bindingUID: expected.BindingUID},
		{name: "recreated-binding", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: "replacement-binding"},
		{name: "additional-root-policy", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID, extra: true},
		{name: "unresolved", subject: expected.AdminSubject, rootUID: expected.NamespaceUID, bindingUID: expected.BindingUID, pending: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := rbacCapacityInitialRootManifest(tc.subject)
			manifest["metadata"].(map[string]any)["uid"] = tc.bindingUID
			manifest["metadata"].(map[string]any)["generation"] = 1
			if !tc.pending {
				manifest["status"] = map[string]any{
					"observedGeneration": 1,
					"resolved": map[string]any{"roleRef": map[string]any{
						"kind": "ClusterRole", "name": "cluster-admin", "uid": "builtin-role", "observedGeneration": 1,
					}},
				}
			}
			encoded, err := json.Marshal(manifest)
			require.NoError(t, err)
			resources := []json.RawMessage{encoded}
			if tc.extra {
				resources = append(resources, encoded)
			}
			tree, err := rbacCapacityRootTreeDigest(tc.subject)
			require.NoError(t, err)
			origin, err := json.Marshal(map[string]any{"namespace": "gitstore-system", "namespaceUID": tc.rootUID,
				"root": true, "active": true, "treeDigest": tree})
			require.NoError(t, err)
			state, err := json.Marshal(map[string]any{"tierID": "cluster", "roleCount": 0, "bindingCount": len(resources)})
			require.NoError(t, err)
			complete, err := checkRBACCapacityRootSource(rbacCapacitySourceHeader{Origin: origin, State: state}, resources, expected)
			if tc.name == "authored" || tc.pending {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Equal(t, tc.name == "authored", complete)
		})
	}
}

func TestRBACCapacitySeedPlanKeepsManagedOwnerSlotAndIsolatesSubjects(t *testing.T) {
	content, err := os.ReadFile("../../shared/schemas/authorization-actions.json")
	require.NoError(t, err)
	var catalog struct{ Actions []struct{ Action string } }
	require.NoError(t, json.Unmarshal(content, &catalog))
	actions := make(map[string]bool)
	for _, action := range catalog.Actions {
		actions[action.Action] = true
	}
	require.NotEmpty(t, actions)
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	require.NoError(t, plan.validate())
	personal := plan.namespace(0)
	require.Equal(t, "personal-owner", personal.Name)
	require.Equal(t, "USER", personal.Tier)
	require.Len(t, personal.Resources, 59, "the controller, not the seeder, owns the fiftieth binding")
	next := plan.namespace(1)
	require.Equal(t, "ORGANIZATION", next.Tier)
	require.NotEqual(t, personal.Name, next.Name)
	require.Len(t, next.Resources, 60)
	for _, namespace := range []rbacCapacitySeedNamespace{personal, next} {
		seen := make(map[string]bool)
		counts := make(map[string]int)
		for _, resource := range namespace.Resources {
			kind := resource["kind"].(string)
			metadata := resource["metadata"].(map[string]any)
			key := kind + "/" + metadata["name"].(string)
			require.False(t, seen[key])
			seen[key] = true
			counts[kind]++
			require.Equal(t, namespace.Name, metadata["namespace"])
			require.NotEqual(t, "namespace-owner-access", metadata["name"])
			require.Equal(t, plan.Owner, metadata["labels"].(map[string]any)[rbacCapacityOwnerLabel])
			content, err := json.Marshal(resource)
			require.NoError(t, err)
			require.Less(t, len(content), 64<<10)
			if kind == "Role" {
				rules := resource["spec"].(map[string]any)["rules"].([]any)
				for _, rule := range rules {
					value := rule.(map[string]any)
					for _, name := range value["resources"].([]string) {
						for _, verb := range value["verbs"].([]string) {
							require.True(t, actions[name+"."+verb], "seeded rules must use the current action catalog")
						}
					}
				}
			}
		}
		require.Equal(t, 10, counts["Role"])
		require.Equal(t, len(namespace.Resources)-10, counts["RoleBinding"])
	}
	firstBinding := func(namespace rbacCapacitySeedNamespace) map[string]any {
		return namespace.Resources[10]["spec"].(map[string]any)
	}
	require.NotEqual(t, firstBinding(personal)["subjects"], firstBinding(next)["subjects"],
		"adjacent namespaces must not grant the same rotating user")
	conditional := next.Resources[1]["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)
	require.Equal(t, []any{map[string]any{"attr": "namespace", "op": "eq", "value": next.Name}}, conditional["when"])
	require.Equal(t, "Group", next.Resources[11]["spec"].(map[string]any)["subjects"].([]any)[0].(map[string]any)["kind"])
	require.Equal(t, "ClusterRole", next.Resources[12]["spec"].(map[string]any)["roleRef"].(map[string]any)["kind"])
	require.Equal(t, "capacity-unresolved", next.Resources[13]["spec"].(map[string]any)["roleRef"].(map[string]any)["name"])
}

func TestRBACCapacitySeedPlanRejectsUnsafeIdentityAndScope(t *testing.T) {
	valid := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d"}, Group: "capacity-readers",
	}
	for _, change := range []func(*rbacCapacitySeedPlan){
		func(p *rbacCapacitySeedPlan) { p.Owner = "" },
		func(p *rbacCapacitySeedPlan) { p.Prefix = "../operator" },
		func(p *rbacCapacitySeedPlan) { p.PersonalSubject = "gitstore-system" },
		func(p *rbacCapacitySeedPlan) { p.PersonalSubject = p.namespace(1).Name },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.Subjects[0] },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.PersonalSubject },
		func(p *rbacCapacitySeedPlan) { p.Subjects[1] = p.Prefix + "-filler-0001-04" },
		func(p *rbacCapacitySeedPlan) { p.Group = "" },
	} {
		broken := valid
		broken.Subjects = append([]string(nil), valid.Subjects...)
		change(&broken)
		require.Error(t, broken.validate())
	}
	seen := make(map[string]bool)
	for index := range 1000 {
		name := valid.namespace(index).Name
		require.False(t, seen[name], fmt.Sprintf("namespace %d must be unique", index))
		seen[name] = true
	}
}

func TestRBACCapacitySeedReadbackKeepsIndependentPolicySemantics(t *testing.T) {
	authored := map[string]any{"rules": []any{map[string]any{
		"resources": []string{"role", "product"}, "verbs": []string{"read", "list"},
		"when": []any{map[string]any{"attr": "namespace", "op": "eq", "value": "owned-namespace"}},
	}}}
	actual := json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"],"resourceNames":null,
		"when":[{"token":null,"attribute":{"attr":"namespace","op":"eq","value":"owned-namespace","values":null}}]}]}`)
	before, err := json.Marshal(authored)
	require.NoError(t, err)
	expected := rbacCapacitySeedSpec(t, authored, true)
	require.Equal(t, expected, rbacCapacitySeedSpec(t, actual, false))
	after, err := json.Marshal(authored)
	require.NoError(t, err)
	require.JSONEq(t, string(before), string(after), "readback comparison must not rewrite the authored oracle")
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"]}]}`),
		json.RawMessage(`{"rules":[{"resources":["product","role"],"verbs":["list","read"],"when":[{"attribute":{"attr":"namespace","op":"eq","value":"other"}}]}]}`),
	} {
		require.NotEqual(t, expected, rbacCapacitySeedSpec(t, invalid, false), "missing or different conditions are not equivalent grants")
	}
	binding := map[string]any{
		"roleRef":  map[string]any{"kind": "Role", "name": "reader"},
		"subjects": []any{map[string]any{"kind": "User", "name": "expected-subject"}},
	}
	require.NotEqual(t, rbacCapacitySeedSpec(t, binding, true), rbacCapacitySeedSpec(t,
		json.RawMessage(`{"roleRef":{"kind":"ROLE","name":"reader"},"subjects":[{"kind":"USER","name":"different-subject"}]}`), false))
}

func TestRBACCapacityRequestPlanPreservesReplicaMixAndExcludesOtherGrants(t *testing.T) {
	plan := rbacCapacitySeedPlan{
		Owner: "owned-fixture", Prefix: "rbac-seed-fixture", PersonalSubject: "personal-owner",
		Subjects: []string{"reader-a", "reader-b", "reader-c", "reader-d", "reader-e", "reader-f", "reader-g", "reader-h"},
		Group:    "capacity-readers",
	}
	groups := []string{"reader-a", "reader-e"}
	counts := [2]map[string]int{{}, {}}
	namespaces, subjects, actions, requests := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for offer := int64(0); offer < 1000*20; offer++ {
		request, err := plan.request(offer, 1000, groups)
		require.NoError(t, err)
		require.False(t, requests[request.ID])
		requests[request.ID] = true
		counts[request.Replica][request.Expected]++
		namespaces[request.Namespace], subjects[request.Subject], actions[request.Action] = true, true, true
		if request.Expected == "deny" {
			index, _ := rbacCapacityCase(offer/2, 1000)
			require.NotEqual(t, plan.Subjects[index%len(plan.Subjects)], request.Subject)
			require.NotEqual(t, plan.Subjects[(index+1)%len(plan.Subjects)], request.Subject)
			require.NotContains(t, groups, request.Subject, "an unresolved or unbound User can still have a Group grant")
		}
		if request.Expected == "isolation" {
			require.Equal(t, plan.PersonalSubject, request.Subject)
			require.NotEqual(t, plan.PersonalSubject, request.Namespace)
		}
	}
	for _, actual := range counts {
		require.Equal(t, map[string]int{"allow": 6000, "deny": 3000, "isolation": 1000}, actual)
	}
	require.Len(t, namespaces, 1000)
	require.Len(t, subjects, 9)
	require.Equal(t, map[string]bool{"product.list": true, "role.list": true}, actions)
	_, err := plan.request(-1, 1000, groups)
	require.Error(t, err)
	_, err = plan.request(0, 1, groups)
	require.Error(t, err, "isolation probes require another owned Namespace")
	_, err = plan.request(0, 1000, []string{"not-an-authored-principal"})
	require.Error(t, err)
	_, err = plan.request(12, 1000, plan.Subjects)
	require.Error(t, err, "a fixture without an independently ungranted principal cannot produce expected denials")
}

func TestRBACCapacityOrdinaryRequestsNeverCountInfrastructureOrPartialDataAsDenial(t *testing.T) {
	for _, tc := range []struct {
		name, expected, body, outcome string
		status                        int
	}{
		{"allow", "allow", `{"data":{"products":{"edges":[]}}}`, "allow", 200},
		{"deny", "deny", `{"data":{"products":null},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "deny", 200},
		{"isolation", "isolation", `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "isolation", 200},
		{"wrong-allow", "deny", `{"data":{"products":{"edges":[]}}}`, "wrong", 200},
		{"wrong-denial", "allow", `{"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "wrong", 200},
		{"unavailable", "deny", `{"errors":[{"extensions":{"code":"AUTHORIZATION_UNAVAILABLE"}}]}`, "unavailable", 200},
		{"partial-data", "deny", `{"data":{"products":{"edges":[]}},"errors":[{"extensions":{"code":"FORBIDDEN"}}]}`, "wrong", 200},
		{"mixed-errors", "deny", `{"errors":[{"extensions":{"code":"FORBIDDEN"}},{"extensions":{"code":"INTERNAL"}}]}`, "wrong", 200},
		{"transport-forbidden", "deny", "", "unavailable", 403},
		{"transport-unavailable", "deny", "", "unavailable", 503},
		{"missing-envelope", "allow", `{"data":{}}`, "wrong", 200},
		{"missing-node", "allow", `{"data":{"products":{"edges":[{}]}}}`, "wrong", 200},
		{"malformed", "allow", `not-json`, "wrong", 200},
		{"bounded-body", "allow", strings.Repeat("x", 65537), "wrong", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, "/admin/graphql", r.URL.Path)
				require.Equal(t, "Bearer private-test-token", r.Header.Get("Authorization"))
				require.Equal(t, "owned-request", r.Header.Get("X-Request-ID"))
				var document struct {
					Query     string
					Variables map[string]string
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&document))
				require.Contains(t, document.Query, "products(namespace:$namespace,first:1)")
				require.Equal(t, "owned-namespace", document.Variables["namespace"])
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			request := rbacCapacityRequest{
				ID: "owned-request", Namespace: "owned-namespace", Action: "product.list", Expected: tc.expected,
			}
			require.Equal(t, tc.outcome, executeRBACCapacityRequest(t.Context(), server.Client(), server.URL, "private-test-token", request))
		})
	}
	t.Run("ordinary-role-list", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			content, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(content), "roles(namespace:$namespace,first:1)")
			_, _ = io.WriteString(w, `{"data":{"roles":{"edges":[]}}}`)
		}))
		defer server.Close()
		require.Equal(t, "allow", executeRBACCapacityRequest(t.Context(), server.Client(), server.URL, "private-test-token",
			rbacCapacityRequest{ID: "owned-request", Namespace: "owned-namespace", Action: "role.list", Expected: "allow"}))
	})
}

func TestRBACCapacityScheduledOffersAndRotation(t *testing.T) {
	require.EqualValues(t, 900000, rbacCapacityScheduledOffers(30*time.Minute, 500))
	require.EqualValues(t, 1, rbacCapacityScheduledOffers(3*time.Millisecond, 500))
	require.Zero(t, rbacCapacityScheduledOffers(0, 500))
	counts := make(map[string]int)
	namespaces := make(map[int]bool)
	for offer := int64(0); offer < 10000; offer++ {
		namespace, probe := rbacCapacityCase(offer, 1000)
		counts[probe]++
		namespaces[namespace] = true
	}
	require.Equal(t, map[string]int{"allow": 6000, "deny": 3000, "isolation": 1000}, counts)
	require.Len(t, namespaces, 1000, "the working set must rotate, not repeat one namespace")
}

func TestRBACCapacityBoundedScheduler(t *testing.T) {
	cfg := rbacCapacityScheduleConfig{Duration: 100 * time.Millisecond, Rate: 1000, Workers: 2, Pending: 16, Drain: time.Second}
	t.Run("accounts for every scheduled completion", func(t *testing.T) {
		healthy := cfg
		healthy.Duration, healthy.Rate, healthy.Pending = time.Second, 10, 10
		accounting, err := runRBACCapacitySchedule(t.Context(), healthy, func(_ context.Context, offer int64) string {
			_, probe := rbacCapacityCase(offer, 1000)
			return probe
		})
		require.NoError(t, err)
		require.NoError(t, accounting.validate(10, false))
		require.EqualValues(t, 6, accounting.Allowed)
		require.EqualValues(t, 3, accounting.Denied)
		require.EqualValues(t, 1, accounting.Invalid)
	})
	t.Run("queue saturation reports all offers without stretching scheduling", func(t *testing.T) {
		started := time.Now()
		accounting, err := runRBACCapacitySchedule(t.Context(), cfg, func(ctx context.Context, _ int64) string {
			<-ctx.Done()
			return "unavailable"
		})
		require.NoError(t, err)
		require.Less(t, time.Since(started), 2*time.Second)
		require.EqualValues(t, 100, accounting.Offered)
		require.Positive(t, accounting.Dropped)
		require.Positive(t, accounting.Unavailable)
		require.Equal(t, accounting.Offered, accounting.Completed+accounting.Dropped)
		require.Error(t, accounting.validate(100, false))
	})
	t.Run("cancellation cannot hide unscheduled offers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		accounting, err := runRBACCapacitySchedule(ctx, cfg, func(context.Context, int64) string {
			t.Error("canceled workload must not execute")
			return "allow"
		})
		require.ErrorIs(t, err, context.Canceled)
		require.EqualValues(t, 100, accounting.Offered)
		require.EqualValues(t, 100, accounting.Dropped)
		require.Zero(t, accounting.Completed)
	})
	t.Run("invalid limits reject before dispatch", func(t *testing.T) {
		invalid := cfg
		invalid.Pending = 0
		_, err := runRBACCapacitySchedule(t.Context(), invalid, func(context.Context, int64) string {
			t.Error("invalid workload must not execute")
			return "allow"
		})
		require.Error(t, err)
	})
	t.Run("full load window remains active after the last offer", func(t *testing.T) {
		short := cfg
		short.Rate = 10
		started := time.Now()
		accounting, err := runRBACCapacitySchedule(t.Context(), short, func(context.Context, int64) string { return "allow" })
		require.NoError(t, err)
		require.EqualValues(t, 1, accounting.Completed)
		require.GreaterOrEqual(t, time.Since(started), short.Duration)
	})
}

func TestRBACCapacityAccountingRejectsLostOrMisclassifiedOffers(t *testing.T) {
	valid := rbacCapacityAccounting{Offered: 900000, Completed: 900000, Allowed: 540000, Denied: 270000, Invalid: 90000}
	require.NoError(t, valid.validate(900000, false))
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityAccounting)
	}{
		{"timer coalescing lost offers", func(a *rbacCapacityAccounting) { a.Offered-- }},
		{"unreported queued work", func(a *rbacCapacityAccounting) { a.Completed--; a.Allowed-- }},
		{"duplicate completions", func(a *rbacCapacityAccounting) { a.Completed++ }},
		{"dropped healthy work", func(a *rbacCapacityAccounting) { a.Dropped++; a.Completed--; a.Allowed-- }},
		{"infrastructure treated as denial", func(a *rbacCapacityAccounting) { a.Unavailable++; a.Denied-- }},
		{"wrong policy decision", func(a *rbacCapacityAccounting) { a.Wrong++; a.Allowed-- }},
		{"negative counter", func(a *rbacCapacityAccounting) { a.Dropped = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			tc.change(&broken)
			require.Error(t, broken.validate(900000, false))
		})
	}
	fault := valid
	fault.Unavailable, fault.Allowed = 10, fault.Allowed-10
	require.NoError(t, fault.validate(900000, true))
	fault.Dropped, fault.Completed = 1, fault.Completed-1
	fault.Allowed--
	require.Error(t, fault.validate(900000, true), "fault windows do not excuse dropped scheduled work")
}

func TestRBACCapacityOracleUsesIndependentCommittedRevisionVectors(t *testing.T) {
	now := time.Unix(1000, 0)
	cluster := rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "cluster-commit", Digest: "cluster-policy"}
	namespace := rbacCapacityPolicyRevision{TierUID: "namespace-uid", GitRevision: "namespace-commit", Digest: "namespace-policy"}
	vector := rbacCapacityPolicyVector{Cluster: cluster, Namespace: namespace}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"owner": true, "outsider": false}))
	require.NoError(t, oracle.check(vector, "owner", true, now))
	require.NoError(t, oracle.check(vector, "outsider", false, now))
	require.Error(t, oracle.check(vector, "owner", false, now), "observations must not define their own expected policy")
	require.Error(t, oracle.check(vector, "unseeded", false, now), "unknown probes cannot silently become denials")
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityPolicyVector)
	}{
		{"unknown cluster commit", func(v *rbacCapacityPolicyVector) { v.Cluster.GitRevision = "unknown" }},
		{"unknown namespace commit", func(v *rbacCapacityPolicyVector) { v.Namespace.GitRevision = "unknown" }},
		{"wrong namespace UID", func(v *rbacCapacityPolicyVector) { v.Namespace.TierUID = "replacement-uid" }},
		{"different translated content", func(v *rbacCapacityPolicyVector) { v.Namespace.Digest = "wrong" }},
		{"cross-tier equal revision", func(v *rbacCapacityPolicyVector) { v.Namespace = cluster }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unknown := vector
			tc.change(&unknown)
			require.Error(t, oracle.check(unknown, "owner", true, now))
		})
	}
	require.Error(t, oracle.commit(vector, now, now, map[string]bool{"owner": false}), "a committed vector cannot be reinterpreted")
	newVector := vector
	newVector.Namespace.GitRevision, newVector.Namespace.Digest = "revocation", "revoked-policy"
	require.NoError(t, oracle.commit(newVector, now, now.Add(time.Second), map[string]bool{"owner": false, "outsider": false}))
	require.NoError(t, oracle.check(vector, "owner", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "owner", true, now.Add(6*time.Second+time.Nanosecond)),
		"an old, correctly evaluated allowance still violates the revocation bound")
	require.NoError(t, oracle.check(newVector, "owner", false, now.Add(7*time.Second)))
	require.Error(t, oracle.check(newVector, "owner", false, now), "a future commit is not evaluated evidence")
}

func TestRBACCapacityOraclePreservesIntermediateRevocations(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "c", Digest: "c"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "ns", GitRevision: "grant", Digest: "grant"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	expected := map[string]bool{"owner": true}
	require.NoError(t, oracle.commit(vector, now, now, expected))
	expected["owner"] = false
	require.NoError(t, oracle.check(vector, "owner", true, now), "the oracle must copy caller-owned expectations")
	revoked := vector
	revoked.Namespace.GitRevision, revoked.Namespace.Digest = "revoke", "revoke"
	require.NoError(t, oracle.commit(revoked, now, now.Add(time.Second), map[string]bool{"owner": false}))
	regranted := vector
	regranted.Namespace.GitRevision, regranted.Namespace.Digest = "regrant", "regrant"
	require.NoError(t, oracle.commit(regranted, now, now.Add(2*time.Second), map[string]bool{"owner": true}))
	require.Error(t, oracle.check(vector, "owner", true, now.Add(7*time.Second)),
		"latest allow must not hide a missed intermediate revocation")
	require.NoError(t, oracle.check(regranted, "owner", true, now.Add(7*time.Second)))
	alias := vector
	alias.Cluster.Digest, alias.Namespace.Digest = "verified-cluster-status", "verified-namespace-status"
	require.Error(t, oracle.check(alias, "owner", true, now.Add(7*time.Second)),
		"matching authored Git commits alone cannot register an unverified source digest")
	require.Error(t, oracle.commit(alias, now, now.Add(3*time.Second), map[string]bool{"owner": true}),
		"a source alias cannot restart the original admission clock")
	require.Error(t, oracle.commit(alias, now, now, map[string]bool{"owner": false}),
		"a source alias cannot reinterpret independently authored expectations")
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"owner": true}))
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"owner": true}))
	require.NoError(t, oracle.check(alias, "owner", true, now.Add(6*time.Second)))
	require.Error(t, oracle.check(alias, "owner", true, now.Add(7*time.Second)),
		"a late source alias must not hide a previously admitted intermediate revocation")
	require.Error(t, oracle.check(alias, "owner", true, now.Add(-time.Nanosecond)))
	uncommitted := alias
	uncommitted.Namespace.GitRevision = "unadmitted"
	require.Error(t, oracle.commit(uncommitted, now, now, map[string]bool{"owner": true}),
		"only source variants of the same admitted Git vector can arrive out of order")
}

func TestRBACCapacityOracleEnforcesGrantPropagation(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "c", Digest: "c"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "ns", GitRevision: "deny", Digest: "deny"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"owner": false}))
	granted := vector
	granted.Namespace.GitRevision, granted.Namespace.Digest = "grant", "grant"
	require.NoError(t, oracle.commit(granted, now, now.Add(time.Second), map[string]bool{"owner": true}))
	require.NoError(t, oracle.check(vector, "owner", false, now.Add(6*time.Second)))
	require.Error(t, oracle.check(vector, "owner", false, now.Add(6*time.Second+time.Nanosecond)),
		"an old denial must not conceal a missed grant propagation deadline")
	require.NoError(t, oracle.check(granted, "owner", true, now.Add(7*time.Second)))
}

func TestRBACCapacityOracleDoesNotRefreshOneTierWithAnother(t *testing.T) {
	for _, changedTier := range []string{"cluster", "namespace"} {
		for _, sameOutcome := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same-outcome=%t", changedTier, sameOutcome), func(t *testing.T) {
				now := time.Unix(1000, 0)
				initial := rbacCapacityPolicyVector{
					Cluster:   rbacCapacityPolicyRevision{TierUID: "cluster", GitRevision: "root-original", Digest: "root-original"},
					Namespace: rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "ns-original", Digest: "ns-original"},
				}
				superseding, mixed := initial, initial
				if changedTier == "cluster" {
					superseding.Cluster.GitRevision, superseding.Cluster.Digest = "root-new", "root-new"
					mixed.Namespace.GitRevision, mixed.Namespace.Digest = "ns-new", "ns-new"
				} else {
					superseding.Namespace.GitRevision, superseding.Namespace.Digest = "ns-new", "ns-new"
					mixed.Cluster.GitRevision, mixed.Cluster.Digest = "root-new", "root-new"
				}
				oracle := newRBACCapacityOracle(5 * time.Second)
				clusterAt, namespaceAt := now, now
				mixedClusterAt, mixedNamespaceAt := now, now
				if changedTier == "cluster" {
					clusterAt, mixedNamespaceAt = now.Add(time.Second), now.Add(3*time.Second)
				} else {
					namespaceAt, mixedClusterAt = now.Add(time.Second), now.Add(3*time.Second)
				}
				require.NoError(t, oracle.commit(initial, now, now, map[string]bool{"reader": true}))
				require.NoError(t, oracle.commit(superseding, clusterAt, namespaceAt, map[string]bool{"reader": sameOutcome}))
				require.NoError(t, oracle.commit(mixed, mixedClusterAt, mixedNamespaceAt, map[string]bool{"reader": true}))
				require.NoError(t, oracle.check(mixed, "reader", true, now.Add(6*time.Second)))
				require.Error(t, oracle.check(mixed, "reader", true, now.Add(6*time.Second+time.Nanosecond)),
					"a newer companion tier must not restart the superseded tier's clock, even when the outcome is unchanged")

				alias := mixed
				alias.Cluster.Digest, alias.Namespace.Digest = "verified-root-alias", "verified-ns-alias"
				require.NoError(t, oracle.commit(alias, mixedClusterAt, mixedNamespaceAt, map[string]bool{"reader": true}))
				require.Error(t, oracle.check(alias, "reader", true, now.Add(7*time.Second)),
					"late verified source aliases must inherit both independent supersession clocks")
			})
		}
	}
}

func TestRBACCapacityOraclePreservesCanonicalTierAdmissions(t *testing.T) {
	now := time.Unix(1000, 0)
	vector := rbacCapacityPolicyVector{
		Cluster:   rbacCapacityPolicyRevision{TierUID: "root", GitRevision: "root-original", Digest: "root-original"},
		Namespace: rbacCapacityPolicyRevision{TierUID: "namespace", GitRevision: "ns-original", Digest: "ns-original"},
	}
	oracle := newRBACCapacityOracle(5 * time.Second)
	require.NoError(t, oracle.commit(vector, now, now, map[string]bool{"reader": true}))
	alias := vector
	alias.Cluster.Digest, alias.Namespace.Digest = "root-alias", "ns-alias"
	require.Error(t, oracle.commit(alias, now.Add(time.Second), now, map[string]bool{"reader": true}),
		"a verified root alias cannot move its canonical admission time")
	require.Error(t, oracle.commit(alias, now, now.Add(time.Second), map[string]bool{"reader": true}),
		"a verified Namespace alias cannot move its canonical admission time")
	require.NoError(t, oracle.commit(alias, now, now, map[string]bool{"reader": true}))

	next := vector
	next.Cluster.GitRevision, next.Cluster.Digest = "root-next", "root-next"
	require.Error(t, oracle.commit(next, now.Add(3*time.Second), now.Add(time.Second), map[string]bool{"reader": true}))
	require.NoError(t, oracle.check(vector, "reader", true, now.Add(20*time.Second)),
		"a rejected mixed admission must not supersede either installed tier")
	require.Error(t, oracle.commit(next, now, now, map[string]bool{"reader": true}),
		"distinct canonical admissions require a strictly increasing per-tier clock")
	require.NoError(t, oracle.commit(next, now.Add(time.Second), now, map[string]bool{"reader": true}),
		"a rejected mixed admission must not leave a partial future root admission")
	namespace := next
	namespace.Namespace.GitRevision, namespace.Namespace.Digest = "ns-next", "ns-next"
	require.NoError(t, oracle.commit(namespace, now.Add(time.Second), now.Add(3*time.Second), map[string]bool{"reader": true}))
	require.Error(t, oracle.check(namespace, "reader", true, now.Add(2*time.Second)))
	require.NoError(t, oracle.check(namespace, "reader", true, now.Add(4*time.Second)))
	root := namespace
	root.Cluster.GitRevision, root.Cluster.Digest = "root-latest", "root-latest"
	require.NoError(t, oracle.commit(root, now.Add(5*time.Second), now.Add(3*time.Second), map[string]bool{"reader": true}))
	require.Error(t, oracle.check(root, "reader", true, now.Add(4*time.Second)))
	require.NoError(t, oracle.check(root, "reader", true, now.Add(5*time.Second)))

	foreign := root
	foreign.Cluster.TierUID = vector.Namespace.TierUID
	foreign.Namespace.TierUID = "other-namespace"
	require.Error(t, oracle.commit(foreign, now.Add(6*time.Second), now.Add(6*time.Second), map[string]bool{"reader": true}),
		"one tier UID cannot change between root and Namespace scope")
}

func TestRBACCapacityPrivateFixture(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fixture.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"cases":[]}`), 0600))
	_, err := readRBACCapacityPrivateFixture(path)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0644))
	_, err = readRBACCapacityPrivateFixture(path)
	require.Error(t, err)
	require.NoError(t, os.Chmod(path, 0600))
	alias := filepath.Join(root, "alias.json")
	require.NoError(t, os.Symlink(path, alias))
	_, err = readRBACCapacityPrivateFixture(alias)
	require.Error(t, err, "do not load credentials through an unverified symlink")
}

func TestRBACCapacityOwnedTopologyAndNativeCounts(t *testing.T) {
	valid := rbacCapacityTopology{Owner: "owned-fixture", NativeNamespaces: 1000, NativeRoles: 10000, NativeBindings: 50000}
	for index, role := range []string{"api", "api", "controller", "controller", "git", "scylla"} {
		process := rbacCapacityProcess{ID: string(rune('a' + index)), Role: role, Owner: valid.Owner, Epoch: "epoch", Running: true}
		if role == "controller" {
			process.Checkpoint = filepath.Join("/private/owned-fixture", process.ID)
		}
		valid.Processes = append(valid.Processes, process)
	}
	require.NoError(t, valid.validate())
	clustered := valid
	clustered.Processes = append(append([]rbacCapacityProcess(nil), valid.Processes...),
		rbacCapacityProcess{ID: "scylla-b", Role: "scylla", Owner: valid.Owner, Epoch: "epoch", Running: true},
		rbacCapacityProcess{ID: "scylla-c", Role: "scylla", Owner: valid.Owner, Epoch: "epoch", Running: true})
	require.NoError(t, clustered.validate(), "shared native Scylla may be a multi-peer cluster")
	for _, tc := range []struct {
		name   string
		change func(*rbacCapacityTopology)
	}{
		{"wrong native count", func(v *rbacCapacityTopology) { v.NativeBindings-- }},
		{"foreign process", func(v *rbacCapacityTopology) { v.Processes[0].Owner = "operator" }},
		{"duplicate process", func(v *rbacCapacityTopology) { v.Processes[1].ID = v.Processes[0].ID }},
		{"missing epoch", func(v *rbacCapacityTopology) { v.Processes[0].Epoch = "" }},
		{"stopped dependency", func(v *rbacCapacityTopology) { v.Processes[5].Running = false }},
		{"shared checkpoints", func(v *rbacCapacityTopology) { v.Processes[3].Checkpoint = v.Processes[2].Checkpoint }},
		{"nested checkpoints", func(v *rbacCapacityTopology) { v.Processes[3].Checkpoint = v.Processes[2].Checkpoint + "/child" }},
		{"second Git writer", func(v *rbacCapacityTopology) {
			v.Processes = append(v.Processes, rbacCapacityProcess{ID: "other-git", Role: "git", Owner: v.Owner, Epoch: "epoch", Running: true})
		}},
		{"missing Scylla", func(v *rbacCapacityTopology) { v.Processes = v.Processes[:5] }},
		{"unknown dependency", func(v *rbacCapacityTopology) { v.Processes[5].Role = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			broken.Processes = append([]rbacCapacityProcess(nil), valid.Processes...)
			tc.change(&broken)
			require.Error(t, broken.validate())
		})
	}
}

func TestRBACCapacityEpochCountersAndFaultAttribution(t *testing.T) {
	segments := []rbacCapacityCounterSegment{
		{Epoch: "old-process", Before: 100, After: 120},
		{Epoch: "replacement", Before: 0, After: 30},
	}
	total, err := rbacCapacityCounterDelta(segments)
	require.NoError(t, err)
	require.EqualValues(t, 50, total)
	for _, invalid := range [][]rbacCapacityCounterSegment{
		{{Epoch: "same-process", Before: 100, After: 0}},
		{{Epoch: "", After: 30}},
		{{Epoch: "same"}, {Epoch: "same", After: 30}},
	} {
		_, err := rbacCapacityCounterDelta(invalid)
		require.Error(t, err)
	}
	window := rbacCapacityFaultWindow{Replica: 0, Start: time.Unix(100, 0), End: time.Unix(130, 0),
		Injected: true, Restored: true, OldEpoch: "old", NewEpoch: "new"}
	require.NoError(t, window.unavailable(0, time.Unix(110, 0), "old"))
	require.Error(t, window.unavailable(1, time.Unix(110, 0), "old"))
	require.Error(t, window.unavailable(0, time.Unix(131, 0), "new"))
	require.Error(t, window.unavailable(0, time.Unix(110, 0), "unknown"))
	window.Restored = false
	require.Error(t, window.unavailable(0, time.Unix(110, 0), "old"), "a planned allowance alone is not fault evidence")
}
