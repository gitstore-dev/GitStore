// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/hydraclient"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/kratosclient"
)

type fakeHydra struct {
	loginReq         *hydraclient.LoginRequest
	loginReqErr      error
	consentReq       *hydraclient.ConsentRequest
	consentReqErr    error
	acceptLoginTo    string
	acceptLoginErr   error
	acceptConsentTo  string
	acceptConsentErr error
	rejectTo         string
	rejectErr        error

	acceptedLoginSubject    string
	acceptLoginCalled       bool
	rejectLoginCalled       bool
	acceptedConsentScope    []string
	acceptedConsentAudience []string
	acceptedConsentClaims   map[string]interface{}
	acceptConsentCalled     bool
	rejectConsentCalled     bool
}

func (f *fakeHydra) GetLoginRequest(_ context.Context, challenge string) (*hydraclient.LoginRequest, error) {
	if f.loginReqErr != nil {
		return nil, f.loginReqErr
	}
	return f.loginReq, nil
}

func (f *fakeHydra) AcceptLoginRequest(_ context.Context, challenge, subject string) (string, error) {
	f.acceptLoginCalled = true
	f.acceptedLoginSubject = subject
	if f.acceptLoginErr != nil {
		return "", f.acceptLoginErr
	}
	return f.acceptLoginTo, nil
}

func (f *fakeHydra) RejectLoginRequest(_ context.Context, challenge, code, desc string) (string, error) {
	f.rejectLoginCalled = true
	if f.rejectErr != nil {
		return "", f.rejectErr
	}
	return f.rejectTo, nil
}

func (f *fakeHydra) GetConsentRequest(_ context.Context, challenge string) (*hydraclient.ConsentRequest, error) {
	if f.consentReqErr != nil {
		return nil, f.consentReqErr
	}
	return f.consentReq, nil
}

func (f *fakeHydra) AcceptConsentRequest(_ context.Context, challenge string, grantScope, grantAudience []string, idTokenClaims map[string]interface{}) (string, error) {
	f.acceptConsentCalled = true
	f.acceptedConsentScope = grantScope
	f.acceptedConsentAudience = grantAudience
	f.acceptedConsentClaims = idTokenClaims
	if f.acceptConsentErr != nil {
		return "", f.acceptConsentErr
	}
	return f.acceptConsentTo, nil
}

func (f *fakeHydra) RejectConsentRequest(_ context.Context, challenge, code, desc string) (string, error) {
	f.rejectConsentCalled = true
	if f.rejectErr != nil {
		return "", f.rejectErr
	}
	return f.rejectTo, nil
}

type fakeKratos struct {
	whoamiIdentity *kratosclient.Identity
	whoamiErr      error
	identity       *kratosclient.Identity
	identityErr    error
}

func (f *fakeKratos) WhoAmI(_ context.Context, cookieHeader string) (*kratosclient.Identity, error) {
	if f.whoamiErr != nil {
		return nil, f.whoamiErr
	}
	return f.whoamiIdentity, nil
}

func (f *fakeKratos) GetIdentity(_ context.Context, id string) (*kratosclient.Identity, error) {
	if f.identityErr != nil {
		return nil, f.identityErr
	}
	return f.identity, nil
}

func newLoginRouter(hydra *fakeHydra, kratos *fakeKratos) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewLoginHandler(hydra, kratos, "http://localhost:4433", zap.NewNop())
	r.GET("/login", h.Handle)
	return r
}

func TestLoginMissingChallenge(t *testing.T) {
	r := newLoginRouter(&fakeHydra{}, &fakeKratos{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestLoginValidSessionAcceptsWithIdentityID(t *testing.T) {
	hydra := &fakeHydra{
		loginReq:      &hydraclient.LoginRequest{Challenge: "ch"},
		acceptLoginTo: "http://hydra:4444/oauth2/auth?login_verifier=xyz",
	}
	kratos := &fakeKratos{whoamiIdentity: &kratosclient.Identity{ID: "018f2e3a-0000-7000-8000-000000000001", Email: "u@example.com"}}
	r := newLoginRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=ch", nil)
	req.Header.Set("Cookie", "ory_kratos_session=abc")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if !hydra.acceptLoginCalled {
		t.Fatal("AcceptLoginRequest was not called")
	}
	if hydra.acceptedLoginSubject != "018f2e3a-0000-7000-8000-000000000001" {
		t.Errorf("subject = %q, want the Kratos identity id, not the email", hydra.acceptedLoginSubject)
	}
	if loc := w.Header().Get("Location"); loc != "http://hydra:4444/oauth2/auth?login_verifier=xyz" {
		t.Errorf("Location = %q", loc)
	}
	if hydra.rejectLoginCalled {
		t.Error("RejectLoginRequest must not be called on the happy path")
	}
}

func TestLoginNoSessionRedirectsToKratosLogin(t *testing.T) {
	hydra := &fakeHydra{loginReq: &hydraclient.LoginRequest{Challenge: "ch"}}
	kratos := &fakeKratos{whoamiErr: kratosclient.ErrNoSession}
	r := newLoginRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=ch", nil)
	req.Host = "localhost:4445"
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "http://localhost:4433/self-service/login/browser?return_to=") {
		t.Fatalf("Location = %q, want Kratos self-service login", loc)
	}
	returnTo, _ := url.Parse(loc)
	rt := returnTo.Query().Get("return_to")
	if !strings.Contains(rt, "/login") || !strings.Contains(rt, "login_challenge=ch") {
		t.Errorf("return_to = %q, want the original /login URL with its challenge preserved", rt)
	}
	if hydra.acceptLoginCalled {
		t.Error("AcceptLoginRequest must not be called without a session")
	}
	if hydra.rejectLoginCalled {
		t.Error("RejectLoginRequest must not be called for a routine no-session redirect")
	}
}

func TestLoginHydraLookupFailureRejectsChallenge(t *testing.T) {
	hydra := &fakeHydra{
		loginReqErr: errors.New("hydra down"),
		rejectTo:    "http://client.example/callback?error=server_error",
	}
	kratos := &fakeKratos{}
	r := newLoginRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectLoginCalled {
		t.Fatal("RejectLoginRequest was not called")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (no raw 500 once the challenge is being processed)", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://client.example/callback?error=server_error" {
		t.Errorf("Location = %q", loc)
	}
}

func TestLoginKratosFailureRejectsChallenge(t *testing.T) {
	hydra := &fakeHydra{
		loginReq: &hydraclient.LoginRequest{Challenge: "ch"},
		rejectTo: "http://client.example/callback?error=server_error",
	}
	kratos := &fakeKratos{whoamiErr: errors.New("kratos 500")}
	r := newLoginRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectLoginCalled {
		t.Fatal("RejectLoginRequest was not called")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
}

func TestLoginAcceptFailureRejectsChallenge(t *testing.T) {
	hydra := &fakeHydra{
		loginReq:       &hydraclient.LoginRequest{Challenge: "ch"},
		acceptLoginErr: errors.New("hydra accept failed"),
		rejectTo:       "http://client.example/callback?error=server_error",
	}
	kratos := &fakeKratos{whoamiIdentity: &kratosclient.Identity{ID: "id-1"}}
	r := newLoginRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectLoginCalled {
		t.Fatal("RejectLoginRequest was not called")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
}
