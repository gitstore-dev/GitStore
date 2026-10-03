// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// T026: StaticToken tests (unchanged, always returns configured string)
func TestStaticToken_ReturnsConfiguredToken(t *testing.T) {
	token := "test-token-12345"
	src := NewStaticToken(token)

	current, err := src.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, token, current)
}

func TestStaticToken_EmptyToken_ReturnsError(t *testing.T) {
	src := NewStaticToken("")

	_, err := src.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

func TestStaticToken_MultipleCallsSameToken(t *testing.T) {
	token := "consistent-token"
	src := NewStaticToken(token)

	for i := 0; i < 5; i++ {
		current, err := src.Current(context.Background())
		require.NoError(t, err)
		assert.Equal(t, token, current)
	}
}

type mockTokenSigner struct {
	assertion string
	err       error
	mu        sync.Mutex
	ttl       time.Duration
	audience  string
}

func (m *mockTokenSigner) SignAssertion(ctx context.Context, namespace, name string, ttl time.Duration, audience string) (string, error) {
	m.mu.Lock()
	m.ttl = ttl
	m.audience = audience
	m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	if m.assertion == "" {
		return fmt.Sprintf("mock-assertion-%s-%s", namespace, name), nil
	}
	return m.assertion, nil
}

func TestServiceAccountSource_FailsWithoutEndpoint(t *testing.T) {
	signer := &mockTokenSigner{assertion: "mock-assertion"}
	src := NewServiceAccountSource(
		"",
		"controllers",
		"manager",
		signer,
		"gitstore-api/serviceaccount-token",
		"gitstore-api",
		10*time.Minute,
		1*time.Hour,
	)

	_, err := src.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "endpoint is empty")
}

func TestServiceAccountSource_LastErrorTracking(t *testing.T) {
	signer := &mockTokenSigner{err: fmt.Errorf("signing failed")}
	src := NewServiceAccountSource(
		"",
		"controllers",
		"manager",
		signer,
		"gitstore-api/serviceaccount-token",
		"gitstore-api",
		10*time.Minute,
		1*time.Hour,
	)

	_, _ = src.Current(context.Background())

	lastErr := src.LastError()
	require.NotNil(t, lastErr)
	assert.NotEmpty(t, lastErr.Error())
}

func TestServiceAccountSource_BackoffOnFailure(t *testing.T) {
	signer := &mockTokenSigner{}
	src := NewServiceAccountSource(
		"",
		"controllers",
		"manager",
		signer,
		"gitstore-api/serviceaccount-token",
		"gitstore-api",
		10*time.Minute,
		1*time.Hour,
	)

	// First attempt should fail and enter backoff
	_, err1 := src.Current(context.Background())
	require.Error(t, err1)

	// Immediate second attempt should fail with backoff error
	_, err2 := src.Current(context.Background())
	require.Error(t, err2)
	assert.Contains(t, err2.Error(), "backoff")
}

func TestServiceAccountSource_ConcurrentCalls_UseSingleflight(t *testing.T) {
	signer := &mockTokenSigner{}
	src := NewServiceAccountSource(
		"",
		"controllers",
		"manager",
		signer,
		"gitstore-api/serviceaccount-token",
		"gitstore-api",
		10*time.Minute,
		1*time.Hour,
	)

	results := make([]error, 3)
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, err := src.Current(context.Background())
			results[idx] = err
		}(i)
	}

	wg.Wait()
	for _, err := range results {
		require.Error(t, err)
	}
}

func TestServiceAccountSource_ExchangesAndReusesToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		assert.Equal(t, "Bearer signed-assertion", r.Header.Get("Authorization"))
		require.NoError(t, json.NewEncoder(w).Encode(tokenExchangeResponse("access-token")))
	}))
	defer server.Close()

	src := NewServiceAccountSource(server.URL, "controllers", "manager", &mockTokenSigner{assertion: "signed-assertion"}, "gitstore-api/serviceaccount-token", "gitstore-api", 10*time.Minute, time.Hour)
	token, err := src.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "access-token", token)
	token, err = src.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "access-token", token)
	assert.Equal(t, int32(1), calls.Load())
}

