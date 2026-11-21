// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package scylla

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gocql/gocql"
	"github.com/stretchr/testify/require"
)

func TestServiceAccountCDCEnrollmentVisibilityAndIdentity(t *testing.T) {
	uid := gocql.TimeUUID()
	row := &serviceAccountRow{UID: uid, Name: "ci-bot", CreationTimestamp: time.Now().UTC(),
		Generation: 1, ResourceVersion: "1", PublicKeys: "[]"}
	payload, err := marshalServiceAccountCDC(row)
	require.NoError(t, err)
	require.Empty(t, payload, "partial enrollment and projection failures must not publish")
	row.EnrollmentComplete = true
	payload, err = marshalServiceAccountCDC(row)
	require.NoError(t, err)
	var account datastore.ServiceAccount
	require.NoError(t, json.Unmarshal(payload, &account))
	require.Equal(t, uid.String(), account.UID)
	require.Equal(t, row.Name, account.Name)
	require.Equal(t, row.ResourceVersion, account.ResourceVersion)
	row.Disabled = true
	payload, err = marshalServiceAccountCDC(row)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(payload, &account))
	require.True(t, account.Disabled)
}

func TestServiceAccountCDCRejectsIncompleteFullImages(t *testing.T) {
	for _, row := range []*serviceAccountRow{
		nil,
		{EnrollmentComplete: true},
		{EnrollmentComplete: true, UID: gocql.TimeUUID(), Name: "ci-bot", CreationTimestamp: time.Now().UTC(), ResourceVersion: "1", PublicKeys: "invalid"},
	} {
		_, err := marshalServiceAccountCDC(row)
		require.Error(t, err)
	}
}
