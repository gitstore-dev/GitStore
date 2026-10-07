// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/controller-manager/internal/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	assertionSigningLifetime = 45 * time.Second
	exchangeHTTPTimeout      = 10 * time.Second
)

// CredentialSource abstracts obtaining and renewing a service-account access
// token.
type CredentialSource interface {
	// Current returns the current credential token. If not available,
	// returns an error (e.g., for reporting via health/readiness).
	// Implementations MAY block or use singleflight to avoid concurrent
	// issuance attempts.
	Current(ctx context.Context) (string, error)
}

// StaticToken provides fixed credentials for isolated client tests.
type StaticToken struct {
	token string
}

// NewStaticToken returns a source that always returns token.
func NewStaticToken(token string) *StaticToken {
	return &StaticToken{token: token}
}

// Current returns the configured test credential.
func (s *StaticToken) Current(_ context.Context) (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("static token is empty")
	}
	return s.token, nil
}

// Ready reports whether a test credential is configured.
func (s *StaticToken) Ready() bool {
	return s.token != ""
}

// ServiceAccountSource signs client assertions and exchanges them for
// access tokens via the issueServiceAccountToken mutation (US4).
// It implements automatic renewal, singleflight under concurrency, and
// jittered backoff on failure.
type ServiceAccountSource struct {
	// Configuration
	endpoint            string
	httpClient          *http.Client
	namespace           string
	name                string
	signer              TokenSigner
	assertionAudience   string
	accessTokenAudience string
	defaultTTL          time.Duration
	maxTTL              time.Duration
	maxBackoff          time.Duration
	exchangeTimeout     time.Duration
	metrics             *credentialMetrics

	// State
	mu           sync.Mutex
	token        string
	expiresAt    time.Time
	lastErr      error
	lastErrTime  time.Time
	backoffUntil time.Time
	failures     uint

	// Concurrency control
	inFlight chan struct{}
}

// TokenSigner signs a client assertion and returns the signed JWT.
// Implementations receive key material from the bootstrap SecretResolver.
type TokenSigner interface {
	// SignAssertion creates and signs a client assertion JWT for the given
	// namespace/name service account, to be exchanged for an access token
	// with the specified TTL and audience.
	SignAssertion(ctx context.Context, namespace, name string, ttl time.Duration, audience string) (string, error)
}

// NewServiceAccountSource returns a CredentialSource that automatically
// acquires and renews access tokens by signing client assertions.
// The signer is responsible for loading the service account's private key.
func NewServiceAccountSource(
	endpoint string,
	namespace string,
	name string,
	signer TokenSigner,
	assertionAudience string,
	accessTokenAudience string,
	defaultTTL time.Duration,
	maxTTL time.Duration,
) *ServiceAccountSource {
	return &ServiceAccountSource{
		endpoint:            endpoint,
		httpClient:          &http.Client{Timeout: exchangeHTTPTimeout},
		namespace:           namespace,
		name:                name,
		signer:              signer,
		assertionAudience:   assertionAudience,
		accessTokenAudience: accessTokenAudience,
		defaultTTL:          defaultTTL,
		maxTTL:              maxTTL,
		maxBackoff:          30 * time.Second, // Configurable; 30s is reasonable default
		exchangeTimeout:     exchangeHTTPTimeout,
		metrics:             defaultCredentialMetrics,
		inFlight:            make(chan struct{}, 1),
	}
}

