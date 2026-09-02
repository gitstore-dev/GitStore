// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnrollServiceAccountIsIdempotentAndDoesNotWriteCredentials(t *testing.T) {
	const adminToken = "admin-token-must-not-be-printed"
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		if got := request.Header.Get("Authorization"); got != "Bearer "+adminToken {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !strings.Contains(payload.Query, "createServiceAccount") {
			t.Errorf("query = %q, want createServiceAccount", payload.Query)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			_, _ = w.Write([]byte(`{"data":{"createServiceAccount":{"metadata":{"uid":"uid-1"}}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"errors":[{"message":"service account controllers:manager already exists"}]}`))
	}))
	defer server.Close()

	keyPath := filepath.Join(t.TempDir(), "controller.pem")
	args := []string{
		"enroll-serviceaccount",
		"--api-url", server.URL,
		"--admin-token", adminToken,
		"--namespace", "controllers",
		"--name", "manager",
		"--key-id", "key-1",
		"--private-key-path", keyPath,
	}

	var stdout, stderr bytes.Buffer
	if got := run(args, strings.NewReader(""), &stdout, &stderr); got != 0 {
		t.Fatalf("first enrollment exit code = %d, stderr = %q", got, stderr.String())
	}
	privateKey, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read generated private key: %v", err)
	}
	if info, err := os.Stat(keyPath); err != nil {
		t.Fatalf("stat generated private key: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Errorf("private-key permissions = %o, want 600", info.Mode().Perm())
	}

	stdout.Reset()
	stderr.Reset()
	if got := run(args, strings.NewReader(""), &stdout, &stderr); got != 0 {
		t.Fatalf("second enrollment exit code = %d, stderr = %q", got, stderr.String())
	}
	if got, err := os.ReadFile(keyPath); err != nil {
		t.Fatalf("read idempotent private key: %v", err)
	} else if !bytes.Equal(got, privateKey) {
		t.Error("idempotent enrollment changed the private key")
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2 create attempts", requests)
	}

	output := stdout.String() + stderr.String()
	for _, sensitive := range []string{adminToken, string(privateKey)} {
		if strings.Contains(output, sensitive) {
			t.Errorf("command output leaked sensitive material")
		}
	}
}

func TestEnrollServiceAccountRejectsRelativePrivateKeyPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if got := run([]string{
		"enroll-serviceaccount",
		"--admin-token", "admin-token",
		"--private-key-path", "controller.pem",
	}, strings.NewReader(""), &stdout, &stderr); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if strings.Contains(stdout.String()+stderr.String(), "admin-token") {
		t.Fatal("command output leaked administrator token")
	}
}

func TestEnrollServiceAccountHelpDoesNotExposeBootstrapToken(t *testing.T) {
	const bootstrapToken = "bootstrap-token-must-not-be-printed"
	t.Setenv("BOOTSTRAP_TOKEN", bootstrapToken)

	var stdout, stderr bytes.Buffer
	if got := run([]string{"enroll-serviceaccount", "--help"}, strings.NewReader(""), &stdout, &stderr); got != 2 {
		t.Fatalf("exit code = %d, want 2", got)
	}
	if strings.Contains(stdout.String()+stderr.String(), bootstrapToken) {
		t.Fatal("command help leaked bootstrap token")
	}
}

func TestEnrollServiceAccountBootstrapsAdminSessionAndWritesIdentity(t *testing.T) {
	const (
		adminPassword = "admin-password-must-not-be-printed"
		accessToken   = "access-token-must-not-be-printed"
	)
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests++
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if requests == 1 {
			if !strings.Contains(payload.Query, "mutation Login") {
				t.Errorf("query = %q, want login mutation", payload.Query)
			}
			_, _ = w.Write([]byte(`{"data":{"login":{"token":{"accessToken":"` + accessToken + `"}}}}`))
			return
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("Authorization = %q, want access token", got)
		}
		_, _ = w.Write([]byte(`{"data":{"createServiceAccount":{"metadata":{"uid":"service-account-uid"}}}}`))
	}))
	defer server.Close()

	t.Setenv("GITSTORE_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GITSTORE_BOOTSTRAP_ADMIN_PASSWORD", adminPassword)
	root := t.TempDir()
	keyPath := filepath.Join(root, "private-key.pem")
	identityPath := filepath.Join(root, "serviceaccount.env")
	var stdout, stderr bytes.Buffer
	if got := run([]string{
		"enroll-serviceaccount",
		"--api-url", server.URL,
		"--private-key-path", keyPath,
		"--identity-output-path", identityPath,
	}, strings.NewReader(""), &stdout, &stderr); got != 0 {
		t.Fatalf("enrollment exit code = %d, stderr = %q", got, stderr.String())
	}
	if got, err := os.ReadFile(identityPath); err != nil {
		t.Fatalf("read identity: %v", err)
	} else if string(got) != "GITSTORE_CONTROLLER__SERVICEACCOUNT__UID=service-account-uid\n" {
		t.Errorf("identity file = %q", got)
	}
	output := stdout.String() + stderr.String()
	for _, sensitive := range []string{adminPassword, accessToken} {
		if strings.Contains(output, sensitive) {
			t.Error("command output leaked bootstrap credentials")
		}
	}
}
