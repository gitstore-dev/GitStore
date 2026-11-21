// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package bridge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

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

func TestOwnedNativeDualIdentifierLoginPreservesOIDCSubject(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("requires the explicitly owned Kratos integration fixture")
	}
	base, err := url.Parse(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	if err != nil {
		t.Fatal("invalid owned identity endpoint")
	}
	admin := base.String()
	base.Host = net.JoinHostPort(base.Hostname(), "4433")
	username, email := "registered", "registered@example.test"
	expected := os.Getenv("GITSTORE_TEST_LOGIN_IDENTITY_ID")
	password := os.Getenv("GITSTORE_TEST_LOGIN_PASSWORD")
	if expected == "" || password == "" {
		t.Fatal("owned identity material is required")
	}
	for _, identifier := range []string{username, email} {
		client := ownedBrowserLogin(t, base, identifier, password)
		hydra := &fakeHydra{loginReq: &hydraclient.LoginRequest{Challenge: "owned"}, acceptLoginTo: "http://hydra.example.test/accepted"}
		handler := NewLoginHandler(hydra, kratosclient.New(base.String(), admin), base.String(), zap.NewNop())
		router := gin.New()
		router.GET("/login", handler.Handle)
		req := httptest.NewRequest(http.MethodGet, "/login?login_challenge=owned", nil)
		for _, cookie := range client.Jar.Cookies(base) {
			req.AddCookie(cookie)
		}
		result := httptest.NewRecorder()
		router.ServeHTTP(result, req)
		if result.Code != http.StatusFound || !hydra.acceptLoginCalled || hydra.acceptedLoginSubject != expected {
			t.Fatal("both native password identifiers must yield the same immutable OIDC subject")
		}
	}
}

func ownedBrowserLogin(t *testing.T, base *url.URL, identifier, password string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, payload any) *http.Response {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal("encode owned login request")
		}
		req, err := http.NewRequestWithContext(t.Context(), method, base.String()+path, strings.NewReader(string(body)))
		if err != nil {
			t.Fatal("create owned login request")
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("owned login request failed")
		}
		return response
	}
	response := request(http.MethodGet, "/self-service/login/browser", nil)
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("browser flow status=%d", response.StatusCode)
	}
	var flow struct {
		ID string `json:"id"`
		UI struct {
			Nodes []struct {
				Attributes struct {
					Name  string `json:"name"`
					Value any    `json:"value"`
				} `json:"attributes"`
			} `json:"nodes"`
		} `json:"ui"`
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&flow)
	response.Body.Close()
	if err != nil || flow.ID == "" {
		t.Fatal("invalid owned browser flow")
	}
	csrf := ""
	for _, node := range flow.UI.Nodes {
		if node.Attributes.Name == "csrf_token" {
			csrf, _ = node.Attributes.Value.(string)
		}
	}
	if csrf == "" {
		t.Fatal("browser login requires native CSRF")
	}
	response = request(http.MethodPost, "/self-service/login?flow="+url.QueryEscape(flow.ID), map[string]any{
		"method": "password", "identifier": identifier, "password": password, "csrf_token": csrf,
	})
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("native browser login status=%d", response.StatusCode)
	}
	return client
}

