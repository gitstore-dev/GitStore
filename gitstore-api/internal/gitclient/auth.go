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

func (h hmacCreds) RequireTransportSecurity() bool { return false }
