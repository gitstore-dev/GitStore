package bridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/hydraclient"
	"github.com/gitstore-dev/gitstore/oidc-bridge/internal/kratosclient"
)

var testPermittedScope = []string{"openid", "profile", "email", "offline_access"}

func newConsentRouter(hydra *fakeHydra, kratos *fakeKratos) *gin.Engine {
	return newConsentRouterWithAudience(hydra, kratos, "")
}

func newConsentRouterWithAudience(hydra *fakeHydra, kratos *fakeKratos, defaultAudience string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewConsentHandler(hydra, kratos, testPermittedScope, defaultAudience, zap.NewNop())
	r.GET("/consent", h.Handle)
	return r
}

func TestConsentMissingChallenge(t *testing.T) {
	r := newConsentRouter(&fakeHydra{}, &fakeKratos{})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestConsentFullScopeGrantWithClaims(t *testing.T) {
	hydra := &fakeHydra{
		consentReq: &hydraclient.ConsentRequest{
			Challenge:         "ch",
			Subject:           "id-1",
			RequestedScope:    []string{"openid", "email"},
			RequestedAudience: []string{"gitstore"},
		},
		acceptConsentTo: "http://hydra:4444/oauth2/auth?consent_verifier=xyz",
	}
	kratos := &fakeKratos{identity: &kratosclient.Identity{ID: "id-1", Email: "u@example.com", Username: "exampleuser"}}
	r := newConsentRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if !hydra.acceptConsentCalled {
		t.Fatal("AcceptConsentRequest was not called")
	}
	got := hydra.acceptedConsentScope
	if len(got) != 2 || got[0] != "openid" || got[1] != "email" {
		t.Errorf("grant_scope = %v, want [openid email]", got)
	}
	if hydra.acceptedConsentClaims["email"] != "u@example.com" {
		t.Errorf("id_token email claim = %v", hydra.acceptedConsentClaims["email"])
	}
	if hydra.acceptedConsentClaims["preferred_username"] != "exampleuser" {
		t.Errorf("id_token preferred_username claim = %v", hydra.acceptedConsentClaims["preferred_username"])
	}
	if loc := w.Header().Get("Location"); loc != "http://hydra:4444/oauth2/auth?consent_verifier=xyz" {
		t.Errorf("Location = %q", loc)
	}
}

func TestConsentPartialGrantWhenRequestedExceedsPermitted(t *testing.T) {
	hydra := &fakeHydra{
		consentReq: &hydraclient.ConsentRequest{
			Challenge:      "ch",
			Subject:        "id-1",
			RequestedScope: []string{"openid", "admin", "email"},
		},
		acceptConsentTo: "http://hydra:4444/continue",
	}
	kratos := &fakeKratos{identity: &kratosclient.Identity{ID: "id-1", Email: "u@example.com", Username: "exampleuser"}}
	r := newConsentRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	got := hydra.acceptedConsentScope
	if len(got) != 2 || got[0] != "openid" || got[1] != "email" {
		t.Errorf("grant_scope = %v, want [openid email] — 'admin' must not be silently granted", got)
	}
}

func TestConsentDefaultAudienceWhenNoneRequested(t *testing.T) {
	hydra := &fakeHydra{
		consentReq: &hydraclient.ConsentRequest{
			Challenge:      "ch",
			Subject:        "id-1",
			RequestedScope: []string{"openid"},
			// RequestedAudience empty — generic clients don't send one.
		},
		acceptConsentTo: "http://hydra:4444/continue",
	}
	kratos := &fakeKratos{identity: &kratosclient.Identity{ID: "id-1", Email: "u@example.com", Username: "exampleuser"}}
	r := newConsentRouterWithAudience(hydra, kratos, "gitstore")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if len(hydra.acceptedConsentAudience) != 1 || hydra.acceptedConsentAudience[0] != "gitstore" {
		t.Errorf("grant_audience = %v, want [gitstore]", hydra.acceptedConsentAudience)
	}
}

func TestConsentRequestedAudiencePreservedOverDefault(t *testing.T) {
	hydra := &fakeHydra{
		consentReq: &hydraclient.ConsentRequest{
			Challenge:         "ch",
			Subject:           "id-1",
			RequestedScope:    []string{"openid"},
			RequestedAudience: []string{"other-api"},
		},
		acceptConsentTo: "http://hydra:4444/continue",
	}
	kratos := &fakeKratos{identity: &kratosclient.Identity{ID: "id-1", Email: "u@example.com", Username: "exampleuser"}}
	r := newConsentRouterWithAudience(hydra, kratos, "gitstore")

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if len(hydra.acceptedConsentAudience) != 1 || hydra.acceptedConsentAudience[0] != "other-api" {
		t.Errorf("grant_audience = %v, want [other-api] (requested wins over default)", hydra.acceptedConsentAudience)
	}
}

func TestConsentIdentityLookupFailureRejects(t *testing.T) {
	hydra := &fakeHydra{
		consentReq: &hydraclient.ConsentRequest{Challenge: "ch", Subject: "id-1", RequestedScope: []string{"openid"}},
		rejectTo:   "http://client.example/callback?error=server_error",
	}
	kratos := &fakeKratos{identityErr: errors.New("kratos down")}
	r := newConsentRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectConsentCalled {
		t.Fatal("RejectConsentRequest was not called")
	}
	if hydra.acceptConsentCalled {
		t.Error("AcceptConsentRequest must not be called when identity lookup fails")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 (no raw 500)", w.Code)
	}
}

func TestConsentRequestLookupFailureRejects(t *testing.T) {
	hydra := &fakeHydra{
		consentReqErr: errors.New("hydra down"),
		rejectTo:      "http://client.example/callback?error=server_error",
	}
	r := newConsentRouter(hydra, &fakeKratos{})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectConsentCalled {
		t.Fatal("RejectConsentRequest was not called")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
}

func TestConsentAcceptFailureRejects(t *testing.T) {
	hydra := &fakeHydra{
		consentReq:       &hydraclient.ConsentRequest{Challenge: "ch", Subject: "id-1", RequestedScope: []string{"openid"}},
		acceptConsentErr: errors.New("hydra accept failed"),
		rejectTo:         "http://client.example/callback?error=server_error",
	}
	kratos := &fakeKratos{identity: &kratosclient.Identity{ID: "id-1"}}
	r := newConsentRouter(hydra, kratos)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/consent?consent_challenge=ch", nil)
	r.ServeHTTP(w, req)

	if !hydra.rejectConsentCalled {
		t.Fatal("RejectConsentRequest was not called")
	}
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
}
