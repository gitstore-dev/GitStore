// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestAdmissionRejectsLargeChunkedBodyBeforeExecution(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	limits.BodyBytes = 32
	gate := newEndpointAdmission("admin", limits)
	r := gin.New()
	r.POST("/admin/graphql", gate.middleware(), func(c *gin.Context) { t.Error("oversized body executed"); c.Status(200) })
	request := httptest.NewRequest(http.MethodPost, "/admin/graphql", strings.NewReader(strings.Repeat("x", 33)))
	request.ContentLength = -1
	w := httptest.NewRecorder()
	r.ServeHTTP(w, request)
	require.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestAdmissionDeadlineReleasesSlotAndPublicNeverReadsBody(t *testing.T) {
	l := config.DefaultEndpoint("admin").Limits
	l.Deadline = time.Millisecond
	gate := newEndpointAdmission("admin", l)
	r := gin.New()
	r.POST("/", gate.middleware(), func(c *gin.Context) { <-c.Request.Context().Done(); c.Status(408) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/", nil))
	require.Equal(t, 408, w.Code)
	require.Empty(t, gate.httpSlots)
	p, err := NewStorefrontHandler(config.DefaultEndpoint("storefront"), config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	request := httptest.NewRequest("POST", "/storefront/graphql", nil)
	request.ContentLength = -1
	request.Body = panicReadBody{}
	w = httptest.NewRecorder()
	p.ServeHTTP(w, request)
	require.Equal(t, 503, w.Code)
}

type panicReadBody struct{}

func (panicReadBody) Read([]byte) (int, error) { panic("public endpoint consumed body") }
func (panicReadBody) Close() error             { return nil }

type heldResponse struct {
	*httptest.ResponseRecorder
	started, release chan struct{}
}

func (w heldResponse) Write(body []byte) (int, error) {
	close(w.started)
	<-w.release
	return w.ResponseRecorder.Write(body)
}

func TestBlockedPublicResponseDoesNotConsumeAdminCapacity(t *testing.T) {
	profile := config.DefaultEndpoint("storefront")
	profile.Limits.InFlight = 1
	public, err := NewStorefrontHandler(profile, config.RateLimitConfig{}, nil)
	require.NoError(t, err)
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		public.ServeHTTP(heldResponse{httptest.NewRecorder(), started, release}, httptest.NewRequest("POST", "/storefront/graphql", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	public.ServeHTTP(w, httptest.NewRequest("POST", "/storefront/graphql", nil))
	require.Equal(t, 429, w.Code)
	env := newWebSocketTestEnvironment(t, time.Now().Add(time.Minute))
	r, err := http.NewRequest("POST", env.server.URL+"/admin/graphql", strings.NewReader(`{"query":"{__typename}"}`))
	require.NoError(t, err)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer valid")
	response, err := env.server.Client().Do(r)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	_ = response.Body.Close()
	close(release)
	<-done
}

func TestAdmissionHasNoWaitingQueueAndReleasesCapacity(t *testing.T) {
	limits := config.DefaultEndpoint("admin").Limits
	limits.InFlight = 1
	gate := newEndpointAdmission("admin", limits)
	started, release := make(chan struct{}), make(chan struct{})
	r := gin.New()
	r.POST("/admin/graphql", gate.middleware(), func(c *gin.Context) { close(started); <-release; c.Status(200) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/admin/graphql", nil))
	}()
	<-started
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/graphql", nil))
	require.Equal(t, 429, w.Code)
	close(release)
	<-done
	require.Empty(t, gate.httpSlots)
}
