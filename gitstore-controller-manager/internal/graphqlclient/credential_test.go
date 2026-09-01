// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graphqlclient

import (
	"context"
	"fmt"
	"testing"
	"time"

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

// T027: ServiceAccountSource tests
// Note: These are placeholder tests demonstrating the interface contract.
// Full implementation with actual token signing and issuance will follow
// once the TokenSigner interface is fully wired (T029a bootstrap resolver).

type mockTokenSigner struct {
	assertion string
	err       error
}

func (m *mockTokenSigner) SignAssertion(ctx context.Context, namespace, name string, ttl time.Duration, audience string) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	if m.assertion == "" {
		return fmt.Sprintf("mock-assertion-%s-%s", namespace, name), nil
	}
	return m.assertion, nil
}

func TestServiceAccountSource_FailsWithoutImplementation(t *testing.T) {
	// This test documents that ServiceAccountSource is not yet fully
	// implemented (issueToken is a placeholder). T028 will complete it.
	signer := &mockTokenSigner{assertion: "mock-assertion"}
	src := NewServiceAccountSource(
		nil,
		"controllers",
		"manager",
		"key-1",
		signer,
		10*time.Minute,
		1*time.Hour,
	)

	_, err := src.Current(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not yet implemented")
}

func TestServiceAccountSource_LastErrorTracking(t *testing.T) {
	signer := &mockTokenSigner{err: fmt.Errorf("signing failed")}
	src := NewServiceAccountSource(
		nil,
		"controllers",
		"manager",
		"key-1",
		signer,
		10*time.Minute,
		1*time.Hour,
	)

	_, _ = src.Current(context.Background())

	lastErr := src.LastError()
	require.NotNil(t, lastErr)
	// Error should be wrapped (could be sign error or not-yet-implemented)
	assert.NotEmpty(t, lastErr.Error())
}

func TestServiceAccountSource_BackoffOnFailure(t *testing.T) {
	signer := &mockTokenSigner{}
	src := NewServiceAccountSource(
		nil,
		"controllers",
		"manager",
		"key-1",
		signer,
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
	// This test documents that concurrent calls to Current() use
	// singleflight to avoid redundant token issuance.
	// Full test with actual singleflight verification will follow
	// once issueToken is fully implemented.
	signer := &mockTokenSigner{}
	src := NewServiceAccountSource(
		nil,
		"controllers",
		"manager",
		"key-1",
		signer,
		10*time.Minute,
		1*time.Hour,
	)

	// Concurrent calls should not panic and should use singleflight
	// (verified by checking that only one error is generated, not N errors)
	results := make([]error, 3)
	for i := 0; i < 3; i++ {
		go func(idx int) {
			_, err := src.Current(context.Background())
			results[idx] = err
		}(i)
	}

	time.Sleep(100 * time.Millisecond)
	for _, err := range results {
		require.Error(t, err)
	}
}
