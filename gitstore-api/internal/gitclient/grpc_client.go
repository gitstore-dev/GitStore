// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC client — connects to gitstore-git-service via the gitstore.git.v1 contract.

package gitclient

import (
	"fmt"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Client wraps the generated GitServiceClient with connection lifecycle management.
// RepositoryID is the target repository for all RPC calls; set before use.
type Client struct {
	conn         *grpc.ClientConn
	Git          gitv1.GitServiceClient
	RepositoryID string
}

// NewClientWithAddr dials the given address directly.
func NewClientWithAddr(addr string) (*Client, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(grpcprom.UnaryClientInterceptor),
		grpc.WithStreamInterceptor(grpcprom.StreamClientInterceptor),
	}
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc.NewClient(%s): %w", addr, err)
	}
	return &Client{
		conn: conn,
		Git:  gitv1.NewGitServiceClient(conn),
	}, nil
}

// Close closes the underlying gRPC connection.
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
