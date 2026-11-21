// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/stretchr/testify/require"
)

func TestContactReplacementPreservesVerifiedAddressAndScopesBounceToThatAddress(t *testing.T) {
	record := identityFixture()
	record["metadata_admin"].(map[string]any)["setup_complete"] = true
	record["metadata_admin"].(map[string]any)["email_deliverable"] = true
	record["metadata_admin"].(map[string]any)["lifecycle_state"] = auth.ActorInvited
	record["verifiable_addresses"] = []map[string]any{{"value": "alice@example.test", "verified": true}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			var operations []map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&operations))
			for _, operation := range operations {
				switch operation["path"] {
				case "/metadata_admin":
					record["metadata_admin"] = operation["value"]
				case "/traits/email":
					record["traits"].(map[string]any)["email"] = operation["value"]
				case "/state":
					record["state"] = operation["value"]
				default:
					if path, ok := operation["path"].(string); ok && len(path) > len("/metadata_admin/") && path[:len("/metadata_admin/")] == "/metadata_admin/" {
						record["metadata_admin"].(map[string]any)[path[len("/metadata_admin/"):]] = operation["value"]
					}
				}
			}
		}
		json.NewEncoder(w).Encode(record)
	}))
	defer server.Close()
	provider, err := New(directoryConfig(server.URL))
	require.NoError(t, err)
	ctx := auth.ContextWithPrincipal(t.Context(), &auth.Principal{Subject: "operator", AuthMethod: "static-users"})
	profile, err := provider.GetBySubject(ctx, "identity-uid")
	require.NoError(t, err)
	require.True(t, profile.EmailDeliverable, "the trusted deliverability observation must be preserved")
	profile.Email = "replacement@example.test"
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	contact, err := provider.ResolveContact(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, "alice@example.test", contact.DeliverableEmail)
	require.Equal(t, "replacement@example.test", contact.PrimaryEmail)
	require.False(t, contact.EmailVerified)
	require.True(t, contact.EmailDeliverable)
	generation := profile.Metadata.Generation
	profile.EmailDeliverable = false
	profile.ContactConsent.ChangedAt = time.Now().Add(time.Hour)
	require.NoError(t, provider.UpsertProfile(ctx, profile))
	require.Equal(t, generation, profile.Metadata.Generation, "bounce and caller-supplied timestamps are not author-owned changes")
	require.True(t, profile.ContactConsent.ChangedAt.IsZero())
	contact, err = provider.ResolveContact(ctx, profile.Subject)
	require.NoError(t, err)
	require.False(t, contact.EmailDeliverable)
	record["verifiable_addresses"] = []map[string]any{{"value": "replacement@example.test", "verified": true}}
	contact, err = provider.ResolveContact(ctx, profile.Subject)
	require.NoError(t, err)
	require.Equal(t, "replacement@example.test", contact.DeliverableEmail)
	require.True(t, contact.EmailDeliverable, "an old-address bounce must not block the newly verified address")
}
