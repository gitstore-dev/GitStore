// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
)

func TestProjectionRepairRequiresExplicitConfirmation(t *testing.T) {
	original := openProjectionRepair
	t.Cleanup(func() { openProjectionRepair = original })
	called := false
	openProjectionRepair = func(config.ScyllaConfig) (projectionRepairClient, error) {
		called = true
		return &fakeProjectionRepairClient{}, nil
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"scylla-projection-repair"}, strings.NewReader(""), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("run() code = %d, want 2", code)
	}
	if called {
		t.Fatal("repair opened Scylla without confirmation")
	}
	if !strings.Contains(stderr.String(), "--dry-run or explicit --confirm") {
		t.Fatalf("stderr = %q, want confirmation guidance", stderr.String())
	}
}

func TestProjectionRepairDryRunPrintsPlanWithoutApply(t *testing.T) {
	client := &fakeProjectionRepairClient{
		plan: scylla.RepairPlan{Findings: []scylla.ProjectionFinding{{
			Type: scylla.FindingMissing, Kind: "Namespace", UID: "uid",
			Table: "namespaces_by_name", Key: "demo", Repairable: true,
		}}},
	}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"scylla-projection-repair", "--dry-run", "--hosts", "scylla-a:9042,scylla-b:9042"},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if client.applyCalls != 0 {
		t.Fatalf("dry run applied %d repairs", client.applyCalls)
	}
	if !strings.Contains(stdout.String(), `"type": "missing"`) {
		t.Fatalf("stdout = %q, want stable JSON finding", stdout.String())
	}
}

func TestProjectionRepairConfirmedAppliesAndVerifies(t *testing.T) {
	client := &fakeProjectionRepairClient{
		plan: scylla.RepairPlan{Actions: []scylla.RepairAction{{
			Type: scylla.RepairInsert, Kind: "Namespace", UID: "uid", ExpectedResourceVersion: "7",
			After: &scylla.ProjectionRecord{Table: "namespaces_by_name", UID: "uid", Name: "demo"},
		}}},
		result: scylla.RepairResult{PlannedActions: 1, AppliedActions: 1},
	}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run(
		[]string{"scylla-projection-repair", "--confirm", "--keyspace", "gitstore"},
		strings.NewReader(""), &stdout, &stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if client.applyCalls != 1 {
		t.Fatalf("apply calls = %d, want 1", client.applyCalls)
	}
	if !strings.Contains(stdout.String(), `"appliedActions": 1`) {
		t.Fatalf("stdout = %q, want applied action summary", stdout.String())
	}
}

func TestProjectionAuditPrintsMachineReadableSummary(t *testing.T) {
	client := &fakeProjectionRepairClient{plan: scylla.RepairPlan{}}
	withFakeProjectionClient(t, client)

	var stdout, stderr bytes.Buffer
	code := run([]string{"scylla-projection-audit"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if strings.TrimSpace(stdout.String()) != `{
  "findings": null,
  "actions": null
}` {
		t.Fatalf("stdout = %q, want stable JSON summary", stdout.String())
	}
}

func withFakeProjectionClient(t *testing.T, client *fakeProjectionRepairClient) {
	t.Helper()
	original := openProjectionRepair
	t.Cleanup(func() { openProjectionRepair = original })
	openProjectionRepair = func(config.ScyllaConfig) (projectionRepairClient, error) {
		return client, nil
	}
}

type fakeProjectionRepairClient struct {
	plan       scylla.RepairPlan
	result     scylla.RepairResult
	auditErr   error
	applyErr   error
	applyCalls int
}

func (f *fakeProjectionRepairClient) Audit(context.Context) (scylla.RepairPlan, error) {
	return f.plan, f.auditErr
}

func (f *fakeProjectionRepairClient) Apply(context.Context, scylla.RepairPlan) (scylla.RepairResult, error) {
	f.applyCalls++
	return f.result, f.applyErr
}

func (f *fakeProjectionRepairClient) Close() {}
