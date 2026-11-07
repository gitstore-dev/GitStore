// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package resolver

import (
	"encoding/base64"
	"net/url"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// DecodeNodeID validates the wire format without consulting current rows. The
// public kind allow-list is independent of Admin's generated models and IDs.
func DecodeNodeID(id string) (string, error) {
	invalid := func() (string, error) {
		return "", &gqlerror.Error{Message: "invalid global ID", Extensions: map[string]any{"code": "BAD_USER_INPUT", "reason": "INVALID_GLOBAL_ID"}}
	}
	raw, err := base64.StdEncoding.DecodeString(id)
	if err != nil {
		return invalid()
	}
	u, err := url.Parse(string(raw))
	if err != nil || u.Scheme != "gid" || u.Host != "GitStore" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid()
	}
	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return invalid()
	}
	kind, err := url.PathUnescape(parts[0])
	if err != nil {
		return invalid()
	}
	uid, err := url.PathUnescape(parts[1])
	if err != nil || uid == "" {
		return invalid()
	}
	if kind != "StorefrontProduct" {
		return "", &gqlerror.Error{Message: "resource not found", Extensions: map[string]any{"code": "NOT_FOUND"}}
	}
	return uid, nil
}
