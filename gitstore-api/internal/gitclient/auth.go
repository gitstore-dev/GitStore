// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import "context"

// hmacCreds injects a Bearer token into every outbound gRPC call's metadata.
// It implements google.golang.org/grpc/credentials.PerRPCCredentials.
type hmacCreds struct {
	token string
}

func (h hmacCreds) GetRequestMetadata(_ context.Context, _ ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + h.token}, nil
}

// RequireTransportSecurity returns false because the API→git-service channel is
// on an internal network (Docker bridge / Kubernetes pod network) with no
// user-facing exposure. TLS can be added in a future phase without code changes
// here — flip to true and add grpc.WithTransportCredentials to grpc_client.go.
func (h hmacCreds) RequireTransportSecurity() bool { return false }
