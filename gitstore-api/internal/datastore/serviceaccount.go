// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package datastore

import (
	"fmt"
	"strings"
	"time"
)

// ServiceAccountSubject derives the canonical cluster-scoped subject.
func ServiceAccountSubject(name string) string {
	return "system:serviceaccount:" + name
}

// ParseServiceAccountSubject is the inverse of ServiceAccountSubject: it
// splits "serviceaccount:<namespace>:<name>" back into its namespace/name
// components. ok is false if subject does not match that exact shape (wrong
// prefix, or not exactly three colon-separated parts) — callers (the
// serviceaccount-jwt/serviceaccount-assertion AuthN providers) must treat a
// false ok as "not a ServiceAccount subject," never as an empty-but-valid
// namespace/name.
func ParseServiceAccountSubject(subject string) (name string, ok bool) {
	name, ok = strings.CutPrefix(subject, "system:serviceaccount:")
	if !ok || name == "" || strings.ContainsAny(name, ":/") || len(subject) > 256 {
		return "", false
	}
	return name, true
}

// ApplyServiceAccountKeyUpdate adds/removes public keys on sa in place,
// advancing Generation and ResourceVersion. Returns ErrConflict if
// expectedResourceVersion does not match sa's current ResourceVersion, and
// ErrInvalidArgument if the resulting key set would be empty (an account
// with no enrolled key can never successfully exchange an assertion).
//
// Backends call this after loading the current record and before persisting
// the mutated result, mirroring how ApplyFileStatusPatch centralises the
// resource-version bump for File so memdb and Scylla never duplicate it.
func ApplyServiceAccountKeyUpdate(sa *ServiceAccount, add []ServiceAccountPublicKey, removeKeyIDs []string, expectedResourceVersion string) error {
	if sa.ResourceVersion != expectedResourceVersion {
		return ErrConflict
	}

	remove := make(map[string]bool, len(removeKeyIDs))
	for _, id := range removeKeyIDs {
		remove[id] = true
	}

	kept := make([]ServiceAccountPublicKey, 0, len(sa.PublicKeys))
	for _, key := range sa.PublicKeys {
		if !remove[key.KeyID] {
			kept = append(kept, key)
		}
	}
	kept = append(kept, add...)

	if len(kept) == 0 {
		return fmt.Errorf("%w: service account %s would have zero enrolled keys", ErrInvalidArgument, sa.Name)
	}
	keyIDs := make(map[string]struct{}, len(kept))
	for _, key := range kept {
		if _, exists := keyIDs[key.KeyID]; exists {
			return fmt.Errorf("%w: service account %s has duplicate key ID %q", ErrInvalidArgument, sa.Name, key.KeyID)
		}
		keyIDs[key.KeyID] = struct{}{}
	}

	sa.PublicKeys = kept
	sa.Generation++
	sa.ResourceVersion = nextResourceVersion(sa.ResourceVersion)
	sa.UpdateTimestamp = time.Now().UTC()
	return nil
}
