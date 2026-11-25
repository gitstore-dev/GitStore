// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func bootstrapCommand(args ...string) (int, string) {
	var out, err bytes.Buffer
	code := Run(args, strings.NewReader(""), &out, &err)
	return code, out.String() + err.String()
}

func TestBootstrapIndependentPreparation(t *testing.T) {
	root := t.TempDir()
	apiKey := filepath.Join(root, "api", "issuer.pem")
	controllerKey := filepath.Join(root, "controller", "key.pem")
	record := filepath.Join(root, "controller", "record.json")
	commands := [][]string{
		{"bootstrap", "api", "--private-key-path", apiKey, "--owner-uid", fmt.Sprint(os.Getuid()), "--owner-gid", fmt.Sprint(os.Getgid())},
		{"bootstrap", "controller", "--private-key-path", controllerKey, "--record-output-path", record, "--key-id", "controller-key"},
	}
	for _, args := range commands {
		if code, output := bootstrapCommand(args...); code != 0 {
			t.Fatalf("preparation code=%d: %s", code, output)
		}
	}
	before := make(map[string][]byte)
	for _, path := range []string{apiKey, controllerKey, record} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = data
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("output must be private: %v", err)
		}
	}
	if bytes.Equal(before[apiKey], before[controllerKey]) {
		t.Fatal("API and controller must have distinct keys")
	}
	if _, err := os.Stat(filepath.Join(root, "api", "record.json")); !os.IsNotExist(err) {
		t.Fatal("API preparation must not create controller material")
	}
	for _, args := range commands {
		if code, output := bootstrapCommand(args...); code != 0 || strings.Contains(output, "PRIVATE KEY") {
			t.Fatalf("repeat preparation code=%d: %s", code, output)
		}
	}
	for path, expected := range before {
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, expected) {
			t.Fatal("repeat preparation changed existing material")
		}
	}
	// Accept the original Compose script's field ordering and trailing newline.
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(before[record], &envelope); err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	if err := json.Unmarshal(envelope["values"], &values); err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("{\"format\":\"serviceaccount-signing-key/v1\",\"values\":{\"privateKey\":%q,\"keyID\":%q}}\n", values["privateKey"], values["keyID"])
	if err := os.WriteFile(record, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	// The old Compose shell redirection could leave this record mode 0644.
	if err := os.Chmod(record, 0644); err != nil {
		t.Fatal(err)
	}
	if code, output := bootstrapCommand(commands[1]...); code != 0 {
		t.Fatalf("legacy record rejected: %s", output)
	}
	retained, _ := os.ReadFile(record)
	if string(retained) != legacy {
		t.Fatal("preparation rewrote an existing valid record")
	}
	if info, err := os.Stat(record); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("preparation did not make the existing record private")
	}
}

func TestBootstrapRefusesUnsafeOrInconsistentMaterial(t *testing.T) {
	for _, scenario := range []string{"bad-key", "symlink-key", "missing-key", "changed-kid", "duplicate-record-field", "symlink-record", "directory-key"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			key, record := filepath.Join(root, "key.pem"), filepath.Join(root, "record.json")
			args := []string{"bootstrap", "controller", "--private-key-path", key, "--record-output-path", record, "--key-id", "key-1"}
			if code, output := bootstrapCommand(args...); code != 0 {
				t.Fatal(output)
			}
			switch scenario {
			case "bad-key":
				_ = os.WriteFile(key, []byte("PRIVATE-MARKER"), 0600)
			case "symlink-key", "symlink-record":
				path := key
				if scenario == "symlink-record" {
					path = record
				}
				if err := os.Rename(path, path+".target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".target", path); err != nil {
					t.Fatal(err)
				}
			case "missing-key":
				_ = os.Remove(key)
			case "changed-kid":
				args[len(args)-1] = "different-key"
			case "duplicate-record-field":
				data, _ := os.ReadFile(record)
				data = append([]byte(`{"format":"serviceaccount-signing-key/v1",`), data[1:]...)
				_ = os.WriteFile(record, data, 0600)
			case "directory-key":
				_ = os.Remove(key)
				_ = os.Mkdir(key, 0700)
			}
			before, _ := os.ReadFile(record)
			if code, output := bootstrapCommand(args...); code != 1 || strings.Contains(output, "PRIVATE-MARKER") || strings.Contains(output, "PRIVATE KEY") {
				t.Fatalf("unsafe preparation code=%d: %s", code, output)
			}
			after, _ := os.ReadFile(record)
			if !bytes.Equal(before, after) {
				t.Fatal("failed preparation modified the record")
			}
			if scenario == "missing-key" {
				if _, err := os.Stat(key); !os.IsNotExist(err) {
					t.Fatal("preparation generated a replacement for a lost enrolled key")
				}
			}
			leftovers, _ := filepath.Glob(filepath.Join(root, ".bootstrap-*"))
			if len(leftovers) != 0 {
				t.Fatal("temporary private files leaked")
			}
		})
	}
}

