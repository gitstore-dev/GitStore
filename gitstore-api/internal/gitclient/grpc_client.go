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
	"sync"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	grpcprom "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

// Client wraps the generated GitServiceClient with connection lifecycle management.
// RepositoryID is the target repository for all RPC calls; set before use.
type Client struct {
	conn         *grpc.ClientConn
	Git          gitv1.GitServiceClient
	RepositoryID string
	Registry     *auth.ProviderRegistry
	Store        interface {
		GetRepository(context.Context, string) (*datastore.Repository, error)
	}
}

var enableRPCDurationMetrics sync.Once

var fileReadPhases = [...]struct{ phase, header string }{
	{"authentication", "x-gitstore-file-read-authentication-ns"},
	{"repository_lock", "x-gitstore-file-read-repository-lock-ns"},
	{"blocking_queue", "x-gitstore-file-read-blocking-queue-ns"},
	{"payload", "x-gitstore-file-read-payload-ns"},
	{"history_commit", "x-gitstore-file-read-history-commit-ns"},
	{"history_tree", "x-gitstore-file-read-history-tree-ns"},
	{"repository_open", "x-gitstore-file-read-repository-open-ns"},
	{"revision", "x-gitstore-file-read-revision-ns"},
	{"target_tree", "x-gitstore-file-read-target-tree-ns"},
	{"blob", "x-gitstore-file-read-blob-ns"},
}

const fileReadHistoryCommitsHeader = "x-gitstore-file-read-history-commits"

var fileReadPhaseSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "gitstore_git_file_read_server_phase_seconds",
	Help:    "Successful GetFile/GetFiles server phase latency reported through response metadata.",
	Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
}, []string{"phase", "mode"})

var fileReadPhaseReports = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "gitstore_git_file_read_phase_reports_total",
	Help: "GetFile/GetFiles phase metadata observation outcomes.",
}, []string{"result"})

var fileReadHistoryCommits = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "gitstore_git_file_read_history_commits",
	Help:    "Commit count in successful unchanged-file history proofs.",
	Buckets: []float64{1, 4, 16, 64, 256, 1024, 4097},
})

func fileReadPhaseInterceptor(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
	if method != gitv1.GitService_GetFile_FullMethodName && method != gitv1.GitService_GetFiles_FullMethodName {
		return invoker(ctx, method, req, reply, cc, opts...)
	}
	var headers metadata.MD
	var callerHeaders *metadata.MD
	for _, option := range opts {
		if header, ok := option.(grpc.HeaderCallOption); ok {
			callerHeaders = header.HeaderAddr
		}
	}
	if callerHeaders == nil {
		opts = append(opts, grpc.Header(&headers))
	}
	err := invoker(ctx, method, req, reply, cc, opts...)
	if callerHeaders != nil {
		headers = *callerHeaders
	}
	result := "rpc_error"
	if err == nil {
		mode := "content"
		if method == gitv1.GitService_GetFiles_FullMethodName {
			mode = "batch_content"
		}
		if request, ok := req.(*gitv1.GetFileRequest); ok && request.GetUnchangedSinceCommit() != "" {
			mode = "history_proof"
		}
		var durations [len(fileReadPhases)]float64
		var commits uint64
		missing := 0
		result = "observed"
		for i, phase := range fileReadPhases {
			values := headers.Get(phase.header)
			if len(values) == 0 {
				missing++
				continue
			}
			if len(values) != 1 {
				result = "invalid"
				break
			}
			nanoseconds, parseErr := strconv.ParseUint(values[0], 10, 63)
			if parseErr != nil {
				result = "invalid"
				break
			}
			durations[i] = float64(nanoseconds) / 1e9
		}
		values := headers.Get(fileReadHistoryCommitsHeader)
		if len(values) == 0 {
			missing++
		} else if len(values) != 1 {
			result = "invalid"
		} else {
			var parseErr error
			commits, parseErr = strconv.ParseUint(values[0], 10, 63)
			if parseErr != nil || commits > 4097 || (mode != "history_proof" && commits != 0) || (mode == "history_proof" && commits == 0) {
				result = "invalid"
			}
		}
		if result == "observed" {
			switch missing {
			case 0:
				for i, phase := range fileReadPhases {
					fileReadPhaseSeconds.WithLabelValues(phase.phase, mode).Observe(durations[i])
				}
				if mode == "history_proof" {
					fileReadHistoryCommits.Observe(float64(commits))
				}
			case len(fileReadPhases) + 1:
				result = "missing"
			default:
				result = "invalid"
			}
		}
	}
	fileReadPhaseReports.WithLabelValues(result).Inc()
	return err
}

// NewClientWithAddr dials with signed, expiring, single-use inter-service requests.
func NewClientWithAddr(addr, hmacSecret string, options ...grpc.DialOption) (*Client, error) {
	if strings.TrimSpace(hmacSecret) == "" {
		return nil, fmt.Errorf("git RPC signing key is required")
	}
	enableRPCDurationMetrics.Do(func() {
		grpcprom.EnableClientHandlingTimeHistogram()
		prometheus.MustRegister(fileReadPhaseSeconds, fileReadPhaseReports, fileReadHistoryCommits)
	})
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDisableRetry(),
		grpc.WithChainUnaryInterceptor(grpcprom.UnaryClientInterceptor, fileReadPhaseInterceptor),
		grpc.WithStreamInterceptor(grpcprom.StreamClientInterceptor),
	}
	opts = append(opts, options...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, fmt.Errorf("grpc.NewClient(%s): %w", addr, err)
	}
	return &Client{
		conn: conn,
		Git:  gitv1.NewGitServiceClient(signedConn{ClientConnInterface: conn, secret: hmacSecret}),
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
	action := "repository.contents.read"
	if service == gitv1.Service_SERVICE_GIT_RECEIVE_PACK {
		action = "repository.contents.write"
	}
	authorization, err := c.authorizeRequest(ctx, action, repoID)
	if err != nil {
		return nil, gitv1.Service_SERVICE_UNSPECIFIED, err
	}
	resp, err := c.Git.InfoRefs(ctx, &gitv1.InfoRefsRequest{
		RepositoryId:  repoID,
		Service:       service,
		Authorization: authorization,
	})
	if err != nil {
		return nil, gitv1.Service_SERVICE_UNSPECIFIED, err
	}
	return resp.Advertisement, resp.Service, nil
}