// Current returns the current access token, acquiring or renewing it as needed.
// It uses singleflight to prevent concurrent token issuance attempts.
func (s *ServiceAccountSource) Current(ctx context.Context) (string, error) {
	s.mu.Lock()
	if s.hasReusableToken(time.Now()) {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	if time.Now().Before(s.backoffUntil) {
		if s.hasValidToken(time.Now()) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		err := fmt.Errorf("credential source in backoff: last error at %v: %w", s.lastErrTime, s.lastErr)
		s.mu.Unlock()
		return "", err
	}
	s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("wait for credential exchange: %w", err)
	}
	select {
	case s.inFlight <- struct{}{}:
		defer func() { <-s.inFlight }()
	case <-ctx.Done():
		return "", fmt.Errorf("wait for credential exchange: %w", ctx.Err())
	}

	s.mu.Lock()
	if s.hasReusableToken(time.Now()) {
		token := s.token
		s.mu.Unlock()
		return token, nil
	}
	if time.Now().Before(s.backoffUntil) {
		if s.hasValidToken(time.Now()) {
			token := s.token
			s.mu.Unlock()
			return token, nil
		}
		err := fmt.Errorf("credential source in backoff: last error at %v: %w", s.lastErrTime, s.lastErr)
		s.mu.Unlock()
		return "", err
	}
	s.mu.Unlock()

	started := time.Now()
	s.metrics.start()
	token, expiresAt, err := s.issueToken(ctx)
	s.metrics.finish(err, time.Since(started))

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("service account exchange canceled: %w", ctx.Err())
		}
		s.lastErr = err
		s.lastErrTime = time.Now()
		s.failures++
		s.backoffUntil = s.nextBackoff()
		if s.hasValidToken(time.Now()) {
			return s.token, nil
		}
		return "", fmt.Errorf("failed to issue service account token: %w", err)
	}

	s.token = token
	s.expiresAt = expiresAt
	s.lastErr = nil
	s.backoffUntil = time.Time{}
	s.failures = 0

	return token, nil
}

func (s *ServiceAccountSource) hasReusableToken(now time.Time) bool {
	return s.token != "" && !s.expiresAt.IsZero() && now.Before(s.expiresAt.Add(-30*time.Second))
}

func (s *ServiceAccountSource) hasValidToken(now time.Time) bool {
	return s.token != "" && !s.expiresAt.IsZero() && now.Before(s.expiresAt)
}

func (s *ServiceAccountSource) tokenValid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return token == s.token && s.hasValidToken(time.Now())
}