func TestOwnedNativeHydraLoginPreservesDirectorySubject(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("requires the explicitly owned native Hydra/Kratos integration fixture")
	}
	admin := os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI")
	base, err := url.Parse(admin)
	if err != nil {
		t.Fatal("invalid owned identity endpoint")
	}
	base.Host = net.JoinHostPort(base.Hostname(), "4433")
	expected := os.Getenv("GITSTORE_TEST_LOGIN_IDENTITY_ID")
	password := os.Getenv("GITSTORE_TEST_LOGIN_PASSWORD")
	output := os.Getenv("GITSTORE_TEST_LOGIN_TOKEN_FILE")
	username, email := os.Getenv("GITSTORE_TEST_LOGIN_USERNAME"), os.Getenv("GITSTORE_TEST_LOGIN_EMAIL")
	if expected == "" || password == "" || output == "" || username == "" || email == "" {
		t.Fatal("owned login material is required")
	}
	hydra := hydraclient.New("http://hydra:4445")
	kratos := kratosclient.New(base.String(), admin)
	router := gin.New()
	router.GET("/login", NewLoginHandler(hydra, kratos, base.String(), zap.NewNop()).Handle)
	router.GET("/consent", NewConsentHandler(hydra, kratos, []string{"openid", "profile", "email"},
		"owned-directory", zap.NewNop()).Handle)
	listener, err := net.Listen("tcp", ":8087")
	if err != nil {
		t.Fatal("listen on owned bridge endpoint")
	}
	server := &http.Server{Handler: router, ReadHeaderTimeout: time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			t.Error("close owned bridge endpoint")
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error("owned bridge server did not stop cleanly")
		}
	})
	callback := "http://" + net.JoinHostPort(base.Hostname(), "8087") + "/callback"
	tokens := make([]string, 0, 2)
	for _, identifier := range []string{username, email} {
		client := ownedBrowserLogin(t, base, identifier, password)
		state, verifier := rand.Text(), rand.Text()+rand.Text()
		challenge := sha256.Sum256([]byte(verifier))
		parameters := url.Values{
			"client_id": {"directory-test-client"}, "response_type": {"code"},
			"scope": {"openid profile email"}, "redirect_uri": {callback}, "state": {state},
			"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
			"code_challenge_method": {"S256"},
		}
		next, code := "http://hydra:4444/oauth2/auth?"+parameters.Encode(), ""
		for range 12 {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, next, nil)
			if err != nil {
				t.Fatal("create owned authorization request")
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal("native authorization request failed")
			}
			response.Body.Close()
			location, err := response.Location()
			if err != nil || response.StatusCode < 300 || response.StatusCode >= 400 {
				t.Fatalf("native authorization redirect status=%d", response.StatusCode)
			}
			if location.Scheme+"://"+location.Host+location.Path == callback {
				if location.Query().Get("state") != state || location.Query().Get("error") != "" {
					t.Fatal("native authorization callback failed state/error validation")
				}
				code = location.Query().Get("code")
				break
			}
			if location.Host != "hydra:4444" && location.Host != net.JoinHostPort(base.Hostname(), "8087") {
				t.Fatal("native authorization left the owned Hydra/bridge endpoints")
			}
			next = location.String()
		}
		if code == "" {
			t.Fatal("native bridge did not complete the authorization-code flow")
		}
		form := url.Values{
			"grant_type": {"authorization_code"}, "code": {code},
			"redirect_uri": {callback}, "code_verifier": {verifier},
		}
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://hydra:4444/oauth2/token",
			strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal("create native token exchange")
		}
		request.SetBasicAuth("directory-test-client", password)
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err := client.Do(request)
		if err != nil {
			t.Fatal("native token exchange failed")
		}
		var issued struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&issued)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || err != nil || issued.AccessToken == "" || issued.IDToken == "" {
			t.Fatalf("native token exchange status=%d", response.StatusCode)
		}
		request, err = http.NewRequestWithContext(t.Context(), http.MethodPost, "http://hydra:4445/admin/oauth2/introspect",
			strings.NewReader(url.Values{"token": {issued.AccessToken}}.Encode()))
		if err != nil {
			t.Fatal("create native token introspection")
		}
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err = client.Do(request)
		if err != nil {
			t.Fatal("native token introspection failed")
		}
		var claims struct {
			Active  bool   `json:"active"`
			Subject string `json:"sub"`
			Scope   string `json:"scope"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&claims)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || err != nil || !claims.Active || claims.Subject != expected ||
			claims.Scope != "openid profile email" {
			t.Fatal("native issued credentials must preserve the identity UID and granted scopes for both identifiers")
		}
		tokens = append(tokens, issued.AccessToken)
	}
	encoded, err := json.Marshal(tokens)
	if err != nil || os.WriteFile(output, encoded, 0600) != nil {
		t.Fatal("save private native token material")
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
