// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"context"
	"errors"
	"fmt"
)

var ErrRepairRequired = errors.New("datastore: repair required")

type MutationStep struct {
	Operation    string
	ResourceKind string
	ResourceUID  string
	Projection   string
	LookupKey    string
	Action       string
}

type RepairRequiredError struct {
	Step         MutationStep
	Primary      error
	Compensation error
}

func NewRepairRequiredError(step MutationStep, primary, compensation error) error {
	return &RepairRequiredError{
		Step:         step,
		Primary:      primary,
		Compensation: compensation,
	}
}

func (e *RepairRequiredError) Error() string {
	return fmt.Sprintf("%s: operation=%s resource_kind=%s projection=%s action=%s: primary=%v: compensation=%v",
		ErrRepairRequired, e.Step.Operation, e.Step.ResourceKind, e.Step.Projection, e.Step.Action,
		e.Primary, e.Compensation)
}

func (e *RepairRequiredError) Unwrap() error {
	return errors.Join(ErrRepairRequired, e.Primary)
}

type FindingType string

const (
	FindingMissing   FindingType = "missing"
	FindingDangling  FindingType = "dangling"
	FindingDuplicate FindingType = "duplicate"
	FindingStale     FindingType = "stale"
)

type ProjectionFinding struct {
	ResourceKind string
	ResourceUID  string
	Projection   string
	LookupKey    string
	Operation    string
	Type         FindingType
}

type ProjectionFindingObserver func(ProjectionFinding)

type projectionFindingObserverKey struct{}

func WithProjectionFindingObserver(ctx context.Context, observer ProjectionFindingObserver) context.Context {
	if observer == nil {
		return ctx
	}
	if existing, ok := ctx.Value(projectionFindingObserverKey{}).(ProjectionFindingObserver); ok {
		next := observer
		observer = func(finding ProjectionFinding) {
			existing(finding)
			next(finding)
		}
	}
	return context.WithValue(ctx, projectionFindingObserverKey{}, observer)
}

func ReportProjectionFinding(ctx context.Context, finding ProjectionFinding) {
	if ctx == nil {
		return
	}
	if observer, ok := ctx.Value(projectionFindingObserverKey{}).(ProjectionFindingObserver); ok {
		observer(finding)
	}
}