// issueToken signs an assertion and exchanges it for an access token.
func (s *ServiceAccountSource) issueToken(ctx context.Context) (string, time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, s.exchangeTimeout)
	defer cancel()
	if s.signer == nil {
		return "", time.Time{}, fmt.Errorf("service account signer is nil")
	}
	if s.assertionAudience == "" {
		return "", time.Time{}, fmt.Errorf("service account assertion audience is empty")
	}
	if s.accessTokenAudience == "" {
		return "", time.Time{}, fmt.Errorf("service account access-token audience is empty")
	}
	assertion, err := s.signer.SignAssertion(ctx, s.namespace, s.name, assertionSigningLifetime, s.assertionAudience)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign assertion: %w", err)
	}
	if s.endpoint == "" {
		return "", time.Time{}, fmt.Errorf("service account token endpoint is empty")
	}
	ttl := s.defaultTTL
	if s.maxTTL > 0 && ttl > s.maxTTL {
		ttl = s.maxTTL
	}

	const mutation = `mutation IssueServiceAccountToken($input: IssueServiceAccountTokenInput!) {
		issueServiceAccountToken(input: $input) {
			tokenRequest { status { token expirationTimestamp } }
		}
	}`
	requestBody, err := json.Marshal(gqlRequest{
		Query: mutation,
		Variables: map[string]any{"input": map[string]any{
			"apiVersion": "authentication.gitstore.dev/v1beta1",
			"kind":       "TokenRequest",
			"metadata": map[string]any{
				"namespace": s.namespace,
				"name":      s.name,
			},
			"spec": map[string]any{
				"audiences":         []string{s.accessTokenAudience},
				"expirationSeconds": int(ttl.Seconds()),
			},
		}},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal token exchange request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(requestBody))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build token exchange request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+assertion)
	response, err := s.httpClient.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("send token exchange request: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck
	if response.StatusCode == http.StatusTooManyRequests {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return "", time.Time{}, fmt.Errorf("%w: token exchange returned HTTP 429", types.ErrRateLimited)
	}
	if response.StatusCode >= http.StatusMultipleChoices {
		return "", time.Time{}, fmt.Errorf("token exchange returned HTTP %d", response.StatusCode)
	}

	var result struct {
		Data struct {
			IssueServiceAccountToken struct {
				TokenRequest struct {
					Status struct {
						Token               string    `json:"token"`
						ExpirationTimestamp time.Time `json:"expirationTimestamp"`
					} `json:"status"`
				} `json:"tokenRequest"`
			} `json:"issueServiceAccountToken"`
		} `json:"data"`
		Errors []*Error `json:"errors"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", time.Time{}, fmt.Errorf("decode token exchange response: %w", err)
	}
	if len(result.Errors) > 0 {
		return "", time.Time{}, result.Errors[0]
	}
	if result.Data.IssueServiceAccountToken.TokenRequest.Status.Token == "" || result.Data.IssueServiceAccountToken.TokenRequest.Status.ExpirationTimestamp.IsZero() {
		return "", time.Time{}, fmt.Errorf("token exchange returned an empty token or expiry")
	}
	return result.Data.IssueServiceAccountToken.TokenRequest.Status.Token, result.Data.IssueServiceAccountToken.TokenRequest.Status.ExpirationTimestamp, nil
}

// nextBackoff returns the next backoff deadline with jitter.
// Implements exponential backoff up to maxBackoff.
func (s *ServiceAccountSource) nextBackoff() time.Time {
	delay := time.Second
	for attempts := uint(1); attempts < s.failures && delay < s.maxBackoff; attempts++ {
		if delay > s.maxBackoff/2 {
			delay = s.maxBackoff
		} else {
			delay *= 2
		}
	}
	if delay > s.maxBackoff {
		delay = s.maxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(delay/5)*2+1)) - delay/5
	delay = min(delay+jitter, s.maxBackoff)
	if s.metrics != nil {
		s.metrics.retry(delay)
	}
	return time.Now().Add(delay)
}

// LastError returns the most recent error, if any. Used for health/readiness reporting.
func (s *ServiceAccountSource) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Ready reports whether an access token has been acquired and remains valid.
// It intentionally does not expose the token or the most recent exchange error.
func (s *ServiceAccountSource) Ready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasValidToken(time.Now())
}

// Close releases provider handles after the controller's tracked work stops.
func (s *ServiceAccountSource) Close() error {
	if closer, ok := s.signer.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type credentialMetrics struct {
	exchanges                        *prometheus.CounterVec
	duration                         prometheus.Histogram
	inflight, peakInflight, maxRetry prometheus.Gauge
	mu                               sync.Mutex
	active, peak                     int
	retryPeak                        time.Duration
}

var defaultCredentialMetrics = newCredentialMetrics(prometheus.DefaultRegisterer)

func newCredentialMetrics(registerer prometheus.Registerer) *credentialMetrics {
	factory := promauto.With(registerer)
	metrics := &credentialMetrics{
		exchanges: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "gitstore_controller_credential_exchange_total",
			Help: "Actual assertion/signing/token exchange outcomes; excludes token-cache and backoff reuse.",
		}, []string{"result"}),
		duration: factory.NewHistogram(prometheus.HistogramOpts{
			Name:    "gitstore_controller_credential_exchange_duration_seconds",
			Help:    "Time spent resolving, signing and exchanging a controller assertion.",
			Buckets: []float64{.001, .005, .01, .05, .1, .5, 1, 2, 5, 10},
		}),
		inflight: factory.NewGauge(prometheus.GaugeOpts{
			Name: "gitstore_controller_credential_exchange_inflight",
			Help: "Active token exchanges across this process's credential sources.",
		}),
		peakInflight: factory.NewGauge(prometheus.GaugeOpts{
			Name: "gitstore_controller_credential_exchange_peak_inflight",
			Help: "Maximum simultaneous token exchanges observed since process start.",
		}),
		maxRetry: factory.NewGauge(prometheus.GaugeOpts{
			Name: "gitstore_controller_credential_retry_max_seconds",
			Help: "Maximum actual jittered retry delay scheduled since process start.",
		}),
	}
	for _, outcome := range []string{"success", "failed", "canceled", "deadline_exceeded"} {
		metrics.exchanges.WithLabelValues(outcome)
	}
	return metrics
}

func (m *credentialMetrics) start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active++
	m.peak = max(m.peak, m.active)
	m.inflight.Set(float64(m.active))
	m.peakInflight.Set(float64(m.peak))
}

func (m *credentialMetrics) finish(err error, duration time.Duration) {
	outcome := "success"
	switch {
	case errors.Is(err, context.Canceled):
		outcome = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		outcome = "deadline_exceeded"
	case err != nil:
		outcome = "failed"
	}
	m.exchanges.WithLabelValues(outcome).Inc()
	m.duration.Observe(duration.Seconds())
	m.mu.Lock()
	defer m.mu.Unlock()
	m.active--
	m.inflight.Set(float64(m.active))
}

func (m *credentialMetrics) retry(delay time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.retryPeak = max(m.retryPeak, delay)
	m.maxRetry.Set(m.retryPeak.Seconds())
}
