// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/oidcjwt"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/rbaclocal"
	"github.com/gitstore-dev/gitstore/api/internal/auth/provider/staticusers"
	"github.com/gitstore-dev/gitstore/api/internal/config"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/memdb"
	"github.com/gitstore-dev/gitstore/api/internal/datastore/scylla"
	"github.com/gitstore-dev/gitstore/api/internal/notification"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

func TestOwnedUserDirectoryIssuer(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("only the owned directory fixture runs this issuer")
	}
	raw, err := os.ReadFile(os.Getenv("GITSTORE_TEST_OWNED_OIDC_KEY_FILE"))
	require.NoError(t, err)
	block, _ := pem.Decode(raw)
	require.NotNil(t, block)
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	require.NoError(t, err)
	const issuer = "http://directory-receiver:8086"
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"issuer": issuer, "jwks_uri": issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}))
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "use": "sig", "kid": "owned-directory", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}}))
	})
	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	server := &http.Server{Addr: ":8086", Handler: mux, ReadHeaderTimeout: 2 * time.Second}
	require.NoError(t, server.ListenAndServe())
}

func TestOwnedAPIStaffSetup(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("only the owned API directory fixture runs this test")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	provider, err := New(cfg)
	require.NoError(t, err)
	current, _, err := provider.fetchIdentity(t.Context(), os.Getenv("GITSTORE_TEST_OWNED_STAFF_UID"))
	require.NoError(t, err)
	profile, err := provider.profile(current)
	require.NoError(t, err)
	require.Equal(t, auth.ActorInvited, profile.Lifecycle)
	var invitation struct {
		RecoveryLink string `json:"recovery_link"`
	}
	require.NoError(t, provider.request(t.Context(), http.MethodPost, "/admin/recovery/link", nil,
		map[string]any{"identity_id": profile.Subject, "expires_in": "1h"}, &invitation))
	verifyOwnedNativeStaffSetup(t, cfg, profile, invitation.RecoveryLink)
}

func TestOwnedWorkloadSourceAuthority(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" || os.Getenv("GITSTORE_TEST_OWNED_SCYLLA") != "1" {
		t.Skip("only the owned shared-Scylla directory fixture runs this test")
	}
	store, err := scylla.New(config.ScyllaConfig{Hosts: []string{"scylla:9042"}, Keyspace: "gitstore"}, zap.NewNop())
	require.NoError(t, err)
	defer store.Close()
	uid := os.Getenv("GITSTORE_TEST_OWNED_SOURCE_UID")
	account, err := store.GetServiceAccountByUID(t.Context(), uid)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(account.Name, "owned-"), "only a fixture-owned workload may be changed")
	require.NoError(t, store.SetServiceAccountDisabled(t.Context(), uid, os.Getenv("GITSTORE_TEST_OWNED_SOURCE_DISABLED") == "1"))
}

