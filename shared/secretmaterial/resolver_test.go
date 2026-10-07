// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAuthorizationBeforeProvider(t *testing.T) {
	p := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
		t.Fatal("unauthorized provider call")
		return nil, nil
	}}
	if _, err := NewRuntimeResolver(p, RuntimeBinding{Environment: "dev", Namespace: "shop"}, nil); !errors.Is(err, ErrForbidden) {
		t.Fatal("missing authorizer accepted")
	}
	r := runtimeResolver(t, p)
	req := request()
	req.Resource.Namespace = "foreign"
	if _, err := r.ResolveSecret(context.Background(), reference(), req); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	ref := reference()
	ref.Namespace = ptr("foreign")
	if _, err := r.ResolveSecret(context.Background(), ref, request()); !errors.Is(err, ErrInvalidRef) {
		t.Fatal(err)
	}
	creds := CredentialsRef{Kind: "CredentialsRef", Type: "future/v1", SecretRef: reference()}
	if _, err := r.ResolveCredentials(context.Background(), creds, request()); !errors.Is(err, ErrUnsupportedType) {
		t.Fatal(err)
	}
	b, err := NewBootstrapResolver(p, BootstrapBinding{Owner: "controller", Ref: reference()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ResolveSecret(context.Background(), reference(), request()); !errors.Is(err, ErrForbidden) {
		t.Fatal("runtime caller accessed bootstrap")
	}
	denied, err := NewRuntimeResolver(p, RuntimeBinding{
		Environment: "dev", Namespace: "shop",
		Authorize: func(context.Context, ResolutionRequest, SecretRef) error { return errors.New(marker) },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := denied.ResolveSecret(context.Background(), reference(), request()); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if p.calls.Load() != 0 {
		t.Fatal("provider accessed on denied path")
	}
}

func TestTypedResolutionAndNoCache(t *testing.T) {
	revision := marker
	p := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
		return record(t, map[string]string{"accessKeyId": revision, "secretAccessKey": marker}), nil
	}}
	r := runtimeResolver(t, p)
	ref := CredentialsRef{Kind: "CredentialsRef", Type: "aws-access-key/v1", SecretRef: reference()}
	for _, next := range []string{"first", "second"} {
		revision = next
		m, err := r.ResolveCredentials(context.Background(), ref, request())
		if err != nil || string(m.Value("accessKeyId")) != next {
			t.Fatalf("fresh resolution failed: %v", err)
		}
		m.Clear()
	}
	ref.SecretRef.Key = ptr("accessKeyId")
	m, err := r.ResolveCredentials(context.Background(), ref, request())
	if !errors.Is(err, ErrMissingKey) || len(m.Values()) != 0 {
		t.Fatal("typed resolution loaded unauthorized sibling or returned partial material")
	}
	if p.calls.Load() != 3 {
		t.Fatal("resolver cached material")
	}
}

func TestBoundedConcurrencyAndCancellation(t *testing.T) {
	entered := make(chan struct{}, MaxInflight)
	release := make(chan struct{})
	p := &fakeProvider{read: func(ctx context.Context, _ SecretRef, _ Scope) ([]byte, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > ResolutionLimit {
			t.Error("missing resolution budget")
		}
		entered <- struct{}{}
		select {
		case <-release:
			return []byte(`{"format":"secret-record/v1","values":{"key":"eA=="}}`), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	r := runtimeResolver(t, p)
	var wg sync.WaitGroup
	for i := 0; i < MaxInflight; i++ {
		wg.Go(func() {
			_, err := r.ResolveSecret(context.Background(), reference(), request())
			if err != nil {
				t.Error(err)
			}
		})
	}
	for i := 0; i < MaxInflight; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("provider calls did not start")
		}
	}
	for i := 0; i < MaxInflight; i++ {
		start := time.Now()
		if _, err := r.ResolveSecret(context.Background(), reference(), request()); !errors.Is(err, ErrProviderUnavailable) || time.Since(start) > 100*time.Millisecond {
			t.Fatal("saturation queued or was not classified")
		}
	}
	close(release)
	wg.Wait()
	if p.calls.Load() != MaxInflight {
		t.Fatal("inflight cap exceeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.ResolveSecret(ctx, reference(), request()); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation")
	}
	p.read = func(ctx context.Context, _ SecretRef, _ Scope) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := r.ResolveSecret(ctx, reference(), request()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("caller deadline not preserved")
	}
}
