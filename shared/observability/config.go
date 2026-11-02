// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package observability provides bounded, shared Go telemetry.
package observability

import (
	"errors"
	"net/url"
	"strings"
)

const (
	APIService        = "gitstore-api"
	ControllerService = "gitstore-controller-manager"
)

type Config struct {
	OTLP OTLPConfig `mapstructure:"otlp"`
}

type OTLPConfig struct {
	URI              string  `mapstructure:"uri"`
	TraceSampleRatio float64 `mapstructure:"trace_sample_ratio" validate:"gte=0,lte=1"`
}

func (c OTLPConfig) Validate() error {
	if !(c.TraceSampleRatio >= 0 && c.TraceSampleRatio <= 1) {
		return errors.New("observability.otlp.trace_sample_ratio must be between zero and one")
	}
	if c.URI == "" {
		return nil
	}
	endpoint, err := url.Parse(c.URI)
	if err != nil || len(c.URI) > 256 || endpoint == nil ||
		(endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.ForceQuery || endpoint.RawQuery != "" || strings.Contains(c.URI, "#") {
		return errors.New("observability.otlp.uri must be an HTTP(S) URI without credentials, query or fragment")
	}
	return nil
}
