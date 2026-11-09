// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"container/list"
	"maps"
	"sync"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
)

type cacheEntry struct {
	key     string
	profile *auth.UserProfile
	expires time.Time
}

type profileCache struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List
	maximum    int
	ttl        time.Duration
	generation uint64
}

func newProfileCache(maximum int, ttl time.Duration) profileCache {
	return profileCache{entries: make(map[string]*list.Element), order: list.New(), maximum: maximum, ttl: ttl}
}

func cloneProfile(profile *auth.UserProfile) *auth.UserProfile {
	copy := *profile
	copy.Groups = append([]string(nil), profile.Groups...)
	copy.Identities = append([]string(nil), profile.Identities...)
	if profile.Metadata != nil {
		metadata := *profile.Metadata
		metadata.Labels = maps.Clone(metadata.Labels)
		metadata.Annotations = maps.Clone(metadata.Annotations)
		copy.Metadata = &metadata
	}
	return &copy
}

func (c *profileCache) get(key string) *auth.UserProfile {
	c.mu.Lock()
	defer c.mu.Unlock()
	element := c.entries[key]
	if element == nil {
		return nil
	}
	entry := element.Value.(cacheEntry)
	if !time.Now().Before(entry.expires) {
		delete(c.entries, key)
		c.order.Remove(element)
		return nil
	}
	c.order.MoveToFront(element)
	return cloneProfile(entry.profile)
}

func (c *profileCache) put(key string, profile *auth.UserProfile) {
	c.putGeneration(key, profile, nil)
}

func (c *profileCache) snapshotGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func (c *profileCache) putGeneration(key string, profile *auth.UserProfile, generation *uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != nil && *generation != c.generation {
		return
	}
	expires := time.Now().Add(c.ttl)
	if !profile.FreshUntil.IsZero() && profile.FreshUntil.Before(expires) {
		expires = profile.FreshUntil
	}
	cached := cloneProfile(profile)
	cached.FreshUntil = expires
	entry := cacheEntry{key: key, profile: cached, expires: expires}
	if element := c.entries[key]; element != nil {
		element.Value = entry
		c.order.MoveToFront(element)
		return
	}

	if len(c.entries) == c.maximum {
		oldest := c.order.Back()
		delete(c.entries, oldest.Value.(cacheEntry).key)
		c.order.Remove(oldest)
	}
	c.entries[key] = c.order.PushFront(entry)
}

func (c *profileCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	c.entries = make(map[string]*list.Element)
	c.order.Init()
}
