// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package wsregistry tracks authenticated WebSocket connections for one API
// process so service-account lifecycle changes can revoke them immediately.
package wsregistry

import (
	"context"
	"sync"
)

type registrationContextKey struct{}

type registration struct {
	uid string
	id  uint64
}

// Registry maps ServiceAccount UIDs to the cancellation functions of their
// active WebSocket connections. It is intentionally process-local; the
// datastore remains the authoritative check for newly established connections.
type Registry struct {
	closed      bool
	mu          sync.Mutex
	connections map[string]map[uint64]context.CancelFunc
	nextID      uint64
}

func New() *Registry {
	return &Registry{
		connections: make(map[string]map[uint64]context.CancelFunc),
	}
}

// Count reports finite process-local bookkeeping without identity labels.
func (r *Registry) Count() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	count := 0
	for _, connections := range r.connections {
		count += len(connections)
	}
	return count
}

// Register adds cancel to uid's active connections and returns a context that
// CloseFunc can pass to Unregister.
func (r *Registry) Register(ctx context.Context, uid string, cancel context.CancelFunc) context.Context {
	if r == nil || uid == "" || cancel == nil {
		return ctx
	}

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		cancel()
		return ctx
	}
	r.nextID++
	registration := registration{uid: uid, id: r.nextID}
	if r.connections[uid] == nil {
		r.connections[uid] = make(map[uint64]context.CancelFunc)
	}
	r.connections[uid][registration.id] = cancel
	r.mu.Unlock()

	return context.WithValue(ctx, registrationContextKey{}, registration)
}

// Close cancels every owned connection and rejects new registrations during
// shutdown. Callbacks run outside the lock and may unregister themselves.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	var cancels []context.CancelFunc
	for _, connections := range r.connections {
		for _, cancel := range connections {
			cancels = append(cancels, cancel)
		}
	}
	r.connections = make(map[string]map[uint64]context.CancelFunc)
	r.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Unregister removes the connection represented by ctx. It is safe to call
// after CancelAll has already removed the UID's connection set.
func (r *Registry) Unregister(ctx context.Context) {
	if r == nil {
		return
	}
	registration, ok := ctx.Value(registrationContextKey{}).(registration)
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	connections := r.connections[registration.uid]
	delete(connections, registration.id)
	if len(connections) == 0 {
		delete(r.connections, registration.uid)
	}
}

// CancelAll synchronously cancels every active connection for uid.
func (r *Registry) CancelAll(uid string) {
	if r == nil || uid == "" {
		return
	}
	r.mu.Lock()
	connections := r.connections[uid]
	delete(r.connections, uid)
	cancels := make([]context.CancelFunc, 0, len(connections))
	for _, cancel := range connections {
		cancels = append(cancels, cancel)
	}
	r.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}