func TestOwnedKratosServiceAccountMirrors(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("only the explicitly owned Kratos integration fixture runs this test")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	cfg.Timeout = 2 * time.Second
	first, err := New(cfg)
	require.NoError(t, err)
	cfg.AdminURI = os.Getenv("GITSTORE_TEST_KRATOS_SECOND_ADMIN_URI")
	second, err := New(cfg)
	require.NoError(t, err)
	store, err := memdb.New()
	require.NoError(t, err)
	account := &datastore.ServiceAccount{UID: uuid.NewString(), Name: "owned-ci-bot", ResourceVersion: "1",
		Generation: 1, CreationTimestamp: time.Now().UTC(), CreationActor: "operator"}
	require.NoError(t, store.CreateServiceAccount(t.Context(), account))
	payload, err := json.Marshal(account)
	require.NoError(t, err)
	event := datastore.ResourceWatchEvent{Kind: "ServiceAccount", Type: datastore.ResourceWatchAdded, Name: account.Name, Payload: payload}
	require.NoError(t, first.ApplyServiceAccountEvent(t.Context(), store, event))
	require.NoError(t, second.ApplyServiceAccountEvent(t.Context(), store, event), "duplicate delivery through another native replica is idempotent")
	external := "/admin/identities/by/external/" + url.PathEscape("gitstore:serviceaccount:"+account.UID)
	var mirror struct {
		ID                  string         `json:"id"`
		State               string         `json:"state"`
		Credentials         map[string]any `json:"credentials"`
		VerifiableAddresses []any          `json:"verifiable_addresses"`
		RecoveryAddresses   []any          `json:"recovery_addresses"`
	}
	require.NoError(t, second.get(t.Context(), external, url.Values{"include_credential": {"password", "oidc"}}, &mirror))
	require.NotEqual(t, account.UID, mirror.ID)
	require.Equal(t, "active", mirror.State)
	require.Empty(t, mirror.Credentials)
	require.Empty(t, mirror.VerifiableAddresses)
	require.Empty(t, mirror.RecoveryAddresses)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	_, err = second.SetActorGroup(ctx, auth.ProfileServiceAccount, account.UID, "ci", true)
	require.NoError(t, err)
	require.NoError(t, store.SetServiceAccountDisabled(t.Context(), account.UID, true))
	require.NoError(t, first.ApplyServiceAccountEvent(t.Context(), store, event), "stale active payload must project the current disabled source")
	require.NoError(t, second.get(t.Context(), external, nil, &mirror))
	require.Equal(t, "inactive", mirror.State)
	groups, err := second.ListServiceAccountGroups(t.Context(), account.UID)
	require.NoError(t, err)
	require.Equal(t, []string{"system:group:ci", "system:serviceaccounts"}, groups)
	require.NoError(t, store.DeleteServiceAccount(t.Context(), account.UID))
	replacement := *account
	replacement.UID = uuid.NewString()
	require.NoError(t, store.CreateServiceAccount(t.Context(), &replacement))
	event.Type = datastore.ResourceWatchDeleted
	event.Payload = []byte(`{"UID":"` + account.UID + `"}`)
	require.NoError(t, second.ApplyServiceAccountEvent(t.Context(), store, event))
	var deleted identity
	require.ErrorIs(t, first.get(t.Context(), external, nil, &deleted), auth.ErrProfileNotFound)
	payload, err = json.Marshal(&replacement)
	require.NoError(t, err)
	event.Type, event.Payload = datastore.ResourceWatchAdded, payload
	require.NoError(t, first.ApplyServiceAccountEvent(t.Context(), store, event))
	groups, err = second.ListServiceAccountGroups(t.Context(), replacement.UID)
	require.NoError(t, err)
	require.Equal(t, []string{"system:serviceaccounts"}, groups, "name reuse must not transfer old directory memberships")
	require.NoError(t, store.DeleteServiceAccount(t.Context(), replacement.UID))
	event.Type = datastore.ResourceWatchDeleted
	event.Payload = []byte(`{"UID":"` + replacement.UID + `"}`)
	require.NoError(t, first.ApplyServiceAccountEvent(t.Context(), store, event))
}

func TestOwnedKratosWebhookReceiver(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("only the owned Kratos integration fixture runs this receiver")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	cfg.Timeout, cfg.MaxConcurrent = 2*time.Second, 16
	provider, err := New(cfg)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if key := os.Getenv("GITSTORE_TEST_OWNED_MAIL_KEY"); key != "" {
		sender, err := notification.NewDevelopment("smtps://mailslurper:1025/?skip_ssl_verify=true", "noreply@example.test")
		require.NoError(t, err)
		handler, err := notification.NewMailHandler(sender, key, 65536, 16, 2*time.Second)
		require.NoError(t, err)
		mux.Handle("/internal/notifications/mail", handler)
	}
	mux.Handle("/", provider.WebhookHandler())
	require.NoError(t, http.ListenAndServe(":8085", mux))
}

