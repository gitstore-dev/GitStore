// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"go.uber.org/zap"
)

type failurePoint string

const (
	failureBefore failurePoint = "before"
	failureAfter  failurePoint = "after"
)

type failureInjector interface {
	Inject(step string, point failurePoint) error
}

type noopFailureInjector struct{}

func (noopFailureInjector) Inject(string, failurePoint) error { return nil }

type mutationAction struct {
	Step       datastore.MutationStep
	Apply      func(context.Context) error
	Compensate func(context.Context) error
}

type mutationExecutor struct {
	injector failureInjector
}

func newMutationExecutor(injector failureInjector) *mutationExecutor {
	if injector == nil {
		injector = noopFailureInjector{}
	}
	return &mutationExecutor{injector: injector}
}

func (e *mutationExecutor) execute(ctx context.Context, actions ...mutationAction) error {
	completed := make([]mutationAction, 0, len(actions))
	for _, action := range actions {
		if err := e.injector.Inject(action.Step.Action, failureBefore); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
		if action.Apply == nil {
			return e.compensate(ctx, completed, action.Step, errors.New("mutation action has no apply function"))
		}
		if err := action.Apply(ctx); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
		completed = append(completed, action)
		if err := e.injector.Inject(action.Step.Action, failureAfter); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
	}
	return nil
}

func (e *mutationExecutor) executeUpdate(
	ctx context.Context,
	committedResourceVersion string,
	authoritative mutationAction,
	projections ...mutationAction,
) error {
	if err := e.injector.Inject(authoritative.Step.Action, failureBefore); err != nil {
		return err
	}
	if authoritative.Apply == nil {
		return errors.New("authoritative mutation action has no apply function")
	}
	if err := authoritative.Apply(ctx); err != nil {
		return err
	}
	authoritativeAfterErr := e.injector.Inject(authoritative.Step.Action, failureAfter)

	var unresolved []error
	var firstStep datastore.MutationStep
	for _, action := range projections {
		if action.Apply == nil {
			if firstStep.Action == "" {
				firstStep = action.Step
			}
			unresolved = append(unresolved, errors.New("projection mutation action has no apply function"))
			continue
		}

		var err error
		for attempt := 0; attempt < 2; attempt++ {
			if injected := e.injector.Inject(action.Step.Action, failureBefore); injected != nil {
				err = injected
			} else {
				err = action.Apply(ctx)
				if err == nil {
					err = e.injector.Inject(action.Step.Action, failureAfter)
				}
			}
			if err == nil {
				break
			}
		}
		if err != nil {
			if firstStep.Action == "" {
				firstStep = action.Step
			}
			unresolved = append(unresolved, fmt.Errorf("%s: %w", action.Step.Projection, err))
		}
	}
	if len(unresolved) == 0 {
		return authoritativeAfterErr
	}
	primary := errors.Join(unresolved...)
	if authoritativeAfterErr != nil {
		primary = errors.Join(authoritativeAfterErr, primary)
	}
	return datastore.NewRepairRequiredError(
		firstStep,
		primary,
		fmt.Errorf("authoritative resource version %s committed; projections require roll-forward", committedResourceVersion),
	)
}

func (e *mutationExecutor) executeDelete(
	ctx context.Context,
	authoritative mutationAction,
	projections ...mutationAction,
) error {
	completed := make([]mutationAction, 0, len(projections))
	for _, action := range projections {
		if err := e.injector.Inject(action.Step.Action, failureBefore); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
		if action.Apply == nil {
			return e.compensate(ctx, completed, action.Step, errors.New("mutation action has no apply function"))
		}
		if err := action.Apply(ctx); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
		completed = append(completed, action)
		if err := e.injector.Inject(action.Step.Action, failureAfter); err != nil {
			return e.compensate(ctx, completed, action.Step, err)
		}
	}

	if err := e.injector.Inject(authoritative.Step.Action, failureBefore); err != nil {
		return e.compensate(ctx, completed, authoritative.Step, err)
	}
	if authoritative.Apply == nil {
		return e.compensate(ctx, completed, authoritative.Step, errors.New("authoritative mutation action has no apply function"))
	}
	if err := authoritative.Apply(ctx); err != nil {
		return e.compensate(ctx, completed, authoritative.Step, err)
	}
	return e.injector.Inject(authoritative.Step.Action, failureAfter)
}

// executeConditionalDelete applies the authoritative CAS before touching any
// projection. A stale resourceVersion therefore fails without mutating lookup
// rows. Once the delete commits, projection failures are handled as roll-forward
// repair work; restoring a stale pre-delete snapshot would be unsafe because a
// concurrent writer may already own different projection keys.
func (e *mutationExecutor) executeConditionalDelete(
	ctx context.Context,
	expectedResourceVersion string,
	authoritative mutationAction,
	projections ...mutationAction,
) error {
	return e.executeUpdate(ctx, expectedResourceVersion, authoritative, projections...)
}