func TestServiceAccountSource_ConcurrentExchangeUsesSingleflight(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		require.NoError(t, json.NewEncoder(w).Encode(tokenExchangeResponse("access-token")))
	}))
	defer server.Close()

	src := NewServiceAccountSource(server.URL, "controllers", "manager", &mockTokenSigner{}, "gitstore-api/serviceaccount-token", "gitstore-api", 10*time.Minute, time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := src.Current(context.Background())
			if err == nil && token != "access-token" {
				err = fmt.Errorf("token = %q", token)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), calls.Load())
}

func TestServiceAccountSource_UsesConfiguredAudiencesAndSigningLifetime(t *testing.T) {
	signer := &mockTokenSigner{assertion: "signed-assertion"}
	var request struct {
		Variables struct {
			Input struct {
				Spec struct {
					Audiences []string `json:"audiences"`
				} `json:"spec"`
			} `json:"input"`
		} `json:"variables"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.NoError(t, json.NewEncoder(w).Encode(tokenExchangeResponse("access-token")))
	}))
	defer server.Close()

	src := NewServiceAccountSource(
		server.URL,
		"controllers",
		"manager",
		signer,
		"controller-token-exchange",
		"controller-api",
		10*time.Minute,
		time.Hour,
	)
	_, err := src.Current(context.Background())
	require.NoError(t, err)

	signer.mu.Lock()
	assert.Equal(t, assertionSigningLifetime, signer.ttl)
	assert.Equal(t, "controller-token-exchange", signer.audience)
	signer.mu.Unlock()
	assert.Equal(t, []string{"controller-api"}, request.Variables.Input.Spec.Audiences)
}

func TestServiceAccountSource_RecoversAfterBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(tokenExchangeResponse("access-token")))
	}))
	defer server.Close()

	src := NewServiceAccountSource(server.URL, "controllers", "manager", &mockTokenSigner{}, "gitstore-api/serviceaccount-token", "gitstore-api", 10*time.Minute, time.Hour)
	_, err := src.Current(context.Background())
	require.Error(t, err)
	src.mu.Lock()
	src.backoffUntil = time.Now().Add(-time.Second)
	src.mu.Unlock()

	token, err := src.Current(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "access-token", token)
	assert.Equal(t, uint(0), src.failures)
}

func TestServiceAccountSourceReadinessRequiresUsableToken(t *testing.T) {
	src := NewServiceAccountSource(
		"",
		"controllers",
		"manager",
		&mockTokenSigner{},
		"gitstore-api/serviceaccount-token",
		"gitstore-api",
		10*time.Minute,
		time.Hour,
	)
	if src.Ready() {
		t.Fatal("Ready() = true before first token exchange")
	}

	src.mu.Lock()
	src.token = "access-token"
	src.expiresAt = time.Now().Add(time.Minute)
	src.mu.Unlock()
	if !src.Ready() {
		t.Fatal("Ready() = false with a valid access token")
	}

	src.mu.Lock()
	src.expiresAt = time.Now().Add(-time.Second)
	src.mu.Unlock()
	if src.Ready() {
		t.Fatal("Ready() = true with an expired access token")
	}
}

func TestServiceAccountSource_CancellationWhileExchangeInFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		require.NoError(t, json.NewEncoder(w).Encode(tokenExchangeResponse("access-token")))
	}))
	defer server.Close()

	src := NewServiceAccountSource(server.URL, "controllers", "manager", &mockTokenSigner{}, "gitstore-api/serviceaccount-token", "gitstore-api", 10*time.Minute, time.Hour)
	leaderResult := make(chan error, 1)
	go func() {
		_, err := src.Current(context.Background())
		leaderResult <- err
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	waiterResult := make(chan error, 1)
	go func() {
		_, err := src.Current(ctx)
		waiterResult <- err
	}()
	cancel()
	select {
	case err := <-waiterResult:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("waiting caller did not return after context cancellation")
	}

	readyResult := make(chan bool, 1)
	go func() { readyResult <- src.Ready() }()
	select {
	case <-readyResult:
	case <-time.After(time.Second):
		t.Fatal("state mutex was held during token exchange")
	}

	close(release)
	require.NoError(t, <-leaderResult)
}

func TestServiceAccountSource_ExchangeHasBoundedTimeout(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
	}))
	defer server.Close()

	src := NewServiceAccountSource(server.URL, "controllers", "manager", &mockTokenSigner{}, "gitstore-api/serviceaccount-token", "gitstore-api", 10*time.Minute, time.Hour)
	src.exchangeTimeout = 50 * time.Millisecond

	result := make(chan error, 1)
	go func() {
		_, err := src.Current(context.Background())
		result <- err
	}()
	<-started
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("token exchange did not obey its configured timeout")
	}
	close(release)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func tokenExchangeResponse(token string) map[string]any {
	return map[string]any{
		"data": map[string]any{"issueServiceAccountToken": map[string]any{"tokenRequest": map[string]any{"status": map[string]any{
			"token": token, "expirationTimestamp": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		}}}},
	}
}

func TestCredentialMetricsMeasureExchangeNotAcquisition(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newCredentialMetrics(registry)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
			"issueServiceAccountToken": map[string]any{"tokenRequest": map[string]any{"status": map[string]any{
				"token": "private-access-token", "expirationTimestamp": time.Now().Add(time.Minute),
			}}},
		}})
	}))
	defer server.Close()
	source := NewServiceAccountSource(server.URL, "private-namespace", "private-name",
		&mockTokenSigner{assertion: "private-assertion"}, "assertion", "access", time.Minute, time.Minute)
	source.metrics = metrics
	var callers sync.WaitGroup
	for range 32 {
		callers.Add(1)
		go func() {
			defer callers.Done()
			_, err := source.Current(t.Context())
			if err != nil {
				t.Error("credential exchange failed")
			}
		}()
	}
	callers.Wait()
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.exchanges.WithLabelValues("success")))
	require.Zero(t, testutil.ToFloat64(metrics.inflight))
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.peakInflight))
	families, err := registry.Gather()
	require.NoError(t, err)
	data, err := json.Marshal(families)
	require.NoError(t, err)
	for _, marker := range []string{"private-access-token", "private-assertion", "private-name", "private-namespace"} {
		require.NotContains(t, string(data), marker)
	}
}

func TestCredentialMetricsFailureCancellationAndRetry(t *testing.T) {
	metrics := newCredentialMetrics(prometheus.NewRegistry())
	source := NewServiceAccountSource("", "ns", "name", &mockTokenSigner{err: errors.New("private-provider-error")},
		"assertion", "access", time.Minute, time.Minute)
	source.metrics = metrics
	_, err := source.Current(t.Context())
	require.Error(t, err)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.exchanges.WithLabelValues("failed")))
	require.Zero(t, testutil.ToFloat64(metrics.exchanges.WithLabelValues("success")))
	require.Zero(t, testutil.ToFloat64(metrics.inflight))
	require.Greater(t, testutil.ToFloat64(metrics.maxRetry), float64(0))
	require.LessOrEqual(t, testutil.ToFloat64(metrics.maxRetry), float64(30))
	_, err = source.Current(t.Context())
	require.Error(t, err)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.exchanges.WithLabelValues("failed")), "backoff reuse is not another exchange")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	source.backoffUntil = time.Time{}
	_, err = source.Current(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, testutil.ToFloat64(metrics.exchanges.WithLabelValues("canceled")), "a canceled waiter did not attempt issuance")
}

func TestCredentialMetricsCanceledExchangeReturnsToZero(t *testing.T) {
	metrics := newCredentialMetrics(prometheus.NewRegistry())
	source := NewServiceAccountSource("", "ns", "name", waitingSigner{}, "assertion", "access", time.Minute, time.Minute)
	source.metrics = metrics
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := source.Current(ctx)
		done <- err
	}()
	require.Eventually(t, func() bool { return testutil.ToFloat64(metrics.inflight) == 1 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, float64(1), testutil.ToFloat64(metrics.exchanges.WithLabelValues("canceled")))
	require.Zero(t, testutil.ToFloat64(metrics.inflight))
	require.Zero(t, testutil.ToFloat64(metrics.maxRetry))
	require.Nil(t, source.LastError(), "caller cancellation must not poison shared backoff")
}