func TestOwnedIdentityMailEndpoint(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" || os.Getenv("GITSTORE_TEST_OWNED_MAIL_KEY") == "" {
		t.Skip("only the explicitly owned HTTP courier fixture runs this test")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	key := os.Getenv("GITSTORE_TEST_OWNED_MAIL_KEY")
	outage := os.Getenv("GITSTORE_TEST_MAIL_OUTAGE") == "1"
	body := `{"recipient":"handler@example.test","subject":"Owned invitation","body":"private setup link","template_type":"invitation"}`
	scenarios := []struct {
		body, key string
		status    int
	}{
		{body, "", http.StatusUnauthorized},
		{"{", key, http.StatusBadRequest},
		{strings.Repeat("x", 65537), key, http.StatusRequestEntityTooLarge},
		{body, key, http.StatusNoContent},
		{body, key, http.StatusNoContent},
	}
	if outage {
		scenarios = scenarios[3:4]
		scenarios[0].status = http.StatusServiceUnavailable
	}
	for _, scenario := range scenarios {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
			"http://directory-receiver:8085/internal/notifications/mail", strings.NewReader(scenario.body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("GitStore-Mail-Key", scenario.key)
		response, err := client.Do(request)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, scenario.status, response.StatusCode)
	}
}

func TestOwnedStaffInvitationsAndContact(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" || os.Getenv("GITSTORE_TEST_OWNED_MAIL_KEY") == "" {
		t.Skip("only the explicitly owned HTTP courier fixture runs this test")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	provider, err := New(cfg)
	require.NoError(t, err)
	sender, err := notification.NewDevelopment("smtps://mailslurper:1025/?skip_ssl_verify=true", "noreply@example.test")
	require.NoError(t, err)
	deliveries := 0
	var setupLink string
	provider.SetInvitationSender(func(ctx context.Context, profile *auth.UserProfile, link string) error {
		err := sender.Send(ctx, notification.Message{Recipient: profile.Email, Subject: "Owned staff setup",
			Body: "Complete setup:\n" + link, TemplateType: "invitation"})
		if err == nil {
			deliveries++
			setupLink = link
		}
		return err
	})
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	profile := &auth.UserProfile{Kind: auth.ProfileStaff, Username: "invited-staff", Email: "invited-staff@example.test"}
	require.NoError(t, provider.CreateStaff(ctx, profile))
	t.Cleanup(func() { _, _ = provider.DeleteHuman(ctx, profile.Subject) })
	require.Equal(t, auth.ActorInvited, profile.Lifecycle)
	profile, err = provider.ResendInvitation(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, 2, deliveries)
	require.Equal(t, "2", profile.Metadata.ResourceVersion)
	verifyOwnedNativeStaffSetup(t, cfg, profile, setupLink)
	profile, err = provider.humanProfile(ctx, profile.Subject)
	require.NoError(t, err)
	profile.EmailDeliverable = true
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	oldEmail := profile.Email
	profile.Email = "replacement-staff@example.test"
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	contact, err := provider.ResolveContact(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, oldEmail, contact.DeliverableEmail)
	require.Equal(t, profile.Email, contact.PrimaryEmail)
	require.True(t, contact.EmailDeliverable)
	require.False(t, contact.EmailVerified)
	generation := profile.Metadata.Generation
	profile.EmailDeliverable = false
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	require.Equal(t, generation, profile.Metadata.Generation)
	var verified identity
	require.NoError(t, provider.request(ctx, http.MethodPatch, "/admin/identities/"+profile.Subject, nil,
		[]map[string]any{{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
			{"value": profile.Email, "verified": true, "via": "email", "status": "completed"},
		}}}, &verified))
	contact, err = provider.ResolveContact(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, profile.Email, contact.DeliverableEmail)
	require.True(t, contact.EmailDeliverable)
	profile, err = provider.humanProfile(ctx, profile.Subject)
	require.NoError(t, err)
	profile.ContactConsent.Marketing = true
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	require.False(t, profile.ContactConsent.ChangedAt.IsZero())
	require.Equal(t, generation+1, profile.Metadata.Generation)
	deleted, err := provider.DeleteHuman(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, profile.Subject, deleted.Subject)
	_, err = provider.ResolveContact(ctx, profile.Subject)
	require.ErrorIs(t, err, auth.ErrProfileNotFound)
}

