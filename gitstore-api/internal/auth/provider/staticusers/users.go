// SPDX-License-Identifier: AGPL-3.0-or-later
package staticusers

import (
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
		return nil, fmt.Errorf("staticusers: read users file %q: %w", path, err)
	}
	var list UserList
	if err := yaml.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("staticusers: parse users file %q: %w", path, err)
	}
	if list.Version != "v1" {
		return nil, fmt.Errorf("staticusers: invalid users file %q: unsupported version %q", path, list.Version)
	}
	if len(list.Users) == 0 {
		return nil, fmt.Errorf("staticusers: invalid users file %q: at least one user is required", path)
	}
	users := make(map[string]UserEntry, len(list.Users))
	for _, u := range list.Users {
		if strings.TrimSpace(u.Username) == "" {
			return nil, fmt.Errorf("staticusers: invalid users file %q: username must be non-empty", path)
		}
		if u.PasswordHash == "" {
			return nil, fmt.Errorf("staticusers: invalid users file %q: password_hash for %q must be non-empty", path, u.Username)
		}
		if _, exists := users[u.Username]; exists {
			return nil, fmt.Errorf("staticusers: invalid users file %q: duplicate username %q", path, u.Username)
		}
		users[u.Username] = u
	}
	return users, nil
}
