// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

type Provider struct {
	config                config.KratosUserDirConfig
	client                *http.Client
	requests              chan struct{}
	cache                 profileCache
	metrics               *metrics
	log                   *zap.Logger
	invalidationMu        sync.RWMutex
	invalidateConnections func()
	invitationMu          sync.RWMutex
	sendInvitation        func(context.Context, *auth.UserProfile, string) error
	webhookHandler        http.Handler
	consumerMu            sync.Mutex
	consumerCancel        context.CancelFunc
	consumerDone          chan struct{}
	consumerClosed        bool
}

var _ auth.ActorDirectory = (*Provider)(nil)
var _ auth.HumanDirectory = (*Provider)(nil)

func New(cfg config.KratosUserDirConfig, logs ...*zap.Logger) (*Provider, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	cfg.Mappings = append([]config.SubjectMapping(nil), cfg.Mappings...)
	cfg.CustomerGroups = append([]string(nil), cfg.CustomerGroups...)
	log := zap.NewNop()
	if len(logs) != 0 && logs[0] != nil {
		log = logs[0]
	}
	provider := &Provider{
		config: cfg, requests: make(chan struct{}, cfg.MaxConcurrent),
		client: &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
		cache:   newProfileCache(cfg.CacheEntries, cfg.CacheTTL),
		metrics: newMetrics(otel.Meter("gitstore/userdir")),
		log:     log,
	}
	provider.webhookHandler = provider.newWebhookHandler()
	return provider, nil
}

func (*Provider) Name() string { return "kratos" }

func (p *Provider) ProfileLeaseDuration() time.Duration { return p.config.CacheTTL }

func (p *Provider) SetInvalidationHandler(handler func()) {
	p.invalidationMu.Lock()
	defer p.invalidationMu.Unlock()
	p.invalidateConnections = handler
}

func (p *Provider) invalidate() {
	p.cache.clear()
	p.invalidationMu.RLock()
	handler := p.invalidateConnections
	p.invalidationMu.RUnlock()
	if handler != nil {
		handler()
	}
}

func validIdentifier(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}

func (p *Provider) lookup(ctx context.Context, cacheKey, path string, query url.Values, username string) (profile *auth.UserProfile, lookupErr error) {
	start := time.Now()
	cacheOutcome := "miss"
	defer func() {
		p.metrics.resolution.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(
			attribute.String("cache", cacheOutcome), attribute.String("outcome", auth.DirectoryErrorClass(lookupErr))))
		p.cache.mu.Lock()
		cacheSize := len(p.cache.entries)
		p.cache.mu.Unlock()
		p.metrics.cacheEntries.Record(ctx, int64(cacheSize))
	}()
	generation := p.cache.snapshotGeneration()
	if profile := p.cache.get(cacheKey); profile != nil {
		cacheOutcome = "hit"
		p.metrics.cache.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "hit")))
		return profile, nil
	}
	p.metrics.cache.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "miss")))
	freshUntil := time.Now().Add(p.config.CacheTTL)
	var record identity
	if username == "" {
		if err := p.get(ctx, path, query, &record); err != nil {
			return nil, err
		}
	} else {
		var identities []identity
		if err := p.get(ctx, path, query, &identities); err != nil {
			return nil, err
		}
		if len(identities) == 0 {
			return nil, auth.ErrProfileNotFound
		}
		if len(identities) != 1 {
			return nil, auth.ErrDirectoryConflict
		}
		record = identities[0]
		if record.Traits.Username != username {
			return nil, auth.ErrProfileNotFound
		}
	}
	profile, err := p.profile(record)
	if err != nil {
		return nil, err
	}
	profile.FreshUntil = freshUntil
	p.cache.putGeneration(cacheKey, profile, &generation)
	return profile, nil
}

func (p *Provider) GetBySubject(ctx context.Context, subject string) (*auth.UserProfile, error) {
	if !validIdentifier(subject) {
		return nil, ErrInvalidInput
	}
	return p.lookup(ctx, "uid\x00"+subject, "/admin/identities/"+url.PathEscape(subject), nil, "")
}

func (p *Provider) ListGroups(ctx context.Context, subject string) ([]string, error) {
	profile, err := p.GetBySubject(ctx, subject)
	if err != nil {
		return nil, err
	}
	return profile.Groups, nil
}

func (p *Provider) ListServiceAccountGroups(ctx context.Context, uid string) ([]string, error) {
	if !validIdentifier(uid) {
		return nil, ErrInvalidInput
	}
	profile, err := p.lookup(ctx, "serviceaccount-groups\x00"+uid,
		"/admin/identities/by/external/"+url.PathEscape("gitstore:serviceaccount:"+uid), nil, "")
	if err != nil {
		return nil, err
	}
	if profile.Kind != auth.ProfileServiceAccount || profile.ServiceAccountUID != uid {
		return nil, auth.ErrProfileNotFound
	}
	return append([]string{}, profile.Groups...), nil
}

func (p *Provider) SearchUsers(ctx context.Context, query string, limit int) ([]*auth.UserProfile, error) {
	if !auth.ValidExternalUsername(query) || limit < 1 || limit > 500 {
		return nil, ErrInvalidInput
	}
	profile, err := p.lookup(ctx, "username\x00"+query, "/admin/identities",
		url.Values{"credentials_identifier": {query}, "page_size": {"2"}}, query)
	if err != nil {
		return nil, err
	}
	return []*auth.UserProfile{profile}, nil
}
