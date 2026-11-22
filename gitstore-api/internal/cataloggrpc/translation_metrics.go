// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"errors"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/admission"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/validate"
	"github.com/gitstore-dev/gitstore/observability"
	"github.com/gitstore-dev/gitstore/translation"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type translationValidationStage string

const (
	translationValidationSource translationValidationStage = "source"
	translationValidationTarget translationValidationStage = "target"
)

type translationMetrics struct {
	validation metric.Float64Counter
	duration   metric.Float64Histogram
	admission  metric.Float64Counter
	admitTime  metric.Float64Histogram
}

var defaultTranslationMetrics = newTranslationMetrics(otel.Meter("gitstore/translation/catalog"))

func newTranslationMetrics(meter metric.Meter) *translationMetrics {
	return &translationMetrics{
		validation: observability.MustInstrument(meter.Float64Counter("gitstore_translation_validation_total",
			metric.WithDescription("Translation validation stages by bounded stage and outcome; not whole-request admission."))),
		duration: observability.MustInstrument(meter.Float64Histogram("gitstore_translation_validation_duration_seconds",
			metric.WithDescription("Translation validation stage duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5))),
		admission: observability.MustInstrument(meter.Float64Counter("gitstore_translation_admission_total",
			metric.WithDescription("Translation source projection attempts by bounded outcome."))),
		admitTime: observability.MustInstrument(meter.Float64Histogram("gitstore_translation_admission_duration_seconds",
			metric.WithDescription("Translation source projection attempt duration."), metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5))),
	}
}

func (m *translationMetrics) observeSource(ctx context.Context, started time.Time, parsed *validate.ParsedResource, err error) {
	var issue *translation.Error
	if (parsed == nil || parsed.Translation == nil) && !errors.As(err, &issue) {
		return
	}
	m.observeValidation(ctx, translationValidationSource, started, err, false)
}

func (m *translationMetrics) observeValidation(ctx context.Context, stage translationValidationStage, started time.Time, err error, unresolved bool) {
	if m == nil {
		return
	}
	if stage != translationValidationSource && stage != translationValidationTarget {
		stage = "other"
	}
	outcome := translationValidationOutcome(ctx, stage, err, unresolved)
	attributes := metric.WithAttributes(attribute.String("stage", string(stage)), attribute.String("outcome", outcome))
	m.validation.Add(ctx, 1, attributes)
	m.duration.Record(ctx, time.Since(started).Seconds(), attributes)
}

func translationValidationOutcome(ctx context.Context, stage translationValidationStage, err error, unresolved bool) string {
	outcome := "accepted"
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	var issue *translation.Error
	switch {
	case errors.Is(err, context.Canceled) || status.Code(err) == codes.Canceled:
		outcome = "canceled"
	case errors.Is(err, context.DeadlineExceeded) || status.Code(err) == codes.DeadlineExceeded:
		outcome = "deadline"
	case errors.As(err, &issue):
		outcome = "rejected"
		if issue.Reason == "TargetWorkUnavailable" || issue.Reason == "TargetUnavailable" {
			outcome = "unavailable"
		}
	case status.Code(err) == codes.PermissionDenied || status.Code(err) == codes.Unauthenticated:
		outcome = "forbidden"
	case status.Code(err) == codes.ResourceExhausted:
		outcome = "overloaded"
	case errors.Is(err, datastore.ErrConflict) || status.Code(err) == codes.Aborted:
		outcome = "conflict"
	case err != nil:
		outcome = "unavailable"
		if stage == translationValidationSource {
			outcome = "rejected"
		}
	case unresolved:
		outcome = "unresolved"
	}
	return outcome
}

func (m *translationMetrics) observeAdmission(ctx context.Context, started time.Time, decision admission.EntryDecision) {
	if m == nil {
		return
	}
	outcome := "unavailable"
	switch decision.Outcome {
	case admission.EntryAccepted:
		outcome = "accepted"
	case admission.EntryNoOp:
		outcome = "no_op"
	case admission.EntryDenied:
		outcome = "denied"
	case admission.EntryFailed:
		if decision.Err != nil {
			outcome = translationValidationOutcome(ctx, translationValidationTarget, decision.Err, false)
		}
	}
	attributes := metric.WithAttributes(attribute.String("outcome", outcome))
	m.admission.Add(ctx, 1, attributes)
	m.admitTime.Record(ctx, time.Since(started).Seconds(), attributes)
}
