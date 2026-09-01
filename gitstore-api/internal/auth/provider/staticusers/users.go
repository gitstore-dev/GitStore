// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
package staticusers

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
)

type UserList struct {
	Version string      `yaml:"version"`
	Users   []UserEntry `yaml:"users"`
}
type UserEntry struct {
	Username     string `yaml:"username"`
	PasswordHash string `yaml:"password_hash"`
	DisplayName  string `yaml:"display_name"`
	Email        string `yaml:"email"`
}

func loadUsers(path string) (map[string]UserEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, usersFileError(path, "the users file could not be read", "create it from gitstore-api/users.yaml.example", err)
	}
	var list UserList
	if err := yaml.Unmarshal(b, &list); err != nil {
		return nil, usersFileError(path, "the YAML is malformed", "fix the YAML syntax and keep the version: v1 header", err)
	}
	if list.Version != "v1" {
		return nil, usersFileError(path, fmt.Sprintf("version %q is unsupported", list.Version), "set version: v1", nil)
	}
	if len(list.Users) == 0 {
		return nil, usersFileError(path, "no users are configured", "add at least one entry under users:", nil)
	}
	users := make(map[string]UserEntry, len(list.Users))
	for _, u := range list.Users {
		if strings.TrimSpace(u.Username) == "" {
			return nil, usersFileError(path, "a username is empty", "set users[].username to a non-empty value", nil)
		}
		if u.PasswordHash == "" {
			return nil, usersFileError(path, fmt.Sprintf("password_hash for %q is empty", u.Username), "set users[].password_hash to a bcrypt hash", nil)
		}
		if _, exists := users[u.Username]; exists {
			return nil, usersFileError(path, fmt.Sprintf("username %q is duplicated", u.Username), "keep each users[].username unique", nil)
		}
		users[u.Username] = u
	}
	return users, nil
}

func usersFileError(path, problem, fix string, cause error) error {
	message := fmt.Sprintf("startup failed: invalid static-users file %q\n\n  Problem: %s\n\n  To fix: %s\n\n  See specs/060-local-multiuser-authn/quickstart.md for a worked example", path, problem, fix)
	if cause != nil {
		return fmt.Errorf("%s: %w", message, cause)
	}
	return errors.New(message)
}
