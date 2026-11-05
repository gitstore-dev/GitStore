// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/spf13/viper"
	"golang.org/x/net/http/httpguts"
)

// EndpointConfig contains independent browser and execution policies. Ports are
// named fields on ApiConfig so they follow the existing Git/catalog convention.
type EndpointConfig struct {
	ControllerSubject string        `mapstructure:"controller_subject"`
	CORS              CORSConfig    `mapstructure:"cors"`
	Limits            GraphQLLimits `mapstructure:"limits"`
}

func (e EndpointConfig) Validate() error {
	if err := e.CORS.Validate(); err != nil {
		return err
	}
	if math.IsInf(e.Limits.PrincipalPerSecond, 0) || math.IsNaN(e.Limits.PrincipalPerSecond) {
		return fmt.Errorf("principal_per_second must be finite")
	}
	return validator.New().Struct(e.Limits)
}

type CORSConfig struct {
	AllowedOrigins   []string      `mapstructure:"allowed_origins"`
	AllowCredentials bool          `mapstructure:"allow_credentials"`
	AllowedHeaders   []string      `mapstructure:"allowed_headers"`
	ExposedHeaders   []string      `mapstructure:"exposed_headers"`
	MaxAge           time.Duration `mapstructure:"max_age"`
}

// GraphQLLimits are finite per-replica protections, not cluster billing quotas.
type GraphQLLimits struct {
	PrincipalPerSecond float64       `mapstructure:"principal_per_second" validate:"gt=0"`
	PrincipalBurst     int           `mapstructure:"principal_burst" validate:"gt=0"`
	BodyBytes          int64         `mapstructure:"body_bytes" validate:"gt=0"`
	DocumentBytes      int           `mapstructure:"document_bytes" validate:"gt=0"`
	ParserTokens       int           `mapstructure:"parser_tokens" validate:"gt=0"`
	Depth              int           `mapstructure:"depth" validate:"gt=0"`
	Aliases            int           `mapstructure:"aliases" validate:"gt=0"`
	RootFields         int           `mapstructure:"root_fields" validate:"gt=0"`
	Directives         int           `mapstructure:"directives" validate:"gt=0"`
	FragmentVisits     int           `mapstructure:"fragment_visits" validate:"gt=0"`
	ValidationErrors   int           `mapstructure:"validation_errors" validate:"gt=0"`
	PageSize           int           `mapstructure:"page_size" validate:"gt=0"`
	NodeIDs            int           `mapstructure:"node_ids" validate:"gt=0"`
	Complexity         int           `mapstructure:"complexity" validate:"gt=0"`
	InFlight           int           `mapstructure:"in_flight" validate:"gt=0"`
	LimiterKeys        int           `mapstructure:"limiter_keys" validate:"gt=0"`
	WSConnections      int           `mapstructure:"ws_connections" validate:"gt=0"`
	WSOperations       int           `mapstructure:"ws_operations" validate:"gt=0"`
	Deadline           time.Duration `mapstructure:"deadline" validate:"gt=0"`
	IdleTTL            time.Duration `mapstructure:"idle_ttl" validate:"gt=0"`
}

func DefaultEndpoint(kind string) EndpointConfig {
	concurrency := 128
	if kind == "storefront" {
		concurrency = 32
	}
	return EndpointConfig{
		ControllerSubject: "system:serviceaccount:gitstore-controller-manager",
		CORS:              CORSConfig{AllowedOrigins: []string{}, AllowedHeaders: []string{"Authorization", "Content-Type"}, ExposedHeaders: []string{"X-Request-Id"}, MaxAge: 10 * time.Minute},
		Limits:            GraphQLLimits{PrincipalPerSecond: 50, PrincipalBurst: 1000, BodyBytes: 1 << 20, DocumentBytes: 64 << 10, ParserTokens: 15000, Depth: 15, Aliases: 30, RootFields: 20, Directives: 100, FragmentVisits: 1000, ValidationErrors: 50, PageSize: 250, NodeIDs: 50, Complexity: 1000, InFlight: concurrency, LimiterKeys: 10000, WSConnections: 1000, WSOperations: 20, Deadline: 10 * time.Second, IdleTTL: 10 * time.Minute},
	}
}

func setEndpointDefaults(v *viper.Viper) {
	v.SetDefault("api.admin_port", 4000)
	v.SetDefault("api.storefront_port", 4001)
	v.SetDefault("api.storefront_rate_limit.per_second", 200)
	v.SetDefault("api.storefront_rate_limit.burst", 400)
	v.SetDefault("api.trusted_proxies", []string{})
	for _, kind := range []string{"admin", "storefront"} {
		defaults := DefaultEndpoint(kind)
		v.SetDefault("api."+kind+".controller_subject", defaults.ControllerSubject)
		for section, value := range map[string]any{"cors": defaults.CORS, "limits": defaults.Limits} {
			r := reflect.ValueOf(value)
			for i := 0; i < r.NumField(); i++ {
				v.SetDefault("api."+kind+"."+section+"."+r.Type().Field(i).Tag.Get("mapstructure"), r.Field(i).Interface())
			}
		}
	}
}

// ValidatePorts is also used when constructing a server directly without Load.
func (a ApiConfig) ValidatePorts() error {
	seen := make(map[int]string)
	for _, item := range []struct {
		name string
		port int
	}{{"admin_port", a.AdminPort}, {"storefront_port", a.StorefrontPort}, {"git_port", a.GitPort}, {"grpc_port", a.GrpcPort}} {
		if item.port < 1 || item.port > 65535 {
			return fmt.Errorf("api.%s must be between 1 and 65535", item.name)
		}
		if previous, ok := seen[item.port]; ok {
			return fmt.Errorf("api.%s collides with api.%s on port %d", item.name, previous, item.port)
		}
		seen[item.port] = item.name
	}
	return nil
}

func (a ApiConfig) ValidateRates() error {
	for _, r := range []RateLimitConfig{a.RateLimit, a.StorefrontRateLimit} {
		if math.IsInf(r.PerSecond, 0) || math.IsNaN(r.PerSecond) || r.PerSecond <= 0 || r.Burst <= 0 {
			return fmt.Errorf("endpoint rate limits must be positive and finite")
		}
	}
	return nil
}

func (c CORSConfig) Validate() error {
	if c.MaxAge < 0 || c.MaxAge > 24*time.Hour {
		return fmt.Errorf("cors.max_age must be between zero and 24h")
	}
	for _, origin := range c.AllowedOrigins {
		if origin == "*" {
			if c.AllowCredentials {
				return fmt.Errorf("cors wildcard origins cannot allow credentials")
			}
			continue
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return fmt.Errorf("cors.allowed_origins contains an invalid HTTP(S) origin")
		}
	}
	for _, headers := range [][]string{c.AllowedHeaders, c.ExposedHeaders} {
		for _, h := range headers {
			if !httpguts.ValidHeaderFieldName(h) {
				return fmt.Errorf("cors contains an invalid header name")
			}
		}
	}
	return nil
}

// Allows includes same-origin browser access and non-browser clients without an
// Origin header. Forwarded headers are not used to manufacture an origin.
func (c CORSConfig) Allows(origin, ownOrigin string) bool {
	if origin == "" || strings.EqualFold(strings.TrimSuffix(origin, "/"), ownOrigin) {
		return true
	}
	for _, allowed := range c.AllowedOrigins {
		if allowed == "*" || strings.EqualFold(strings.TrimSuffix(allowed, "/"), strings.TrimSuffix(origin, "/")) {
			return true
		}
	}
	return false
}
