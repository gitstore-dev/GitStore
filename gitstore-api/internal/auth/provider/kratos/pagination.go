// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/google/uuid"
)

func (p *Provider) cursorPrefix() string {
	source := sha256.Sum256([]byte(strings.TrimRight(p.config.AdminURI, "/")))
	return "kratos:actors:v1:" + hex.EncodeToString(source[:]) + ":"
}

func (p *Provider) encodeCursor(id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(p.cursorPrefix() + id))
}

func (p *Provider) decodeCursor(cursor string) (string, error) {
	if len(cursor) > 256 {
		return "", ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", ErrInvalidInput
	}
	id, found := strings.CutPrefix(string(decoded), p.cursorPrefix())
	if !found {
		return "", ErrInvalidInput
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", ErrInvalidInput
	}
	return id, nil
}

func (p *Provider) ListActors(ctx context.Context, request auth.ActorPageRequest) (*auth.ActorPage, error) {
	if err := request.ValidateForward(); err != nil {
		return nil, err
	}
	size := 100
	if request.First != nil {
		size = *request.First
	}
	query := url.Values{"page_size": {strconv.Itoa(size)}}
	if request.After != nil {
		id, err := p.decodeCursor(*request.After)
		if err != nil {
			return nil, err
		}
		query.Set("page_token", id)
	}
	var records []identity
	headers := make(http.Header)
	if err := p.request(ctx, http.MethodGet, "/admin/identities", query, nil, &records, headers); err != nil {
		return nil, err
	}
	if len(records) > size {
		return nil, auth.ErrDirectoryUnavailable
	}
	page := &auth.ActorPage{Edges: make([]auth.ActorEdge, 0, len(records)),
		HasPreviousPage: request.After != nil,
		HasNextPage:     strings.Contains(headers.Get("Link"), `rel="next"`)}
	previous := query.Get("page_token")
	for _, record := range records {
		if _, err := uuid.Parse(record.ID); err != nil || record.ID <= previous {
			return nil, auth.ErrDirectoryUnavailable
		}
		profile, err := p.profile(record)
		if err != nil {
			return nil, err
		}
		page.Edges = append(page.Edges, auth.ActorEdge{Cursor: p.encodeCursor(record.ID), Profile: profile})
		previous = record.ID
	}
	if len(page.Edges) != 0 {
		page.StartCursor = &page.Edges[0].Cursor
		page.EndCursor = &page.Edges[len(page.Edges)-1].Cursor
	}
	return page, nil
}
