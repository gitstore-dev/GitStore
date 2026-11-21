// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package notification

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type testProvider struct {
	calls            int
	err              error
	entered, release chan struct{}
}

func (p *testProvider) Send(ctx context.Context, message Message) error {
	p.calls++
	if p.entered != nil {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.err
}

type unreadableBody struct{}

func (unreadableBody) Read([]byte) (int, error) { panic("unauthenticated request body read") }
func (unreadableBody) Close() error             { return nil }

func TestMailAuthenticationBoundsAndProviderAcceptance(t *testing.T) {
	const token = "owned-only-mail-credential-32-characters"
	for _, scenario := range []string{"success", "unauthenticated", "malformed", "oversized", "outage", "trailing-json", "header-injection", "wrong-method", "wrong-content-type"} {
		t.Run(scenario, func(t *testing.T) {
			provider := &testProvider{}
			handler, err := NewMailHandler(provider, token, 512, 1, 50*time.Millisecond)
			require.NoError(t, err)
			body := `{"recipient":"recipient@example.test","subject":"Verify","body":"private verification code","template_type":"verification_code_valid"}`
			want := http.StatusNoContent
			switch scenario {
			case "malformed":
				body, want = "{", http.StatusBadRequest
			case "oversized":
				body, want = strings.Repeat("x", 513), http.StatusRequestEntityTooLarge
			case "outage":
				provider.err, want = errors.New("private error"), http.StatusServiceUnavailable
			case "trailing-json":
				body, want = body+"{}", http.StatusBadRequest
			case "header-injection":
				body, want = `{"recipient":"recipient@example.test","subject":"bad\r\nBcc: other@example.test","body":"body"}`, http.StatusBadRequest
			case "unauthenticated":
				want = http.StatusUnauthorized
			case "wrong-method":
				want = http.StatusMethodNotAllowed
			case "wrong-content-type":
				want = http.StatusUnsupportedMediaType
			}
			request := httptest.NewRequest(http.MethodPost, "/internal/notifications/mail", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if scenario == "wrong-method" {
				request.Method = http.MethodGet
			}
			if scenario == "wrong-content-type" {
				request.Header.Set("Content-Type", "text/plain")
			}
			if scenario == "unauthenticated" {
				request.Body = unreadableBody{}
			} else {
				request.Header.Set("GitStore-Mail-Key", token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, want, response.Code)
			require.NotContains(t, response.Body.String(), "private")
			require.NotContains(t, response.Body.String(), "recipient")
			if scenario == "success" || scenario == "outage" {
				require.Equal(t, 1, provider.calls)
			} else {
				require.Zero(t, provider.calls)
			}
		})
	}
}

func TestMailSaturationDoesNotQueueAndCancellationReleasesSlot(t *testing.T) {
	provider := &testProvider{entered: make(chan struct{}), release: make(chan struct{})}
	const token = "owned-only-mail-credential-32-characters"
	handler, err := NewMailHandler(provider, token, 512, 1, 50*time.Millisecond)
	require.NoError(t, err)
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"recipient":"a@example.test","subject":"hello","body":"body"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("GitStore-Mail-Key", token)
		return req
	}

	first, done := httptest.NewRecorder(), make(chan struct{})
	go func() { handler.ServeHTTP(first, request()); close(done) }()
	<-provider.entered
	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	require.Equal(t, http.StatusServiceUnavailable, second.Code)
	<-done
	require.Equal(t, http.StatusServiceUnavailable, first.Code)
	provider.entered = nil
	third := httptest.NewRecorder()
	handler.ServeHTTP(third, request())
	require.Equal(t, http.StatusNoContent, third.Code)
}

func TestMailTelemetryUsesSDKAndNeverRecordsMessageOrCredentialFields(t *testing.T) {
	provider := &testProvider{err: errors.New("private provider response")}
	const key = "private-owned-mail-key-at-least-32-characters"
	handler, err := NewMailHandler(provider, key, 1024, 1, time.Second)
	require.NoError(t, err)
	registry := prometheus.NewRegistry()
	meter := observabilitytest.Meter(t, registry, "gitstore/notification")
	handler.requests, err = meter.Int64Counter("gitstore_identity_mail_requests_total")
	require.NoError(t, err)
	handler.duration, err = meter.Float64Histogram("gitstore_identity_mail_duration_seconds")
	require.NoError(t, err)
	core, entries := observer.New(zap.DebugLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"recipient":"private@example.test","subject":"private subject","body":"private verification code","template_type":"private injected template","request_headers":{"Authorization":"private credential"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("GitStore-Mail-Key", key)
	handler.ServeHTTP(httptest.NewRecorder(), request)
	require.Equal(t, float64(1), observabilitytest.Value(t, registry, "gitstore_identity_mail_requests_total",
		map[string]string{"outcome": "unavailable", "template_class": "other"}))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, sample := range family.Metric {
			for _, label := range sample.Label {
				require.Contains(t, []string{"outcome", "template_class"}, label.GetName())
				require.NotContains(t, label.GetValue(), "private")
			}
		}
	}
	require.Len(t, entries.All(), 1)
	fields := entries.All()[0].ContextMap()
	require.Len(t, fields, 3)
	for name, value := range fields {
		require.Contains(t, []string{"outcome", "template_class", "correlation_id"}, name)
		require.NotContains(t, value, "private")
	}
}