func TestOwnedKratosAdminPatch(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("only the owned Kratos integration fixture runs this test")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	cfg.Timeout, cfg.MaxConcurrent = 2*time.Second, 16
	provider, err := New(cfg)
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{
		Subject: "oidc-operator-id", Username: "operator", AuthMethod: "oidc-jwt",
	})
	profile := &auth.UserProfile{
		Username: "hook-admin", Email: "hook-admin@example.test", DisplayName: "Initial",
		Kind: auth.ProfileStaff, Lifecycle: auth.ActorActive,
	}
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	t.Cleanup(func() {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodDelete,
			os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI")+"/admin/identities/"+profile.Subject, nil)
		require.NoError(t, err)
		response, err := provider.client.Do(request)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusNoContent, response.StatusCode)
	})
	require.EqualValues(t, 1, profile.Metadata.Generation)
	require.Equal(t, "1", profile.Metadata.ResourceVersion)
	require.Equal(t, "operator", profile.Metadata.CreationActor)
	profile.DisplayName = "Changed through GitStore"
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	actual, err := provider.GetBySubject(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, profile.DisplayName, actual.DisplayName)
	require.EqualValues(t, 2, actual.Metadata.Generation)
	require.Equal(t, "2", actual.Metadata.ResourceVersion)
	require.Equal(t, "operator", actual.Metadata.CreationActor)
	require.Equal(t, "operator", actual.Metadata.UpdateActor)
	var reserved identity
	require.NoError(t, provider.request(ctx, http.MethodPatch, "/admin/identities/"+profile.Subject, nil,
		[]map[string]any{{"op": "add", "path": "/credentials/password", "value": map[string]any{
			"identifiers": []string{profile.Username, profile.Email}, "config": map[string]any{},
		}}}, &reserved), "invited Staff must be able to reserve native identifiers without a password")
	var byIdentifier []identity
	require.NoError(t, provider.request(ctx, http.MethodGet, "/admin/identities",
		map[string][]string{"credentials_identifier": {profile.Username}, "page_size": {"2"}}, nil, &byIdentifier))
	require.Len(t, byIdentifier, 1, "native exact lookup must resolve credentialless invited Staff")
	require.Equal(t, profile.Subject, byIdentifier[0].ID)
	var credentialProof struct {
		Credentials map[string]struct {
			Config struct {
				Hash     string `json:"hashed_password"`
				Password string `json:"password"`
			} `json:"config"`
		} `json:"credentials"`
	}
	require.NoError(t, provider.request(ctx, http.MethodGet, "/admin/identities/"+profile.Subject,
		map[string][]string{"include_credential": {"password"}}, nil, &credentialProof))
	require.True(t, credentialProof.Credentials["password"].Config.Hash == "" &&
		credentialProof.Credentials["password"].Config.Password == "", "identifier reservation must not store a password or password hash")
	password := make([]byte, 32)
	_, err = rand.Read(password)
	require.NoError(t, err)
	var duplicate identity
	err = provider.request(ctx, http.MethodPost, "/admin/identities", nil, map[string]any{
		"schema_id": "customer", "state": "active",
		"traits": map[string]any{"username": profile.Username, "email": "reserved-collision@example.test"},
		"credentials": map[string]any{"password": map[string]any{"config": map[string]any{
			"password": base64.RawURLEncoding.EncodeToString(password),
		}}},
	}, &duplicate)
	require.ErrorIs(t, err, auth.ErrDirectoryConflict, "native uniqueness must protect invited Staff against Customer identifier reuse")
	var invitation struct {
		RecoveryLink string `json:"recovery_link"`
	}
	require.NoError(t, provider.request(ctx, http.MethodPost, "/admin/recovery/link", nil,
		map[string]any{"identity_id": profile.Subject, "expires_in": "1h"}, &invitation),
		"the selected pin must support credentialless Staff password-setup invitations")
	require.NotEmpty(t, invitation.RecoveryLink)
	first := 2
	page, err := provider.ListActors(ctx, auth.ActorPageRequest{First: &first})
	require.NoError(t, err)
	require.Len(t, page.Edges, 2)
	fromInterior, err := provider.ListActors(ctx, auth.ActorPageRequest{First: &first, After: &page.Edges[0].Cursor})
	require.NoError(t, err)
	require.NotEmpty(t, fromInterior.Edges)
	require.Equal(t, page.Edges[1].Profile.Subject, fromInterior.Edges[0].Profile.Subject,
		"the selected pin must accept every edge's UID as its forward page token")
	require.True(t, fromInterior.HasPreviousPage)
	_, err = provider.ListActors(ctx, auth.ActorPageRequest{Last: &first})
	require.ErrorIs(t, err, auth.ErrNotSupported)
	verifyOwnedNativeStaffSetup(t, cfg, profile, invitation.RecoveryLink)
	verifyOwnedDirectoryPolicy(t, cfg, profile)
}