func (e *mutationExecutor) compensate(
	ctx context.Context,
	completed []mutationAction,
	failedStep datastore.MutationStep,
	primary error,
) error {
	var compensationErrors []error
	compensationStep := failedStep
	for i := len(completed) - 1; i >= 0; i-- {
		if completed[i].Compensate == nil {
			continue
		}
		if err := completed[i].Compensate(ctx); err != nil {
			if len(compensationErrors) == 0 {
				compensationStep = completed[i].Step
			}
			compensationErrors = append(compensationErrors, err)
		}
	}
	if len(compensationErrors) > 0 {
		return datastore.NewRepairRequiredError(compensationStep, primary, errors.Join(compensationErrors...))
	}
	return primary
}

func (s *scyllaDatastore) reportFinding(ctx context.Context, finding datastore.ProjectionFinding) {
	datastore.ReportProjectionFinding(ctx, finding)
	s.log.Warn("scylla catalogue projection inconsistency",
		zap.String("operation", finding.Operation),
		zap.String("resource_kind", finding.ResourceKind),
		zap.String("resource_uid", finding.ResourceUID),
		zap.String("projection", finding.Projection),
		zap.String("lookup_key", finding.LookupKey),
		zap.String("finding_type", string(finding.Type)),
	)
}

func (s *scyllaDatastore) reserveName(
	ctx context.Context,
	resourceKind, tableName, namespace, name string,
	owner gocql.UUID,
	createdAt time.Time,
) error {
	_, err := s.reserveNameWithMode(ctx, resourceKind, tableName, namespace, name, owner, createdAt, true)
	return err
}

func (s *scyllaDatastore) reserveNameOwned(
	ctx context.Context,
	resourceKind, tableName, namespace, name string,
	owner gocql.UUID,
	createdAt time.Time,
) (bool, error) {
	return s.reserveNameWithMode(ctx, resourceKind, tableName, namespace, name, owner, createdAt, false)
}

func (s *scyllaDatastore) reserveNameWithMode(
	ctx context.Context,
	resourceKind, tableName, namespace, name string,
	owner gocql.UUID,
	createdAt time.Time,
	repairSameOwner bool,
) (bool, error) {
	statement := fmt.Sprintf(
		"INSERT INTO %s (namespace,name,uid,creation_timestamp) VALUES (?,?,?,?) IF NOT EXISTS",
		tableName,
	)
	return s.reserveOwned(ctx, datastore.MutationStep{
		Operation:    "reserve",
		ResourceKind: resourceKind,
		ResourceUID:  owner.String(),
		Projection:   tableName,
		LookupKey:    namespace + "/" + name,
		Action:       "reserve-name",
	}, statement, []any{namespace, name, owner, createdAt}, owner, func(existing map[string]any) error {
		existingCreated, _ := existing["creation_timestamp"].(time.Time)
		if scyllaTimestampEqual(existingCreated, createdAt) {
			return nil
		}
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: resourceKind,
			ResourceUID:  owner.String(),
			Projection:   tableName,
			LookupKey:    namespace + "/" + name,
			Operation:    "reserve",
			Type:         datastore.FindingStale,
		})
		if !repairSameOwner {
			return fmt.Errorf("%w: %s %s/%s has stale identity metadata", datastore.ErrConflict, tableName, namespace, name)
		}
		applied, err := s.session.Query(
			fmt.Sprintf("UPDATE %s SET creation_timestamp=? WHERE namespace=? AND name=? IF uid=?", tableName),
			nil,
		).WithContext(ctx).Bind(createdAt, namespace, name, owner).ExecCASRelease()
		if err != nil {
			return err
		}
		if !applied {
			return fmt.Errorf("%w: %s %s/%s changed owner during repair", datastore.ErrAlreadyExists, tableName, namespace, name)
		}
		return nil
	})
}

func (s *scyllaDatastore) reserveUID(
	ctx context.Context,
	resourceKind, tableName, namespace string,
	owner gocql.UUID,
	createdAt time.Time,
) error {
	_, err := s.reserveUIDWithMode(ctx, resourceKind, tableName, namespace, owner, createdAt, true)
	return err
}

func (s *scyllaDatastore) reserveUIDOwned(
	ctx context.Context,
	resourceKind, tableName, namespace string,
	owner gocql.UUID,
	createdAt time.Time,
) (bool, error) {
	return s.reserveUIDWithMode(ctx, resourceKind, tableName, namespace, owner, createdAt, false)
}

