// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package notification

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type MailHandler struct {
	provider  NotificationProvider
	key       [32]byte
	bodyBytes int64
	inFlight  chan struct{}
	timeout   time.Duration
	requests  metric.Int64Counter
	duration  metric.Float64Histogram
}

func NewMailHandler(provider NotificationProvider, key string, bodyBytes int64, maxConcurrent int, timeout time.Duration) (*MailHandler, error) {
	if provider == nil || len(key) < 32 || bodyBytes < 1 || bodyBytes > 1<<20 || maxConcurrent < 1 || maxConcurrent > 128 || timeout <= 0 || timeout > 10*time.Second {
		return nil, errors.New("invalid notification configuration")
	}
	meter := otel.Meter("github.com/gitstore-dev/gitstore/api/notification")
	return &MailHandler{
		provider: provider, key: sha256.Sum256([]byte(key)), bodyBytes: bodyBytes,
		inFlight: make(chan struct{}, maxConcurrent), timeout: timeout,
		requests: observability.MustInstrument(meter.Int64Counter("gitstore_identity_mail_requests_total")),
		duration: observability.MustInstrument(meter.Float64Histogram("gitstore_identity_mail_duration_seconds", metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10))),
	}, nil
}

func (h *MailHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	outcome, templateClass := "invalid", "other"
	defer func() {
		opts := metric.WithAttributes(attribute.String("outcome", outcome), attribute.String("template_class", templateClass))
		h.requests.Add(r.Context(), 1, opts)
		h.duration.Record(r.Context(), time.Since(start).Seconds(), opts)
		zap.L().Debug("identity mail delivery", zap.String("outcome", outcome), zap.String("template_class", templateClass),
			zap.String("correlation_id", trace.SpanContextFromContext(r.Context()).TraceID().String()))
	}()
	key := sha256.Sum256([]byte(r.Header.Get("GitStore-Mail-Key")))
	if subtle.ConstantTimeCompare(h.key[:], key[:]) != 1 {
		outcome = "unauthenticated"
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || contentType != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}
	select {
	case h.inFlight <- struct{}{}:
		defer func() { <-h.inFlight }()
	default:
		outcome = "saturated"
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	r.Body = http.MaxBytesReader(w, r.Body, h.bodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
		return
	}
	var message Message
	if json.Unmarshal(body, &message) != nil || !validMessage(message) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	templateClass = mailTemplateClass(message.TemplateType)
	if h.provider.Send(ctx, message) != nil {
		outcome = "unavailable"
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	outcome = "accepted"
	w.WriteHeader(http.StatusNoContent)
}

func validMessage(message Message) bool {
	address, err := mail.ParseAddress(message.Recipient)
	return err == nil && address.Address == message.Recipient && len(message.Recipient) <= 254 &&
		len(message.Subject) > 0 && len(message.Subject) <= 998 && !strings.ContainsAny(message.Subject, "\r\n") &&
		(message.Body != "" || message.HTMLBody != "") && (message.MessageType == "" || message.MessageType == "email")
}

func mailTemplateClass(value string) string {
	switch value {
	case "recovery_invalid", "recovery_valid", "recovery_code_invalid", "recovery_code_valid":
		return "recovery"
	case "verification_invalid", "verification_valid", "verification_code_invalid", "verification_code_valid":
		return "verification"
	case "login_code_valid":
		return "login"
	case "registration_code_valid":
		return "registration"
	case "invitation":
		return "invitation"
	default:
		return "other"
	}
}
