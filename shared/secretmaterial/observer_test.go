// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package secretmaterial

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingObserver struct {
	mu       sync.Mutex
	inflight int
	max      int
	reasons  []string
	labels   []Observation
}

func (o *recordingObserver) Inflight(_ Observation, delta int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.inflight += delta
	o.max = max(o.max, o.inflight)
}

func (o *recordingObserver) Observe(labels Observation, reason string, _ time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.labels = append(o.labels, labels)
	o.reasons = append(o.reasons, reason)
}

func TestObservedFailuresAreClassifiedAndRedacted(t *testing.T) {
	for _, class := range []error{
		ErrInvalidRef, ErrNotFound, ErrMissingKey, ErrForbidden,
		ErrProviderUnavailable, ErrUnsupportedType, ErrValueTooLarge,
		context.Canceled, context.DeadlineExceeded, errors.New(marker),
	} {
		t.Run(outcome(class), func(t *testing.T) {
			o := &recordingObserver{}
			p := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
				return []byte(marker), fmt.Errorf("%s: %w", marker, class)
			}}
			r, err := NewBootstrapResolver(p, BootstrapBinding{Owner: marker, Ref: reference()}, o)
			if err != nil {
				t.Fatal(err)
			}
			m, err := r.ResolveSecret(context.Background(), reference(), ResolutionRequest{Principal: marker})
			if err == nil || len(m.Values()) != 0 || strings.Contains(fmt.Sprintf("%#v", err), marker) {
				t.Fatal("failure exposed provider data")
			}
			if len(o.reasons) != 1 || o.reasons[0] != outcome(class) || o.inflight != 0 || o.max != 1 {
				t.Fatalf("incorrect observations: %#v", o)
			}
			if strings.Contains(fmt.Sprintf("%#v", o.labels), marker) {
				t.Fatal("principal leaked into metric dimensions")
			}
		})
	}
}

func TestBootstrapBindingCannotChangeAfterConstruction(t *testing.T) {
	ref := reference()
	ref.Key = ptr("key")
	p := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
		return record(t, map[string]string{"key": marker}), nil
	}}
	r, err := NewBootstrapResolver(p, BootstrapBinding{Owner: "controller", Ref: ref}, nil)
	if err != nil {
		t.Fatal(err)
	}
	*ref.Key = "other"
	if _, err := r.ResolveSecret(context.Background(), ref, ResolutionRequest{Principal: "controller"}); !errors.Is(err, ErrForbidden) {
		t.Fatal("binding changed with caller-owned pointer")
	}
	ref.Key = ptr("key")
	m, err := r.ResolveSecret(context.Background(), ref, ResolutionRequest{Principal: "controller"})
	if err != nil || string(m.Value("key")) != marker {
		t.Fatal("original binding no longer resolves")
	}
}

func TestRuntimeCannotConsumeBootstrapRecord(t *testing.T) {
	p := &fakeProvider{read: func(context.Context, SecretRef, Scope) ([]byte, error) {
		return []byte(`{"format":"serviceaccount-signing-key/v1","values":{"privateKey":"eA==","keyID":"aWQ="}}`), nil
	}}
	r := runtimeResolver(t, p)
	m, err := r.ResolveSecret(context.Background(), reference(), request())
	if !errors.Is(err, ErrForbidden) || len(m.Values()) != 0 {
		t.Fatal("bootstrap record exposed to runtime consumer")
	}
}

func TestProviderOverrunningDeadlineCannotReturnMaterial(t *testing.T) {
	p := &fakeProvider{read: func(ctx context.Context, _ SecretRef, _ Scope) ([]byte, error) {
		<-ctx.Done()
		return record(t, map[string]string{"key": marker}), nil
	}}
	r := runtimeResolver(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	m, err := r.ResolveSecret(ctx, reference(), request())
	if !errors.Is(err, context.DeadlineExceeded) || len(m.Values()) != 0 {
		t.Fatal("late result returned material")
	}
}