func (s *scyllaDatastore) reserveUIDWithMode(
	ctx context.Context,
	resourceKind, tableName, namespace string,
	owner gocql.UUID,
	createdAt time.Time,
	repairSameOwner bool,
) (bool, error) {
	statement := fmt.Sprintf(
		"INSERT INTO %s (uid,namespace,creation_timestamp) VALUES (?,?,?) IF NOT EXISTS",
		tableName,
	)
	return s.reserveOwned(ctx, datastore.MutationStep{
		Operation:    "reserve",
		ResourceKind: resourceKind,
		ResourceUID:  owner.String(),
		Projection:   tableName,
		LookupKey:    owner.String(),
		Action:       "reserve-uid",
	}, statement, []any{owner, namespace, createdAt}, owner, func(existing map[string]any) error {
		existingNamespace, _ := existing["namespace"].(string)
		existingCreated, _ := existing["creation_timestamp"].(time.Time)
		if existingNamespace == namespace && scyllaTimestampEqual(existingCreated, createdAt) {
			return nil
		}
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: resourceKind,
			ResourceUID:  owner.String(),
			Projection:   tableName,
			LookupKey:    owner.String(),
			Operation:    "reserve",
			Type:         datastore.FindingStale,
		})
		if !repairSameOwner {
			return fmt.Errorf("%w: %s uid %s has stale identity metadata", datastore.ErrConflict, tableName, owner)
		}
		applied, err := s.session.Query(
			fmt.Sprintf("UPDATE %s SET namespace=?,creation_timestamp=? WHERE uid=? IF namespace=? AND creation_timestamp=?", tableName),
			nil,
		).WithContext(ctx).Bind(namespace, createdAt, owner, existingNamespace, existingCreated).ExecCASRelease()
		if err != nil {
			return err
		}
		if !applied {
			return fmt.Errorf("%w: %s uid %s changed during repair", datastore.ErrConflict, tableName, owner)
		}
		return nil
	})
}

func (s *scyllaDatastore) reserveSKU(
	ctx context.Context,
	namespace, sku string,
	owner gocql.UUID,
	createdAt time.Time,
) error {
	_, err := s.reserveSKUWithMode(ctx, namespace, sku, owner, createdAt, true)
	return err
}

func (s *scyllaDatastore) reserveSKUOwned(
	ctx context.Context,
	namespace, sku string,
	owner gocql.UUID,
	createdAt time.Time,
) (bool, error) {
	return s.reserveSKUWithMode(ctx, namespace, sku, owner, createdAt, false)
}

func (s *scyllaDatastore) reserveSKUWithMode(
	ctx context.Context,
	namespace, sku string,
	owner gocql.UUID,
	createdAt time.Time,
	repairSameOwner bool,
) (bool, error) {
	const tableName = "product_variant_by_sku"
	statement := "INSERT INTO product_variant_by_sku (namespace,sku,uid,creation_timestamp) VALUES (?,?,?,?) IF NOT EXISTS"
	return s.reserveOwned(ctx, datastore.MutationStep{
		Operation:    "reserve",
		ResourceKind: "ProductVariant",
		ResourceUID:  owner.String(),
		Projection:   tableName,
		LookupKey:    namespace + "/" + sku,
		Action:       "reserve-sku",
	}, statement, []any{namespace, sku, owner, createdAt}, owner, func(existing map[string]any) error {
		existingCreated, _ := existing["creation_timestamp"].(time.Time)
		if scyllaTimestampEqual(existingCreated, createdAt) {
			return nil
		}

		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: "ProductVariant",
			ResourceUID:  owner.String(),
			Projection:   tableName,
			LookupKey:    namespace + "/" + sku,
			Operation:    "reserve",
			Type:         datastore.FindingStale,
		})
		if !repairSameOwner {
			return fmt.Errorf("%w: sku %s/%s has stale identity metadata", datastore.ErrConflict, namespace, sku)
		}
		applied, err := s.session.Query(
			"UPDATE product_variant_by_sku SET creation_timestamp=? WHERE namespace=? AND sku=? IF uid=?",
			nil,
		).WithContext(ctx).Bind(createdAt, namespace, sku, owner).ExecCASRelease()
		if err != nil {
			return err
		}
		if !applied {
			return fmt.Errorf("%w: sku %s/%s changed owner during repair", datastore.ErrAlreadyExists, namespace, sku)
		}
		return nil
	})
}

func scyllaTimestampEqual(left, right time.Time) bool {
	return left.UnixMilli() == right.UnixMilli()
}

func (s *scyllaDatastore) reserveOwned(
	ctx context.Context,
	step datastore.MutationStep,
	statement string,
	args []any,
	owner gocql.UUID,
	sameOwner func(map[string]any) error,
) (bool, error) {
	return s.reserveOwnedColumn(ctx, step, statement, args, "uid", owner, sameOwner)
}

