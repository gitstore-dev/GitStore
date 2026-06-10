// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package graph

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

const (
	nodeKindProduct        = "Product"
	nodeKindCategory       = "Category"
	nodeKindCollection     = "Collection"
	nodeKindNamespace      = "Namespace"
	nodeKindRepository     = "Repository"
	nodeKindProductVariant = "ProductVariant"

	globalIDScheme = "gid"
	globalIDHost   = "GitStore"
)

var supportedNodeKinds = map[string]struct{}{
	nodeKindProduct:        {},
	nodeKindCategory:       {},
	nodeKindCollection:     {},
	nodeKindNamespace:      {},
	nodeKindRepository:     {},
	nodeKindProductVariant: {},
}

// EncodeNodeID returns an opaque Relay-style global ID for a GraphQL Node.
func EncodeNodeID(kind, rawID string) (string, error) {
	if _, ok := supportedNodeKinds[kind]; !ok {
		return "", fmt.Errorf("unsupported node kind %q", kind)
	}
	if rawID == "" {
		return "", fmt.Errorf("raw ID is required")
	}
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("gid://GitStore/%s/%s", kind, rawID))), nil
}

// DecodeNodeID decodes and validates a GraphQL Node global ID.
func DecodeNodeID(encoded string) (kind string, rawID string, err error) {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", fmt.Errorf("invalid global ID encoding: %w", err)
	}

	u, err := url.Parse(string(decoded))
	if err != nil {
		return "", "", fmt.Errorf("invalid global ID URI: %w", err)
	}
	if u.Scheme != globalIDScheme {
		return "", "", fmt.Errorf("invalid global ID scheme %q", u.Scheme)
	}
	if u.Host != globalIDHost {
		return "", "", fmt.Errorf("invalid global ID host %q", u.Host)
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("invalid global ID URI")
	}

	parts := strings.SplitN(strings.TrimPrefix(u.EscapedPath(), "/"), "/", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("global ID must include node kind and raw ID")
	}
	kind, err = url.PathUnescape(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("invalid global ID kind: %w", err)
	}
	rawID, err = url.PathUnescape(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("invalid global ID raw ID: %w", err)
	}
	if kind == "" {
		return "", "", fmt.Errorf("node kind is required")
	}
	if rawID == "" {
		return "", "", fmt.Errorf("raw ID is required")
	}
	if _, ok := supportedNodeKinds[kind]; !ok {
		return "", "", fmt.Errorf("unsupported node kind %q", kind)
	}
	return kind, rawID, nil
}

func mustEncodeNodeID(kind, rawID string) string {
	id, err := EncodeNodeID(kind, rawID)
	if err != nil {
		panic(err)
	}
	return id
}

func decodeNodeIDAs(kind, id string) (string, error) {
	actualKind, rawID, err := DecodeNodeID(id)
	if err != nil {
		return "", invalidGlobalIDError(err)
	}
	if actualKind != kind {
		return "", gqlerror.Errorf("invalid global ID kind: expected %s, got %s", kind, actualKind)
	}
	return rawID, nil
}

func decodeNodeIDsAs(kind string, ids []string) ([]string, error) {
	rawIDs := make([]string, len(ids))
	for i, id := range ids {
		rawID, err := decodeNodeIDAs(kind, id)
		if err != nil {
			return nil, err
		}
		rawIDs[i] = rawID
	}
	return rawIDs, nil
}

func invalidGlobalIDError(err error) error {
	return gqlerror.Errorf("invalid global ID: %v", err)
}
