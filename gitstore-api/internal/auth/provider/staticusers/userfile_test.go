// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package staticusers

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddUserPreservesCommentsOrderAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	original := "# operator comment\nversion: v1\nusers:\n  - username: alice\n    password_hash: old-hash\n"
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := AddUser(path, UserEntry{Username: "bob", PasswordHash: "new-hash"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	if !strings.Contains(text, "# operator comment") || strings.Index(text, "alice") > strings.Index(text, "bob") {
		t.Fatalf("comments or order not preserved:\n%s", text)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %o, want 640", got)
	}
}

func TestAddUserRejectsDuplicate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.yaml")
	if err := os.WriteFile(path, []byte("version: v1\nusers:\n  - username: alice\n    password_hash: old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AddUser(path, UserEntry{Username: "alice", PasswordHash: "new"})
	if !errors.Is(err, ErrUserExists) {
		t.Fatalf("AddUser() error = %v, want ErrUserExists", err)
	}
}
