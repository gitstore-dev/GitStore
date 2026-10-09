// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package staticusers

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const newUsersFile = "version: v1\nusers: []\n"

// AddUser appends one user to a static-users YAML file. Existing comments and
// ordering are retained, and the validated result replaces the file atomically.
func AddUser(path string, user UserEntry) error {
	if strings.TrimSpace(user.Username) == "" {
		return errors.New("staticusers: username is required")
	}
	if user.PasswordHash == "" {
		return errors.New("staticusers: password hash is required")
	}

	original, mode, existed, err := readUsersFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(original, &document); err != nil {
		return fmt.Errorf("staticusers: parse %q: %w", path, err)
	}
	root, err := usersRoot(&document)
	if err != nil {
		return fmt.Errorf("staticusers: parse %q: %w", path, err)
	}
	users, err := mappingValue(root, "users")
	if err != nil {
		return fmt.Errorf("staticusers: parse %q: %w", path, err)
	}
	if users.Kind != yaml.SequenceNode {
		return fmt.Errorf("staticusers: parse %q: users must be a sequence", path)
	}
	for _, entry := range users.Content {
		username, lookupErr := mappingValue(entry, "username")
		if lookupErr == nil && username.Value == user.Username {
			return fmt.Errorf("%w: %q", ErrUserExists, user.Username)
		}
	}
	users.Style = 0
	users.Content = append(users.Content, userNode(user))

	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return fmt.Errorf("staticusers: encode %q: %w", path, err)
	}
	if err := encoder.Close(); err != nil {
		return fmt.Errorf("staticusers: encode %q: %w", path, err)
	}
	if _, err := parseUsers(path, output.Bytes()); err != nil {
		return err
	}
	return replaceUsersFile(path, original, output.Bytes(), mode, existed)
}

func readUsersFile(path string) ([]byte, fs.FileMode, bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []byte(newUsersFile), 0o600, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("staticusers: read %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, false, fmt.Errorf("staticusers: stat %q: %w", path, err)
	}
	return b, info.Mode().Perm(), true, nil
}

func usersRoot(document *yaml.Node) (*yaml.Node, error) {
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("document root must be a mapping")
	}
	root := document.Content[0]
	version, err := mappingValue(root, "version")
	if err != nil || version.Value != "v1" {
		return nil, errors.New("version must be v1")
	}
	return root, nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, error) {
	if mapping.Kind != yaml.MappingNode {
		return nil, errors.New("expected a mapping")
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1], nil
		}
	}
	return nil, fmt.Errorf("missing %q", key)
}

func userNode(user UserEntry) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	appendScalar := func(key, value string) {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
		)
	}
	appendScalar("username", user.Username)
	appendScalar("password_hash", user.PasswordHash)
	if user.DisplayName != "" {
		appendScalar("display_name", user.DisplayName)
	}
	if user.Email != "" {
		appendScalar("email", user.Email)
	}
	return node
}

func replaceUsersFile(path string, original, replacement []byte, mode fs.FileMode, existed bool) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".users.yaml.tmp-*")
	if err != nil {
		return fmt.Errorf("staticusers: create temporary file beside %q: %w", path, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return fmt.Errorf("staticusers: set temporary file mode: %w", err)
	}
	if _, err := tmp.Write(replacement); err != nil {
		tmp.Close()
		return fmt.Errorf("staticusers: write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("staticusers: sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("staticusers: close temporary file: %w", err)
	}

	current, err := os.ReadFile(path)
	if existed {
		if err != nil || !bytes.Equal(current, original) {
			return fmt.Errorf("staticusers: %q changed while adding the user; retry", path)
		}
	} else if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("staticusers: %q was created while adding the user; retry", path)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("staticusers: replace %q: %w", path, err)
	}
	return nil
}
