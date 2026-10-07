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

func TestServiceAccountIdentityPublicationIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.env")
	for range 2 {
		if err := writeServiceAccountIdentity(path, "uid-1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeServiceAccountIdentity(path, "different-uid"); err == nil {
		t.Fatal("must not silently replace a different enrolled identity")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "GITSTORE_CONTROLLER__SERVICEACCOUNT__UID=uid-1\n" {
		t.Fatal("existing identity was changed")
	}
}

func TestGenerateSigningRecordPreservesExistingKey(t *testing.T) {
	root := t.TempDir()
	keyPath, recordPath := filepath.Join(root, "private.pem"), filepath.Join(root, "controller.json")
	args := []string{"generate-signing-key", "--private-key-path", keyPath, "--record-output-path", recordPath, "--key-id", "enrolled-key"}
	var stdout, stderr bytes.Buffer
	for range 2 {
		if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("generate signing record: code=%d stderr=%s", code, stderr.String())
		}
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		Format string            `json:"format"`
		Values map[string][]byte `json:"values"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.Format != "serviceaccount-signing-key/v1" || !bytes.Equal(record.Values["privateKey"], key) || string(record.Values["keyID"]) != "enrolled-key" {
		t.Fatal("record does not contain the matching key and enrollment ID")
	}
	if info, err := os.Stat(recordPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("record must remain private")
	}
	if strings.Contains(stdout.String()+stderr.String(), "PRIVATE KEY") || bytes.Contains([]byte(stdout.String()+stderr.String()), key) {
		t.Fatal("private material appeared in command output")
	}
	args[len(args)-1] = "different-key"
	if code := run(args, strings.NewReader(""), &stdout, &stderr); code != 1 {
		t.Fatal("generator must not silently replace an enrolled signing record")
	}
	after, err := os.ReadFile(recordPath)
	if err != nil || !bytes.Equal(data, after) {
		t.Fatal("failed record update changed existing material")
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".signing-record-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("private temporary records were not cleaned up")
	}
}

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
			_, _ = w.Write([]byte(`{"data":{"createServiceAccount":{"serviceAccount":{"metadata":{"uid":"uid-1"}}}}}`))
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
	for _, instruction := range []string{"distinct key ID", "atomically", "all replicas"} {
		if !strings.Contains(stdout.String(), instruction) {
			t.Errorf("enrollment output missing rotation instruction %q", instruction)
		}
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
		_, _ = w.Write([]byte(`{"data":{"createServiceAccount":{"serviceAccount":{"metadata":{"uid":"service-account-uid"}}}}}`))
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

func TestEnrollServiceAccountSurfacesLoginFailureDetail(t *testing.T) {
	const adminPassword = "wrong-password-must-not-be-printed"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":null,"errors":[{"message":"invalid username or password"}]}`))
	}))
	defer server.Close()

	t.Setenv("GITSTORE_BOOTSTRAP_ADMIN_USERNAME", "admin")
	t.Setenv("GITSTORE_BOOTSTRAP_ADMIN_PASSWORD", adminPassword)
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if got := run([]string{
		"enroll-serviceaccount",
		"--api-url", server.URL,
		"--private-key-path", filepath.Join(root, "private-key.pem"),
		"--identity-output-path", filepath.Join(root, "serviceaccount.env"),
	}, strings.NewReader(""), &stdout, &stderr); got != 1 {
		t.Fatalf("enrollment exit code = %d, want 1", got)
	}
	// The real server-side cause must surface, not just the generic
	// "bootstrap authentication failed" wrapper around it.
	if !strings.Contains(stderr.String(), "invalid username or password") {
		t.Errorf("stderr = %q, want it to contain the underlying GraphQL error", stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), adminPassword) {
		t.Error("command output leaked bootstrap credentials")
	}
}