func TestBootstrapConcurrentPreparationAndValidation(t *testing.T) {
	root := t.TempDir()
	key, record := filepath.Join(root, "key.pem"), filepath.Join(root, "record.json")
	args := []string{"bootstrap", "controller", "--private-key-path", key, "--record-output-path", record, "--key-id", "key-1"}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if code, output := bootstrapCommand(args...); code != 0 {
				t.Errorf("concurrent preparation: %s", output)
			}
		})
	}
	wg.Wait()
	for _, invalid := range [][]string{
		{"bootstrap"}, {"bootstrap", "unknown"},
		{"bootstrap", "api", "--private-key-path", "relative.pem"},
		{"bootstrap", "api", "--private-key-path", key, "--owner-uid", "-2"},
		{"bootstrap", "controller", "--private-key-path", key, "--record-output-path", key, "--key-id", "key-1"},
		{"bootstrap", "api", "--private-key-path", key, "--record-output-path", record},
	} {
		if code, _ := bootstrapCommand(invalid...); code != 2 {
			t.Fatalf("invalid arguments accepted: %v", invalid)
		}
	}
}

func TestEnrollmentWritesIndependentControllerTOML(t *testing.T) {
	root := t.TempDir()
	key := filepath.Join(root, "controller.pem")
	identity := filepath.Join(root, "identity", "controller.toml")
	uid := "uid-one"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing administrative authentication")
		}
		_, _ = fmt.Fprintf(w, `{"data":{"createServiceAccount":{"serviceAccount":{"metadata":{"uid":%q}}}}}`, uid)
	}))
	defer server.Close()
	t.Setenv("BOOTSTRAP_TOKEN", "test-token")
	args := []string{"enroll-serviceaccount", "--api-url", server.URL, "--name", "manager", "--key-id", "key-1", "--private-key-path", key, "--identity-output-path", identity, "--identity-output-format", "toml"}
	if code, output := bootstrapCommand(args...); code != 0 {
		t.Fatal(output)
	}
	data, _ := os.ReadFile(identity)
	if string(data) != "[controller.serviceaccount]\nuid = \"uid-one\"\n" {
		t.Fatal("identity is not a controller configuration overlay")
	}
	if err := requireEnrollmentIdentity(identity, "toml"); err != nil {
		t.Fatal(err)
	}
	uid = "uid-two"
	if code, _ := bootstrapCommand(args...); code != 1 {
		t.Fatal("silently replaced the saved ServiceAccount identity")
	}
	if code, output := bootstrapCommand(append(args, "--replace-identity")...); code != 0 {
		t.Fatal(output)
	}
	if got, _ := os.ReadFile(identity); string(got) != string(identityBytes(uid, "toml")) {
		t.Fatal("explicit identity replacement failed")
	}
	if err := os.WriteFile(identity, []byte("unrelated config"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := bootstrapCommand(append(args, "--replace-identity")...); code != 1 {
		t.Fatal("explicit replacement overwrote an unrelated file")
	}
}
