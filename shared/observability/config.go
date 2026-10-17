// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package observability provides bounded, shared Go telemetry.
package observability

import (
	"errors"
	"net/netip"
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

type ProfilingConfig struct {
	URI string `mapstructure:"uri"`
}

func (c ProfilingConfig) Validate() error {
	_, err := c.ListenAddress()
	return err
}

func (c ProfilingConfig) ListenAddress() (string, error) {
	if c.URI == "" {
		return "", nil
	}
	invalid := errors.New("profiling.uri must be an HTTP URI with an explicit loopback host and nonzero port, without credentials, base path, query or fragment")
	uri, err := url.Parse(c.URI)
	if err != nil || len(c.URI) > 256 || uri == nil || uri.Scheme != "http" ||
		uri.User != nil || (uri.Path != "" && uri.Path != "/") || uri.RawPath != "" ||
		uri.ForceQuery || uri.RawQuery != "" || strings.Contains(c.URI, "#") {
		return "", invalid
	}
	address, err := netip.ParseAddrPort(uri.Host)
	if err != nil || !address.Addr().IsLoopback() || address.Port() == 0 {
		return "", invalid
	}
	return uri.Host, nil
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
