// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOwnedKratosHTTPCourier(t *testing.T) {
	r := startDirectoryTestRuntime(t, true)
	r.command(t, nil, "exec", r.receiver, "/directory-receiver.test",
		"-test.run=^TestOwnedIdentityMailEndpoint$", "-test.timeout=30s")
	r.command(t, nil, "exec", r.receiver, "/directory-receiver.test",
		"-test.run=^TestOwnedStaffInvitationsAndContact$", "-test.timeout=30s")
	customer := r.createHuman(t, "customer", "mail-customer", "courier@example.test")
	status, body := r.requestAt(t, r.public, http.MethodGet, "/self-service/verification/api", nil)
	require.Equal(t, http.StatusOK, status)
	var flow struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal(body, &flow))
	require.NotEmpty(t, flow.ID)
	status, _ = r.requestAt(t, r.public, http.MethodPost, "/self-service/verification?flow="+flow.ID,
		map[string]any{"method": "code", "email": customer.Traits["email"]},
		http.Header{"Accept": {"application/json"}})
	require.Equal(t, http.StatusOK, status, "native verification flow must queue the authenticated HTTP courier")
	require.Eventually(t, func() bool {
		response, err := r.client.Get(r.mailAPI + "/mail")
		if err != nil {
			return false
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return err == nil && response.StatusCode == http.StatusOK && strings.Contains(string(data), "courier@example.test")
	}, 30*time.Second, 250*time.Millisecond, "native HTTP delivery must reach the reference SMTP catcher")
	response, err := r.client.Get(r.mailAPI + "/mail")
	require.NoError(t, err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	require.NoError(t, err)
	require.Equal(t, 2, strings.Count(string(data), "handler@example.test"),
		"duplicate HTTP deliveries are accepted without an exactly-once promise")
	require.Equal(t, 2, strings.Count(string(data), "invited-staff@example.test"),
		"create and resend must deliver native private setup invitations")
	catcher := "gitstore-userdir-mail-" + r.owner[:12]
	r.command(t, nil, "stop", "--time", "5", catcher)
	r.command(t, nil, "exec", "-e", "GITSTORE_TEST_MAIL_OUTAGE=1", r.receiver,
		"/directory-receiver.test", "-test.run=^TestOwnedIdentityMailEndpoint$", "-test.timeout=30s")
}
