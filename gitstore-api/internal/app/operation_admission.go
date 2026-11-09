// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"context"
	"sync"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/middleware/security"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type operationAdmission struct {
	profile config.EndpointConfig
	budget  *security.RateLimit
	mu      sync.Mutex
	active  map[string]int
}

func newOperationAdmission(profile config.EndpointConfig) *operationAdmission {
	l := profile.Limits
	return &operationAdmission{profile: profile, budget: security.NewBoundedRateLimit(l.PrincipalPerSecond, l.PrincipalBurst, l.LimiterKeys, l.IdleTTL), active: make(map[string]int)}
}

func principalKey(p *auth.Principal) string {
	if p == nil {
		return "anonymous"
	}
	return p.Issuer + "\x00" + p.Subject
}

func (a *operationAdmission) charge(p *auth.Principal, cost int) bool {
	if p != nil && p.ServiceAccountUID != "" && p.AuthMethod == "serviceaccount-jwt" && p.Subject == a.profile.ControllerSubject {
		return true
	}
	if cost < 1 {
		cost = 1
	}
	return a.budget.Allow(principalKey(p), cost)
}

func (a *operationAdmission) acquire(key string) (func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.active[key] >= a.profile.Limits.WSOperations || (a.active[key] == 0 && len(a.active) >= a.profile.Limits.LimiterKeys) {
		return nil, false
	}
	a.active[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.active[key]--
			if a.active[key] == 0 {
				delete(a.active, key)
			}
		})
	}, true
}

func throttledOperation(ctx context.Context) graphql.ResponseHandler {
	if transport, ok := ctx.Value(operationTransportKey{}).(operationTransport); ok && !transport.websocket {
		transport.writer.Header().Set("Cache-Control", "no-store")
		if writer, ok := transport.writer.(*operationResponseWriter); ok {
			writer.status = 429
		}
	}
	return graphql.OneShot(&graphql.Response{Errors: gqlerror.List{{Message: "operation admission exhausted", Extensions: map[string]any{"code": "THROTTLED"}}}})
}

// middleware runs after the provider authenticator and after document/cost
// validation. No request header can opt into the service identity exemption.
func (a *operationAdmission) middleware(ctx context.Context, next graphql.OperationHandler) graphql.ResponseHandler {
	p := auth.PrincipalFromContext(ctx)
	if p != nil && p.ProfileKind == auth.ProfileCustomer {
		return graphql.OneShot(&graphql.Response{Errors: gqlerror.List{{
			Message:    "profile cannot access management operations",
			Extensions: map[string]any{"code": "FORBIDDEN"},
		}}})
	}
	cost := 1
	if stats := extension.GetComplexityStats(ctx); stats != nil {
		cost = stats.Complexity
	}
	op := graphql.GetOperationContext(ctx)
	transport, _ := ctx.Value(operationTransportKey{}).(operationTransport)
	if !transport.websocket {
		if !a.charge(p, cost) {
			return throttledOperation(ctx)
		}
		return next(ctx)
	}
	release, ok := a.acquire(principalKey(p))
	if !ok {
		return throttledOperation(ctx)
	}
	if !a.charge(p, cost) {
		release()
		return throttledOperation(ctx)
	}
	if op.Operation.Operation != ast.Subscription {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.profile.Limits.Deadline)
		baseRelease := release
		release = func() { cancel(); baseRelease() }
	}
	stop := context.AfterFunc(ctx, release)
	response := next(ctx)
	return func(responseCtx context.Context) *graphql.Response {
		// gqlgen returns the downstream operation context (including our
		// deadline and the authorization ledger) to the transport. Preserve it.
		result := response(responseCtx)
		if result == nil || op.Operation.Operation != ast.Subscription || (len(result.Errors) > 0 && result.Data == nil) {
			stop()
			release()
		}
		return result
	}
}