func (s *scyllaDatastore) reserveOwnedColumn(
	ctx context.Context,
	step datastore.MutationStep,
	statement string,
	args []any,
	ownerColumn string,
	owner gocql.UUID,
	sameOwner func(map[string]any) error,
) (bool, error) {
	existing := make(map[string]any)
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).MapScanCAS(existing)
	if err != nil {
		return false, fmt.Errorf("scylla: reserve %s: %w", step.Projection, err)
	}
	if applied {
		return true, nil
	}
	actualOwner, ok := asUUID(existing[ownerColumn])
	if !ok || actualOwner != owner {
		s.reportFinding(ctx, datastore.ProjectionFinding{
			ResourceKind: step.ResourceKind,
			ResourceUID:  owner.String(),
			Projection:   step.Projection,
			LookupKey:    step.LookupKey,
			Operation:    step.Operation,
			Type:         datastore.FindingDuplicate,
		})
		return false, fmt.Errorf("%w: %s %s is owned by %v", datastore.ErrAlreadyExists, step.Projection, step.LookupKey, existing[ownerColumn])
	}
	if sameOwner != nil {
		return false, sameOwner(existing)
	}
	return false, nil
}

func (s *scyllaDatastore) releaseName(
	ctx context.Context,
	tableName, namespace, name string,
	owner gocql.UUID,
) error {
	return s.releaseOwned(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE namespace=? AND name=? IF uid=?", tableName),
		[]any{namespace, name, owner},
		datastore.ProjectionFinding{
			ResourceKind: resourceKindForProjection(tableName), ResourceUID: owner.String(), Projection: tableName, LookupKey: namespace + "/" + name,
			Operation: "release", Type: datastore.FindingStale,
		},
	)
}

func (s *scyllaDatastore) releaseUID(
	ctx context.Context,
	tableName, namespace string,
	owner gocql.UUID,
	createdAt time.Time,
) error {
	return s.releaseOwned(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE uid=? IF namespace=? AND creation_timestamp=?", tableName),
		[]any{owner, namespace, createdAt},
		datastore.ProjectionFinding{
			ResourceKind: resourceKindForProjection(tableName), ResourceUID: owner.String(), Projection: tableName, LookupKey: owner.String(),
			Operation: "release", Type: datastore.FindingStale,
		},
	)
}

func (s *scyllaDatastore) releaseSKU(
	ctx context.Context,
	namespace, sku string,
	owner gocql.UUID,
) error {
	return s.releaseOwned(ctx,
		"DELETE FROM product_variant_by_sku WHERE namespace=? AND sku=? IF uid=?",
		[]any{namespace, sku, owner},
		datastore.ProjectionFinding{
			ResourceKind: "ProductVariant", ResourceUID: owner.String(), Projection: "product_variant_by_sku",
			LookupKey: namespace + "/" + sku, Operation: "release", Type: datastore.FindingStale,
		},
	)
}

func (s *scyllaDatastore) releaseOwned(
	ctx context.Context,
	statement string,
	args []any,
	finding datastore.ProjectionFinding,
) error {
	existing := make(map[string]any)
	applied, err := s.session.Query(statement, nil).WithContext(ctx).Bind(args...).MapScanCAS(existing)
	if err != nil {
		return err
	}
	if !applied {
		s.reportFinding(ctx, finding)
	}
	return nil
}

func resourceKindForProjection(tableName string) string {
	switch tableName {
	case "products_by_name", "products_by_uid", "products_by_namespace":
		return "Product"
	case "category_taxonomy_by_name", "category_taxonomy_by_uid", "category_taxonomy":
		return "CategoryTaxonomy"
	case "collection_by_name", "collection_by_uid", "collection":
		return "Collection"
	case "product_variant_by_name", "product_variant_by_uid", "product_variant_by_sku",
		"product_variant_by_product_ref", "product_variant_by_namespace":
		return "ProductVariant"
	case "namespace_mappings":
		return "Repository"
	case "service_accounts_by_name", "service_accounts_by_uid", "service_accounts_by_namespace":
		return "ServiceAccount"
	default:
		return "Unknown"
	}
}

func asUUID(value any) (gocql.UUID, bool) {
	switch typed := value.(type) {
	case gocql.UUID:
		return typed, true
	case *gocql.UUID:
		if typed != nil {
			return *typed, true
		}
	case string:
		parsed, err := gocql.ParseUUID(typed)
		return parsed, err == nil
	case []byte:
		parsed, err := gocql.UUIDFromBytes(typed)
		return parsed, err == nil
	}
	return gocql.UUID{}, false
}
