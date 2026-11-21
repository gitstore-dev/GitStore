// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package security

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
)

var (
	errTransferInvalidKind   = errors.New("targetOwnerRef.kind must be USER, GROUP, or SERVICE_ACCOUNT")
	errTransferMissingTarget = errors.New("targetOwnerRef.name is required")
	errTransferInvalidTarget = errors.New("targetOwnerRef.name is not valid for its identity kind")
)

// normalizeTransferTarget supplies identity facts only. Permission, ownership
// and target membership are evaluated together by the live AuthZ provider.
func normalizeTransferTarget(targetKind, targetName string) (string, error) {
	if targetName == "" {
		return "", errTransferMissingTarget
	}
	var ownerKind datastore.OwnerKind
	switch targetKind {
	case "USER":
		if !auth.ValidExternalUsername(targetName) {
			return "", errTransferInvalidTarget
		}
		ownerKind = datastore.OwnerKindUser
	case "GROUP":
		ownerKind = datastore.OwnerKindGroup
		targetName = strings.TrimPrefix(targetName, "system:group:")
	case "SERVICE_ACCOUNT":
		ownerKind = datastore.OwnerKindServiceAccount
		targetName = strings.TrimPrefix(targetName, "system:serviceaccount:")
	default:
		return "", errTransferInvalidKind
	}
	if targetName == "" {
		return "", errTransferMissingTarget
	}
	if len(targetName) > 256 || !utf8.ValidString(targetName) || strings.IndexFunc(targetName, unicode.IsControl) >= 0 || (ownerKind != datastore.OwnerKindUser && strings.ContainsAny(targetName, ":/")) {
		return "", errTransferInvalidTarget
	}
	return datastore.EncodeOwnerSubject(ownerKind, targetName), nil
}
