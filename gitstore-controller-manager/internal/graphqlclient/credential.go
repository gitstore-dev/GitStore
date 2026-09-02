// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"sync"
	"time"
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
func (s *StaticToken) Current(ctx context.Context) (string, error) {
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
		err := fmt.Errorf("credential source in backoff: last error at %v: %w", s.lastErrTime, s.lastErr)
		s.mu.Unlock()
		return "", err
	}
	s.mu.Unlock()

	token, expiresAt, err := s.issueToken(ctx)

	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err
		s.lastErrTime = time.Now()
		s.failures++
		s.backoffUntil = s.nextBackoff()
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

// issueToken signs an assertion and exchanges it for an access token.
func (s *ServiceAccountSource) issueToken(ctx context.Context) (string, time.Time, error) {
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
			status { token expiresAt }
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
				"audience":   s.accessTokenAudience,
				"ttlSeconds": int(ttl.Seconds()),
			},
		}},
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("marshal token exchange request: %w", err)
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, s.exchangeTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(exchangeCtx, http.MethodPost, s.endpoint, bytes.NewReader(requestBody))
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
	if response.StatusCode >= http.StatusMultipleChoices {
		return "", time.Time{}, fmt.Errorf("token exchange returned HTTP %d", response.StatusCode)
	}

	var result struct {
		Data struct {
			IssueServiceAccountToken struct {
				Status struct {
					Token     string    `json:"token"`
					ExpiresAt time.Time `json:"expiresAt"`
				} `json:"status"`
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
	if result.Data.IssueServiceAccountToken.Status.Token == "" || result.Data.IssueServiceAccountToken.Status.ExpiresAt.IsZero() {
		return "", time.Time{}, fmt.Errorf("token exchange returned an empty token or expiry")
	}
	return result.Data.IssueServiceAccountToken.Status.Token, result.Data.IssueServiceAccountToken.Status.ExpiresAt, nil
}

// nextBackoff returns the next backoff deadline with jitter.
// Implements exponential backoff up to maxBackoff.
func (s *ServiceAccountSource) nextBackoff() time.Time {
	delay := time.Second
	for attempts := uint(1); attempts < s.failures && delay < s.maxBackoff/2; attempts++ {
		delay *= 2
	}
	if delay > s.maxBackoff {
		delay = s.maxBackoff
	}
	jitter := time.Duration(rand.Int64N(int64(delay/5)*2+1)) - delay/5
	return time.Now().Add(delay + jitter)
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
	return s.token != "" && !s.expiresAt.IsZero() && time.Now().Before(s.expiresAt)
}
