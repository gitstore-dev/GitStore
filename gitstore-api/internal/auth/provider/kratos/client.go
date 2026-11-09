// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package kratos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"go.uber.org/zap"
)

var ErrInvalidInput = auth.ErrDirectoryInvalidInput

func (p *Provider) get(ctx context.Context, path string, query url.Values, result any) error {
	return p.request(ctx, http.MethodGet, path, query, nil, result)
}

func (p *Provider) request(ctx context.Context, method, path string, query url.Values, payload, result any, headers ...http.Header) (requestErr error) {
	start := time.Now()
	defer func() {
		p.metrics.observe(ctx, method, start, requestErr)
		if errors.Is(requestErr, auth.ErrDirectoryUnavailable) {
			p.log.Debug("directory request failed", zap.String("provider", "kratos"),
				zap.String("error_class", auth.DirectoryErrorClass(requestErr)))
		}
	}()
	select {
	case p.requests <- struct{}{}:
		defer func() { <-p.requests }()
	default:
		p.metrics.saturation.Add(ctx, 1)
		return auth.ErrDirectoryUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	endpoint := strings.TrimRight(p.config.AdminURI, "/") + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil || int64(len(body)) > p.config.MaxResponseBytes {
			return ErrInvalidInput
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return ErrInvalidInput
	}
	req.Header.Set("Accept", "application/json")
	if method == http.MethodPatch {
		req.Header.Set("Content-Type", "application/json-patch+json")
	} else if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if p.config.AdminToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.config.AdminToken)
	}
	response, err := p.client.Do(req)
	if err != nil {
		return auth.ErrDirectoryUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusNoContent:
	case http.StatusNotFound:
		return auth.ErrProfileNotFound
	case http.StatusConflict:
		return auth.ErrDirectoryConflict
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrInvalidInput
	default:
		return auth.ErrDirectoryUnavailable
	}
	body, err = io.ReadAll(io.LimitReader(response.Body, p.config.MaxResponseBytes+1))
	if err != nil || int64(len(body)) > p.config.MaxResponseBytes {
		return auth.ErrDirectoryUnavailable
	}
	if result == nil {
		return nil
	}
	if json.Unmarshal(body, result) != nil {
		return auth.ErrDirectoryUnavailable
	}
	if len(headers) != 0 {
		link := response.Header.Get("Link")
		if len(link) > 4096 {
			return auth.ErrDirectoryUnavailable
		}
		headers[0].Set("Link", link)
	}
	return nil
}
