// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// gRPC client — connects to gitstore-git-service via the gitstore.git.v1 contract.

package gitclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"

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

// NewClientWithAddr dials the given address with an HMAC bearer token for inter-service auth.
func NewClientWithAddr(addr, hmacSecret string) (*Client, error) {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(hmacCreds{token: hmacSecret}),
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

// InfoRefs returns the ref advertisement bytes and service for the given repository.
func (c *Client) InfoRefs(ctx context.Context, repoID string, service gitv1.Service) ([]byte, gitv1.Service, error) {
	resp, err := c.Git.InfoRefs(ctx, &gitv1.InfoRefsRequest{
		RepositoryId: repoID,
		Service:      service,
	})
	if err != nil {
		return nil, gitv1.Service_SERVICE_UNSPECIFIED, err
	}
	return resp.Advertisement, resp.Service, nil
}

// UploadPack sends the want/have body and returns an io.Reader streaming pack response chunks.
func (c *Client) UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error) {
	stream, err := c.Git.UploadPack(ctx, &gitv1.UploadPackRequest{
		RepositoryId: repoID,
		Body:         body,
	})
	if err != nil {
		return nil, err
	}
	return &UploadPackReceiver{stream: stream}, nil
}

// ReceivePack streams the pack body to the git service and returns the report-status payload.
// The receive-pack body starts with pkt-line ref-update commands followed by a flush packet
// (0000), then raw PACK data. This function parses those ref commands and sends them in the
// first gRPC chunk alongside any initial pack bytes.
func (c *Client) ReceivePack(ctx context.Context, repoID string, body io.Reader) ([]byte, error) {
	stream, err := c.Git.ReceivePack(ctx)
	if err != nil {
		return nil, err
	}

	br := bufio.NewReader(body)
	refCmds, err := parsePktLineRefCommands(br)
	if err != nil {
		return nil, fmt.Errorf("parse ref commands: %w", err)
	}

	const chunkSize = 64 * 1024
	buf := make([]byte, chunkSize)
	first := true
	for {
		n, readErr := br.Read(buf)
		if n > 0 {
			var chunk *gitv1.ReceivePackRequest
			if first {
				chunk = &gitv1.ReceivePackRequest{
					RepositoryId: repoID,
					RefCommands:  refCmds,
					PackData:     buf[:n],
					IsLast:       readErr == io.EOF,
				}
				first = false
			} else {
				chunk = &gitv1.ReceivePackRequest{
					PackData: buf[:n],
					IsLast:   readErr == io.EOF,
				}
			}
			if err := stream.Send(chunk); err != nil {
				return nil, fmt.Errorf("send chunk: %w", err)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read body: %w", readErr)
		}
	}

	// If no pack data was read (e.g. delete-only push), send the first chunk with just ref commands.
	if first {
		if err := stream.Send(&gitv1.ReceivePackRequest{
			RepositoryId: repoID,
			RefCommands:  refCmds,
			IsLast:       true,
		}); err != nil {
			return nil, fmt.Errorf("send ref-only chunk: %w", err)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("close and recv: %w", err)
	}
	return resp.ReportStatus, nil
}

// parsePktLineRefCommands reads pkt-line ref-update commands from the receive-pack body
// up to and including the flush packet (0000), returning parsed RefCommand protos.
func parsePktLineRefCommands(r *bufio.Reader) ([]*gitv1.RefCommand, error) {
	var cmds []*gitv1.RefCommand
	for {
		// Read the 4-byte hex length prefix.
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return nil, fmt.Errorf("read pkt-len: %w", err)
		}
		length, err := strconv.ParseInt(string(lenBuf), 16, 32)
		if err != nil {
			return nil, fmt.Errorf("parse pkt-len %q: %w", lenBuf, err)
		}
		if length == 0 {
			// Flush packet — end of ref commands.
			break
		}
		dataLen := int(length) - 4
		if dataLen < 0 {
			return nil, fmt.Errorf("invalid pkt-line length %d", length)
		}
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, fmt.Errorf("read pkt-line data: %w", err)
		}
		// Strip trailing newline.
		data = bytes.TrimRight(data, "\n")
		// First line may have NUL-separated capabilities after the ref info; strip them.
		if idx := bytes.IndexByte(data, 0); idx >= 0 {
			data = data[:idx]
		}
		// Format: "<old-oid> <new-oid> <refname>"
		parts := strings.SplitN(string(data), " ", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed ref command: %q", data)
		}
		oldOID, newOID, refName := parts[0], parts[1], parts[2]
		// Validate: must be 40-char hex or zero OID.
		if err := validateOID(oldOID); err != nil {
			return nil, fmt.Errorf("old_oid %q: %w", oldOID, err)
		}
		if err := validateOID(newOID); err != nil {
			return nil, fmt.Errorf("new_oid %q: %w", newOID, err)
		}
		cmds = append(cmds, &gitv1.RefCommand{
			OldOid:  oldOID,
			NewOid:  newOID,
			RefName: refName,
		})
	}
	return cmds, nil
}

// validateOID checks that s is a 40-character lowercase hex string.
func validateOID(s string) error {
	if len(s) != 40 {
		return fmt.Errorf("must be 40 hex chars, got %d", len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return err
	}
	return nil
}