func verifyOwnedNativeStaffSetup(t *testing.T, cfg config.KratosUserDirConfig, profile *auth.UserProfile, recoveryLink string) {
	t.Helper()
	provider, err := New(cfg)
	require.NoError(t, err)
	var verified identity
	require.NoError(t, provider.request(t.Context(), http.MethodPatch, "/admin/identities/"+profile.Subject, nil,
		[]map[string]any{{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
			{"value": profile.Email, "verified": true, "via": "email", "status": "completed"},
		}}}, &verified))
	before, err := provider.profile(verified)
	require.NoError(t, err)
	require.Equal(t, auth.ActorInvited, before.Lifecycle, "native verification alone must not bypass password setup")
	operator := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	premature, err := provider.SetHumanLifecycle(operator, profile.Subject, auth.ActorActive)
	require.NoError(t, err)
	require.Equal(t, auth.ActorInvited, premature.Lifecycle, "reactivation must not bypass tracked setup")
	base, err := url.Parse(cfg.AdminURI)
	require.NoError(t, err)
	base.Host = net.JoinHostPort(base.Hostname(), "4433")
	base.Path = ""
	link, err := url.Parse(recoveryLink)
	require.True(t, err == nil, "owned recovery link must be a supported URL")
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path string, body any, acceptJSON bool) *http.Response {
		t.Helper()
		data, err := json.Marshal(body)
		require.NoError(t, err)
		req, err := http.NewRequestWithContext(t.Context(), method, base.String()+path, strings.NewReader(string(data)))
		require.True(t, err == nil, "owned native setup request must be valid")
		req.Header.Set("Content-Type", "application/json")
		if acceptJSON {
			req.Header.Set("Accept", "application/json")
		}
		response, err := client.Do(req)
		require.True(t, err == nil, "owned native setup request must succeed")
		return response
	}
	recovered := request(http.MethodGet, link.Path+"?"+link.RawQuery, nil, false)
	recovered.Body.Close()
	require.Equal(t, http.StatusSeeOther, recovered.StatusCode)
	settingsURL, err := url.Parse(recovered.Header.Get("Location"))
	require.NoError(t, err)
	flowID := settingsURL.Query().Get("flow")
	require.True(t, flowID != "", "native recovery must open a settings flow")
	flowResponse := request(http.MethodGet, "/self-service/settings/flows?id="+url.QueryEscape(flowID), nil, true)
	require.Equal(t, http.StatusOK, flowResponse.StatusCode)
	var flow struct {
		UI struct {
			Nodes []struct {
				Attributes struct {
					Name  string `json:"name"`
					Value any    `json:"value"`
				} `json:"attributes"`
			} `json:"nodes"`
		} `json:"ui"`
	}
	require.NoError(t, json.NewDecoder(flowResponse.Body).Decode(&flow))
	flowResponse.Body.Close()
	var csrf string
	for _, node := range flow.UI.Nodes {
		if node.Attributes.Name == "csrf_token" {
			csrf, _ = node.Attributes.Value.(string)
		}
	}
	require.True(t, csrf != "", "native browser password settings must supply a CSRF token")
	password := make([]byte, 32)
	_, err = rand.Read(password)
	require.NoError(t, err)
	changed := request(http.MethodPost, "/self-service/settings?flow="+url.QueryEscape(flowID),
		map[string]any{"method": "password", "password": base64.RawURLEncoding.EncodeToString(password), "csrf_token": csrf}, true)
	changed.Body.Close()
	require.Equal(t, http.StatusOK, changed.StatusCode, "native password settings must run the protected setup callback")
	current, _, err := provider.fetchIdentity(t.Context(), profile.Subject)
	require.NoError(t, err)
	require.NotNil(t, current.MetadataAdmin.SetupComplete)
	require.True(t, *current.MetadataAdmin.SetupComplete, "only the actual password callback must complete tracked setup")
	after, err := provider.profile(current)
	require.NoError(t, err)
	require.Equal(t, auth.ActorActive, after.Lifecycle)
	require.True(t, after.Active)
	validSession := request(http.MethodGet, "/sessions/whoami", nil, true)
	validSession.Body.Close()
	require.Equal(t, http.StatusOK, validSession.StatusCode)
	for _, state := range []auth.ActorLifecycleState{auth.ActorSuspended, auth.ActorInactive} {
		paused, err := provider.SetHumanLifecycle(operator, profile.Subject, state)
		require.NoError(t, err)
		require.Equal(t, state, paused.Lifecycle)
		require.False(t, paused.Active)
	}
	revokedSession := request(http.MethodGet, "/sessions/whoami", nil, true)
	revokedSession.Body.Close()
	require.Equal(t, http.StatusUnauthorized, revokedSession.StatusCode, "native sessions must be revoked after suspension/deactivation")
	reactivated, err := provider.SetHumanLifecycle(operator, profile.Subject, auth.ActorActive)
	require.NoError(t, err)
	require.Equal(t, auth.ActorActive, reactivated.Lifecycle)
	require.True(t, reactivated.Active)
}

