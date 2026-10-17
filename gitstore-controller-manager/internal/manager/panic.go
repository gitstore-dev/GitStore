// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package manager

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/health"
	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
)

// PanicError wraps a reconciler panic for structured logging and metric emission.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string {
	return fmt.Sprintf("reconciler panic: %v", e.Value)
}

// safeReconcile wraps r.Reconcile in a deferred recover. Any panic is captured
// as a PanicError and returned as ResultTransient so the retry engine handles it.
func safeReconcile(r Reconciler, key WorkItemKey, loggers ...*zap.Logger) func(context.Context) types.ReconcileResult {
	return func(ctx context.Context) types.ReconcileResult {
		started := time.Now()
		ctx, span := otel.Tracer("gitstore/controller-manager").Start(ctx, "controller.reconcile")
		defer span.End()
		kind := key.Kind
		switch kind {
		case "Namespace", "Repository", "CategoryTaxonomy", "Product", "File":
		default:
			kind = "other"
		}
		span.SetAttributes(attribute.String("resource.kind", kind))
		return func() (result types.ReconcileResult) {
			defer func() {
				if v := recover(); v != nil {
					result = types.ResultTransient(&PanicError{
						Value: v,
						Stack: debug.Stack(),
					})
				}
				outcome := "unknown"
				switch result.(type) {
				case types.Success:
					outcome = "success"
				case types.RequeueAfter:
					outcome = "requeue"
				case types.TerminalFailure:
					outcome = "terminal_failure"
				case types.TransientFailure:
					outcome = "transient_failure"
				}
				if ctx.Err() != nil {
					outcome = "cancelled"
				}
				span.SetAttributes(attribute.String("reconcile.result", outcome))
				if outcome != "success" && outcome != "requeue" {
					span.SetStatus(codes.Error, outcome)
				}
				elapsed := time.Since(started)
				observability.Observe(ctx, health.ReconcileDuration.WithLabelValues(kind, outcome), elapsed.Seconds())
				if len(loggers) > 0 {
					log := observability.Logger(ctx, loggers[0])
					fields := []zap.Field{zap.String("kind", kind), zap.String("result", outcome), zap.Duration("duration", elapsed)}
					if outcome == "success" || outcome == "requeue" {
						if observability.Sampled(ctx) && span.IsRecording() {
							log.Info("reconcile attempt completed", fields...)
						} else {
							log.Debug("reconcile attempt completed", fields...)
						}
					} else {
						log.Warn("reconcile attempt failed", fields...)
					}
				}
			}()
			return r.Reconcile(ctx, key)
		}()
	}
}
