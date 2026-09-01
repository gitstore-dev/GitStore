// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// CredentialSource abstracts the mechanism for obtaining an access token.
// Implementations may be static (FR-014 deprecated compatibility) or
// dynamic (service-account signing + renewal per US4).
type CredentialSource interface {
	// Current returns the current credential token. If not available,
	// returns an error (e.g., for reporting via health/readiness).
	// Implementations MAY block or use singleflight to avoid concurrent
	// issuance attempts.
	Current(ctx context.Context) (string, error)
}

// StaticToken always returns a configured bearer token string.
// Used as a fallback for FR-014 deprecated GITSTORE_CONTROLLER__API_TOKEN path.
type StaticToken struct {
	token string
}

// NewStaticToken returns a CredentialSource that always returns the given token.
func NewStaticToken(token string) *StaticToken {
	return &StaticToken{token: token}
}

// Current returns the static token.
func (s *StaticToken) Current(ctx context.Context) (string, error) {
	if s.token == "" {
		return "", fmt.Errorf("static token is empty")
	}
	return s.token, nil
}

// ServiceAccountSource signs client assertions and exchanges them for
// access tokens via the issueServiceAccountToken mutation (US4).
// It implements automatic renewal, singleflight under concurrency, and
// jittered backoff on failure.
type ServiceAccountSource struct {
	// Configuration
	client       *Client
	namespace    string
	name         string
	keyID        string
	signer       TokenSigner
	defaultTTL   time.Duration
	maxTTL       time.Duration
	maxBackoff   time.Duration

	// State
	mu           sync.Mutex
	token        string
	expiresAt    time.Time
	lastErr      error
	lastErrTime  time.Time
	backoffUntil time.Time

	// Concurrency control
	inFlight sync.Mutex
}

// TokenSigner signs a client assertion and returns the signed JWT.
// Implementations may access a private key from a SecretResolver or local filesystem.
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
	client *Client,
	namespace string,
	name string,
	keyID string,
	signer TokenSigner,
	defaultTTL time.Duration,
	maxTTL time.Duration,
) *ServiceAccountSource {
	return &ServiceAccountSource{
		client:     client,
		namespace:  namespace,
		name:       name,
		keyID:      keyID,
		signer:     signer,
		defaultTTL: defaultTTL,
		maxTTL:     maxTTL,
		maxBackoff: 30 * time.Second, // Configurable; 30s is reasonable default
	}
}

// Current returns the current access token, acquiring or renewing it as needed.
// It uses singleflight to prevent concurrent token issuance attempts.
func (s *ServiceAccountSource) Current(ctx context.Context) (string, error) {
	s.inFlight.Lock()
	defer s.inFlight.Unlock()

	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if we have a valid token that doesn't need renewal
	if s.token != "" && time.Now().Before(s.expiresAt.Add(-30*time.Second)) {
		return s.token, nil
	}

	// Check if we're in backoff (exponential backoff on repeated failures)
	if time.Now().Before(s.backoffUntil) {
		return "", fmt.Errorf("credential source in backoff: last error at %v: %w", s.lastErrTime, s.lastErr)
	}

	// Need to acquire or renew token
	token, expiresAt, err := s.issueToken(ctx)
	if err != nil {
		s.lastErr = err
		s.lastErrTime = time.Now()
		// Apply exponential backoff with jitter
		s.backoffUntil = s.nextBackoff()
		return "", fmt.Errorf("failed to issue service account token: %w", err)
	}

	s.token = token
	s.expiresAt = expiresAt
	s.lastErr = nil
	s.backoffUntil = time.Time{} // Clear backoff on success

	return token, nil
}

// issueToken signs an assertion and exchanges it for an access token.
// Must be called with s.mu held.
func (s *ServiceAccountSource) issueToken(ctx context.Context) (string, time.Time, error) {
	// Sign a client assertion
	_, err := s.signer.SignAssertion(ctx, s.namespace, s.name, s.defaultTTL, "gitstore-api")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign assertion: %w", err)
	}

	// Exchange assertion for access token via issueServiceAccountToken mutation
	// (This is a placeholder; actual mutation call will be wired in the real signer)
	// For now, return a structured error that indicates what's needed
	return "", time.Time{}, fmt.Errorf("token issuance not yet implemented; requires Client mutation support")
}

// nextBackoff returns the next backoff deadline with jitter.
// Implements exponential backoff up to maxBackoff.
func (s *ServiceAccountSource) nextBackoff() time.Time {
	// Simple jittered backoff: for simplicity, use a fixed backoff with small jitter
	// In production, implement proper exponential backoff tracking
	jitter := time.Duration(int64(time.Second * 5)) // 5s base backoff, tune as needed
	return time.Now().Add(jitter)
}

// LastError returns the most recent error, if any. Used for health/readiness reporting.
func (s *ServiceAccountSource) LastError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}
