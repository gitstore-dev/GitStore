// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package namespace

import (
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrInvalidIdentifier  = errors.New("invalid namespace identifier")
	ErrReservedIdentifier = errors.New("reserved namespace identifier")
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$|^[a-z0-9]$`)

var reservedIdentifiers = map[string]struct{}{
	"admin": {}, "root": {}, "system": {}, "default": {}, "api": {}, "git": {},
	"www": {}, "mail": {}, "smtp": {}, "ftp": {}, "org": {}, "orgs": {},
	"static": {}, "assets": {}, "cdn": {}, "docs": {}, "help": {}, "support": {},
	"billing": {}, "status": {}, "health": {}, "internal": {}, "local": {},
	"localhost": {}, "null": {}, "undefined": {}, "true": {}, "false": {},
	"new": {}, "test": {}, "gitstore": {}, "enterprise": {}, "user": {},
	"namespace": {}, "namespaces": {}, "repo": {}, "repos": {},
}

func ValidateIdentifier(identifier string) error {
	if !identifierPattern.MatchString(identifier) {
		return fmt.Errorf(
			"metadata.name must match DNS label format (lowercase alphanumeric and hyphens, 1-63 chars, no leading/trailing hyphen): %w",
			ErrInvalidIdentifier,
		)
	}
	if _, reserved := reservedIdentifiers[identifier]; reserved && !IsBootstrap(identifier) {
		return fmt.Errorf("metadata.name identifier %q is reserved: %w", identifier, ErrReservedIdentifier)
	}
	return nil
}