// UploadPack sends the want/have body and returns an io.Reader streaming pack response chunks.
func (c *Client) UploadPack(ctx context.Context, repoID string, body []byte) (io.Reader, error) {
	authorization, err := c.authorizeRequest(ctx, "repository.contents.read", repoID)
	if err != nil {
		return nil, err
	}
	stream, err := c.Git.UploadPack(ctx, &gitv1.UploadPackRequest{
		RepositoryId:  repoID,
		Body:          body,
		Authorization: authorization,
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
	authorization, err := c.authorizeRequest(ctx, auth.ActionRepositoryContentsWrite, repoID)
	if err != nil {
		return nil, err
	}
	// Copy the request snapshot; never mutate a context shared by other work.
	previous := PushContextFromContext(ctx)
	if previous == nil || previous.Authorization == nil {
		return nil, fmt.Errorf("receive-pack requires approved request authorization")
	}
	pushCtx := proto.Clone(previous).(*gitv1.PushContext)
	pushCtx.Authorization = authorization
	br := bufio.NewReader(body)
	refCmds, shallowOIDs, err := parsePktLineRefCommands(br)
	if err != nil {
		return nil, fmt.Errorf("parse ref commands: %w", err)
	}
	stream, err := c.Git.ReceivePack(ctx)
	if err != nil {
		return nil, err
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
					PushContext:  pushCtx,
					ShallowOids:  shallowOIDs,
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
			PushContext:  pushCtx,
			ShallowOids:  shallowOIDs,
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
// up to and including the flush packet (0000), preserving preceding shallow boundaries.
func parsePktLineRefCommands(r *bufio.Reader) ([]*gitv1.RefCommand, []string, error) {
	const maxHeaderBytes = 4 * 1024 * 1024
	const maxShallowCommits = 4096
	var cmds []*gitv1.RefCommand
	var shallowOIDs []string
	seenShallow := make(map[string]struct{})
	headerBytes := 0
	for {
		// Read the 4-byte hex length prefix.
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(r, lenBuf); err != nil {
			return nil, nil, fmt.Errorf("read pkt-len: %w", err)
		}
		length, err := strconv.ParseInt(string(lenBuf), 16, 32)
		if err != nil {
			return nil, nil, fmt.Errorf("parse pkt-len %q: %w", lenBuf, err)
		}
		if length == 0 {
			// Flush packet — end of ref commands.
			break
		}
		if length < 4 || length > 65520 {
			return nil, nil, fmt.Errorf("invalid pkt-line length %d", length)
		}
		headerBytes += int(length)
		if headerBytes > maxHeaderBytes {
			return nil, nil, fmt.Errorf("ref command header exceeds %d bytes", maxHeaderBytes)
		}
		dataLen := int(length) - 4
		data := make([]byte, dataLen)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, nil, fmt.Errorf("read pkt-line data: %w", err)
		}
		// Strip trailing newline.
		data = bytes.TrimRight(data, "\n")
		if bytes.HasPrefix(data, []byte("shallow ")) {
			if len(cmds) != 0 {
				return nil, nil, fmt.Errorf("shallow boundaries must precede ref commands")
			}
			oid := string(data[len("shallow "):])
			if err := validateOID(oid); err != nil {
				return nil, nil, fmt.Errorf("invalid shallow commit: %w", err)
			}
			if oid == strings.Repeat("0", 40) || strings.ToLower(oid) != oid {
				return nil, nil, fmt.Errorf("shallow commit must be nonzero lowercase hex")
			}
			if _, duplicate := seenShallow[oid]; duplicate {
				return nil, nil, fmt.Errorf("duplicate shallow commit")
			}
			if len(shallowOIDs) == maxShallowCommits {
				return nil, nil, fmt.Errorf("shallow boundaries exceed %d commits", maxShallowCommits)
			}
			seenShallow[oid] = struct{}{}
			shallowOIDs = append(shallowOIDs, oid)
			continue
		}
		// First line may have NUL-separated capabilities after the ref info; strip them.
		if idx := bytes.IndexByte(data, 0); idx >= 0 {
			data = data[:idx]
		}
		// Format: "<old-oid> <new-oid> <refname>"
		parts := strings.SplitN(string(data), " ", 3)
		if len(parts) != 3 {
			return nil, nil, fmt.Errorf("malformed ref command: %q", data)
		}
		oldOID, newOID, refName := parts[0], parts[1], parts[2]
		// Validate: must be 40-char hex or zero OID.
		if err := validateOID(oldOID); err != nil {
			return nil, nil, fmt.Errorf("old_oid %q: %w", oldOID, err)
		}
		if err := validateOID(newOID); err != nil {
			return nil, nil, fmt.Errorf("new_oid %q: %w", newOID, err)
		}
		cmds = append(cmds, &gitv1.RefCommand{
			OldOid:  oldOID,
			NewOid:  newOID,
			RefName: refName,
		})
	}
	return cmds, shallowOIDs, nil
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
