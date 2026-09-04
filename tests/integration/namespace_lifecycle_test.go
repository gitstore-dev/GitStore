// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func namespaceLifecycleFixture(name, title string) string {
	return fmt.Sprintf(`---
apiVersion: gitstore.dev/v1beta1
kind: Namespace
metadata:
  name: %s
spec:
  title: %s
  tier: USER
---
`, name, title)
}

type namespaceLifecycleState struct {
	Metadata struct {
		ResourceVersion string `json:"resourceVersion"`
		Generation      int    `json:"generation"`
	} `json:"metadata"`
	Spec struct {
		Title *string `json:"title"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration  int     `json:"observedGeneration"`
		LastAppliedRevision *string `json:"lastAppliedRevision"`
		Conditions          []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
	} `json:"status"`
}

func queryNamespaceLifecycle(t *testing.T, name string) *namespaceLifecycleState {
	t.Helper()
	resp := gqlQuery(t, `
		query($name: String!) {
			namespace(by: {identifier: $name}) {
				metadata { resourceVersion generation }
				spec { title }
				status {
					observedGeneration
					lastAppliedRevision
					conditions { type status }
				}
			}
		}
	`, map[string]any{"name": name})
	if len(resp.Errors) > 0 {
		return nil
	}
	var data struct {
		Namespace *namespaceLifecycleState `json:"namespace"`
	}
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		t.Fatalf("unmarshal namespace lifecycle response: %v", err)
	}
	return data.Namespace
}

func waitForNamespaceLifecycle(t *testing.T, name string, generation int) *namespaceLifecycleState {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		state := queryNamespaceLifecycle(t, name)
		if state != nil && state.Metadata.Generation == generation {
			return state
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("namespace %q did not reach generation %d", name, generation)
	return nil
}

func requireAdmissionAccepted(t *testing.T, state *namespaceLifecycleState) {
	t.Helper()
	if state.Status.ObservedGeneration != state.Metadata.Generation {
		t.Fatalf("status.observedGeneration = %d, want %d", state.Status.ObservedGeneration, state.Metadata.Generation)
	}
	if state.Status.LastAppliedRevision == nil || *state.Status.LastAppliedRevision == "" {
		t.Fatal("status.lastAppliedRevision is empty")
	}
	for _, condition := range state.Status.Conditions {
		if condition.Type == "AdmissionAccepted" && condition.Status == "TRUE" {
			return
		}
	}
	t.Fatalf("AdmissionAccepted=True not found in %+v", state.Status.Conditions)
}

func TestNamespaceLifecycle_CreateAndUpdateThroughAdmission(t *testing.T) {
	name := uniqueName("namespace-lifecycle")
	h := newPushHelperForRepo(t, "gitstore-system", "gitstore-system")
	h.commitNamespace(name+".md", namespaceLifecycleFixture(name, "Initial title"))
	if out, err := h.push(); err != nil {
		t.Fatalf("create namespace push failed: %v\n%s", err, out)
	}

	created := waitForNamespaceLifecycle(t, name, 1)
	requireAdmissionAccepted(t, created)
	initialVersion := created.Metadata.ResourceVersion

	h.commitNamespace(name+".md", namespaceLifecycleFixture(name, "Updated title"))
	if out, err := h.push(); err != nil {
		t.Fatalf("update namespace push failed: %v\n%s", err, out)
	}

	updated := waitForNamespaceLifecycle(t, name, 2)
	requireAdmissionAccepted(t, updated)
	if updated.Metadata.ResourceVersion == initialVersion {
		t.Fatalf("resourceVersion did not advance from %q", initialVersion)
	}
	if updated.Spec.Title == nil || *updated.Spec.Title != "Updated title" {
		t.Fatalf("spec.title = %v, want Updated title", updated.Spec.Title)
	}
}
