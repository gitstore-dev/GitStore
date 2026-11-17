// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	RoleSmartHTTP  = "smart-http"
	RoleCatalog    = "catalog"
	RoleAdmin      = "admin"
	RoleStorefront = "storefront"
)

// DefaultRoles excludes Storefront until publication readers and trusted target
// binding exist. An explicit Storefront role serves only its unavailable boundary.
func DefaultRoles() []string { return []string{RoleSmartHTTP, RoleCatalog, RoleAdmin} }

func (a ApiConfig) HasRole(role string) bool {
	roles := a.Roles
	if roles == nil {
		roles = DefaultRoles()
	}
	return slices.Contains(roles, role)
}

func (a ApiConfig) ValidateRuntime() error {
	if a.Roles != nil && len(a.Roles) == 0 {
		return fmt.Errorf("api.roles must select at least one role")
	}
	seen := map[string]bool{}
	for _, role := range a.Roles {
		if !slices.Contains([]string{RoleSmartHTTP, RoleCatalog, RoleAdmin, RoleStorefront}, role) || seen[role] {
			return fmt.Errorf("api.roles contains an unknown or duplicate role %q", role)
		}
		seen[role] = true
	}
	if a.GraphQLLayout != "" && a.GraphQLLayout != "dedicated" && a.GraphQLLayout != "shared" {
		return fmt.Errorf("api.graphql_layout must be dedicated or shared")
	}
	return a.Advertised.Validate()
}

func (a ApiConfig) ValidateRoleBudgets() error {
	for role, budget := range map[string]RoleBudget{RoleSmartHTTP: a.SmartHTTP, RoleCatalog: a.Catalog} {
		if !a.HasRole(role) {
			continue
		}
		if budget.InFlight < 1 || budget.InFlight > 1024 || budget.Deadline <= 0 || budget.Deadline > 10*time.Minute {
			return fmt.Errorf("api.%s requires in_flight in [1,1024] and deadline in (0,10m]", role)
		}
	}
	return nil
}

// RoleBudget pools are separate from GraphQL and from outgoing Git RPCs.
// In particular, an originating Admin/Smart HTTP request must not consume
// the slot needed by its catalog callback.
type RoleBudget struct {
	BindHost string        `mapstructure:"bind_host"`
	InFlight int           `mapstructure:"in_flight"`
	Deadline time.Duration `mapstructure:"deadline"`
}

func (a ApiConfig) BindAddress(role string) string {
	var host string
	var port int
	switch role {
	case RoleAdmin:
		host, port = a.Admin.BindHost, a.AdminPort
	case RoleStorefront:
		host, port = a.Storefront.BindHost, a.StorefrontPort
	case RoleSmartHTTP:
		host, port = a.SmartHTTP.BindHost, a.GitPort
	case RoleCatalog:
		host, port = a.Catalog.BindHost, a.GrpcPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// AdvertisedEndpoints is deployment configuration, independent of local roles,
// binds and internal RPC targets. Empty URLs are deliberately undisclosed.
// Merely configuring a URL never authorizes forwarding credentials to its origin.
type AdvertisedEndpoints struct {
	Admin      AdvertisedEndpoint `mapstructure:"admin"`
	Storefront AdvertisedEndpoint `mapstructure:"storefront"`
	GitHTTP    AdvertisedEndpoint `mapstructure:"git_http"`
}

type AdvertisedEndpoint struct {
	URL      string `mapstructure:"url"`
	Disclose bool   `mapstructure:"disclose"`
}

func (a AdvertisedEndpoints) Validate() error {
	for name, endpoint := range map[string]AdvertisedEndpoint{"admin": a.Admin, "storefront": a.Storefront, "git_http": a.GitHTTP} {
		if endpoint.URL == "" {
			if endpoint.Disclose {
				return fmt.Errorf("api.advertised.%s.disclose requires a URL", name)
			}
			continue
		}
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Hostname() == "" || u.Opaque != "" || u.User != nil ||
			u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
			(u.Scheme != "https" && u.Scheme != "http") || strings.ContainsAny(endpoint.URL, " \t\r\n\\#") {
			return fmt.Errorf("api.advertised.%s.url must be an absolute HTTP(S) URL without credentials, query or fragment", name)
		}
		if port := u.Port(); port != "" {
			number, err := strconv.Atoi(port)
			if err != nil || number < 1 || number > 65535 {
				return fmt.Errorf("api.advertised.%s.url port must be in [1,65535]", name)
			}
		} else if strings.HasSuffix(u.Host, ":") {
			return fmt.Errorf("api.advertised.%s.url must not contain an empty port", name)
		}
		if name != "git_http" && !strings.HasSuffix(u.Path, "/"+name+"/graphql") {
			return fmt.Errorf("api.advertised.%s.url must include the complete /%s/graphql path", name, name)
		}
	}
	return nil
}

// Disclosed returns only explicitly disclosable client endpoints. This is a
// reusable policy boundary, not a discovery transport or a health report.
func (a AdvertisedEndpoints) Disclosed() map[string]string {
	result := map[string]string{}
	for name, endpoint := range map[string]AdvertisedEndpoint{"admin": a.Admin, "storefront": a.Storefront, "git_http": a.GitHTTP} {
		if endpoint.Disclose && endpoint.URL != "" {
			result[name] = endpoint.URL
		}
	}
	return result
}

// Digest lets operators compare stable deployment configuration across replicas
// without printing private endpoint URLs. It does not include local roles.
func (a AdvertisedEndpoints) Digest() string {
	value, _ := json.Marshal(a)
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