func TestOwnedCustomerIdentityAuthorization(t *testing.T) {
	if os.Getenv("GITSTORE_TEST_OWNED_RECEIVER") != "1" {
		t.Skip("requires the explicitly owned Kratos integration fixture")
	}
	cfg := directoryConfig(os.Getenv("GITSTORE_TEST_KRATOS_ADMIN_URI"))
	cfg.CustomerGroups = []string{"shopper"}
	provider, err := New(cfg)
	require.NoError(t, err)
	var created identity
	require.NoError(t, provider.request(t.Context(), http.MethodPost, "/admin/identities", nil,
		map[string]any{
			"schema_id": "customer", "state": "active",
			"traits": map[string]any{"username": "native-customer-policy", "email": "native-customer-policy@example.test"},
			"metadata_admin": map[string]any{
				"groups":          []string{"merchandiser", "system:staff", "system:masters"},
				"lifecycle_state": auth.ActorActive,
			},
		}, &created))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(t, provider.request(ctx, http.MethodDelete, "/admin/identities/"+created.ID, nil, nil, nil))
	})
	profile, err := provider.profile(created)
	require.NoError(t, err)
	verifyOwnedDirectoryPolicy(t, cfg, profile)
}

func verifyOwnedDirectoryPolicy(t *testing.T, cfg config.KratosUserDirConfig, profile *auth.UserProfile) {
	t.Helper()
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	writer, err := New(cfg)
	require.NoError(t, err)
	var updated identity
	require.NoError(t, writer.request(ctx, http.MethodPatch, "/admin/identities/"+profile.Subject, nil,
		[]map[string]any{{"op": "add", "path": "/metadata_admin/groups", "value": []string{"merchandiser"}},
			{"op": "add", "path": "/metadata_admin/lifecycle_state", "value": auth.ActorActive},
			{"op": "add", "path": "/verifiable_addresses", "value": []map[string]any{
				{"value": profile.Email, "verified": true, "via": "email", "status": "completed"},
			}}}, &updated))
	verified, err := writer.profile(updated)
	require.NoError(t, err)
	require.True(t, verified.EmailVerified, "owned administrator fixture must persist a verified address")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		if r.URL.Path == "/.well-known/openid-configuration" {
			json.NewEncoder(w).Encode(map[string]any{"issuer": base, "jwks_uri": base + "/keys",
				"authorization_endpoint": base + "/auth", "token_endpoint": base + "/token"})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "owned-key",
			"n": base64.RawURLEncoding.EncodeToString(key.PublicKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
		}}})
	}))
	defer issuer.Close()
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": issuer.URL,
		"sub": profile.Subject, "aud": "owned-directory", "exp": time.Now().Add(time.Minute).Unix()})
	token.Header["kid"] = "owned-key"
	oidcToken, err := token.SignedString(key)
	require.NoError(t, err)
	oidc, err := oidcjwt.New(t.Context(), config.OIDCConfig{IssuerURI: issuer.URL, Audience: "owned-directory"}, zap.NewNop())
	require.NoError(t, err)
	hash, err := bcrypt.GenerateFromPassword([]byte("owned-only"), bcrypt.MinCost)
	require.NoError(t, err)
	userFile := filepath.Join(t.TempDir(), "users.yaml")
	require.NoError(t, os.WriteFile(userFile, []byte("version: v1\nusers:\n  - username: "+profile.Username+"\n    password_hash: "+string(hash)+"\n"), 0600))
	static, err := staticusers.New(config.AuthConfig{StaticUsers: config.StaticUsersConfig{UsersFile: userFile},
		JWT: config.JWTConfig{Secret: "owned-only-signing-key-not-a-secret", Issuer: "owned-static", TTL: time.Minute}}, zap.NewNop())
	require.NoError(t, err)
	staticToken, _, err := static.IssueSession(t.Context(), profile.Username)
	require.NoError(t, err)
	policyFile := filepath.Join(t.TempDir(), "policy.yaml")
	require.NoError(t, os.WriteFile(policyFile, []byte(`apiVersion: rbac.authorization.gitstore.dev/v1beta1
kind: RbacLocalConfiguration
clusterRoles:
  merchandiser:
    rules: [{resources: [product], verbs: [create]}]
roleBindings:
  - namespace: acme-store
    roleRef: {kind: ClusterRole, name: merchandiser}
    subjects: [{kind: Group, name: "system:group:merchandiser"}]
`), 0600))
	policy, err := rbaclocal.New(config.RBACConfig{PolicyFile: policyFile}, zap.NewNop())
	require.NoError(t, err)
	cfg.Mappings = []config.SubjectMapping{{Provider: "oidc-jwt", Issuer: issuer.URL, Mode: "uid"},
		{Provider: "static-users", Issuer: "owned-static/static-users", Mode: "uid"}}
	// This enrolled test identity maps the static username through an explicit
	// external ID, not credential or email similarity.
	cfg.Mappings[1].Mode, cfg.Mappings[1].Prefix = "external_id", "owned-static:"
	require.NoError(t, writer.request(ctx, http.MethodPatch, "/admin/identities/"+profile.Subject, nil,
		[]map[string]any{{"op": "add", "path": "/external_id", "value": "owned-static:" + profile.Username}}, &updated))
	for replica := range 2 {
		directory, err := New(cfg)
		require.NoError(t, err)
		for _, credential := range []struct {
			provider auth.AuthNProvider
			token    string
		}{{static, staticToken}, {oidc, oidcToken}} {
			header := make(http.Header)
			header.Set("Authorization", "Bearer "+credential.token)
			principal, decision, err := auth.NewChainedAuthN(credential.provider).Authenticate(t.Context(), auth.AuthRequest{Header: header})
			require.NoError(t, err)
			require.Equal(t, auth.OutcomeAllow, decision.Outcome)
			principal, err = auth.EnrichPrincipal(t.Context(), directory, principal)
			require.NoError(t, err)
			require.Equal(t, profile.Username, principal.Username)
			if verified.Kind == auth.ProfileCustomer {
				require.Equal(t, []string{"system:authenticated", "system:group:shopper", "system:customers"}, principal.Groups)
			} else {
				require.Contains(t, principal.Groups, "system:staff")
				require.Contains(t, principal.Groups, "system:group:merchandiser")
			}
			for _, namespace := range []string{"acme-store", "other-store"} {
				decision, err = policy.Authorize(t.Context(), principal, "product.create",
					auth.ResourceContext{Kind: "product", Name: "owned-product", Attrs: map[string]any{"namespace": namespace}})
				require.NoError(t, err)
				require.Equal(t, verified.Kind == auth.ProfileStaff && namespace == "acme-store", decision.Outcome == auth.OutcomeAllow,
					"replica %d / %s / %s", replica, credential.provider.Name(), namespace)
			}
		}
	}
}
