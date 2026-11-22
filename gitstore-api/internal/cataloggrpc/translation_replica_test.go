// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package cataloggrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	catalogv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/catalog/v1"
	"github.com/gitstore-dev/gitstore/api/internal/auth"
	"github.com/gitstore-dev/gitstore/api/internal/datastore"
	"github.com/gitstore-dev/gitstore/translation"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func translationCallbackFaultInterceptor(upstream *grpc.ClientConn, fault func() (string, error),
	record func(string, bool, *catalogv1.AdmitResourcesRequest) error) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		var reply any
		switch info.FullMethod {
		case catalogv1.CatalogService_ValidateResources_FullMethodName:
			reply = &catalogv1.ValidateResourcesResponse{}
		case catalogv1.CatalogService_ValidateRBACReceive_FullMethodName:
			reply = &catalogv1.ValidateRBACReceiveResponse{}
		case catalogv1.CatalogService_ValidateResourceDeletions_FullMethodName:
			reply = &catalogv1.ValidateResourceDeletionsResponse{}
		case catalogv1.CatalogService_AdmitResources_FullMethodName:
			reply = &catalogv1.AdmitResourcesResponse{}
		default:
			return nil, status.Error(codes.Unimplemented, "unrecognized owned callback method")
		}
		stage := ""
		if info.FullMethod == catalogv1.CatalogService_AdmitResources_FullMethodName {
			var err error
			stage, err = fault()
			if err != nil {
				return nil, status.Error(codes.Unavailable, "owned callback fault control unavailable")
			}
			if stage != "" && stage != "callback-loss" && stage != "reply-loss" {
				return nil, status.Error(codes.FailedPrecondition, "unrecognized owned callback fault")
			}
			if stage == "callback-loss" {
				if err := record(stage, false, request.(*catalogv1.AdmitResourcesRequest)); err != nil {
					return nil, status.Error(codes.Unavailable, "owned callback fault receipt unavailable")
				}
				return nil, status.Error(codes.Unavailable, "simulated callback transport loss")
			}
		}
		headers, _ := metadata.FromIncomingContext(ctx)
		ctx = metadata.NewOutgoingContext(ctx, headers.Copy())
		err := upstream.Invoke(ctx, info.FullMethod, request, reply)
		if err == nil && info.FullMethod == catalogv1.CatalogService_AdmitResources_FullMethodName {
			if err := record(stage, true, request.(*catalogv1.AdmitResourcesRequest)); err != nil {
				return nil, status.Error(codes.Unavailable, "owned callback fault receipt unavailable")
			}
			if stage == "reply-loss" {
				return nil, status.Error(codes.Unavailable, "simulated callback reply loss")
			}
		}
		return reply, err
	}
}

func TestTranslationCallbackFaultsPreserveValidationAndSignedRequests(t *testing.T) {
	var calls atomic.Int32
	var selected atomic.Int32
	var controlFailure, recordingFailure, upstreamFailure atomic.Bool
	deliveries := make(chan bool, 4)
	request := &catalogv1.AdmitResourcesRequest{RepositoryId: uuid.NewString(), NewCommitSha: strings.Repeat("a", 40)}
	start := func(interceptor grpc.UnaryServerInterceptor) *grpc.ClientConn {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		server := grpc.NewServer(grpc.UnaryInterceptor(interceptor))
		catalogv1.RegisterCatalogServiceServer(server, &catalogv1.UnimplementedCatalogServiceServer{})
		t.Cleanup(server.Stop)
		go func() { _ = server.Serve(listener) }()
		connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		return connection
	}
	upstream := start(func(ctx context.Context, received any, info *grpc.UnaryServerInfo, _ grpc.UnaryHandler) (any, error) {
		headers, _ := metadata.FromIncomingContext(ctx)
		if strings.Join(headers.Get(auth.RPCSignatureMetadata), "") != "unchanged-signature" {
			return nil, status.Error(codes.Unauthenticated, "signature metadata changed in transit")
		}
		switch info.FullMethod {
		case catalogv1.CatalogService_AdmitResources_FullMethodName:
			if !proto.Equal(request, received.(proto.Message)) {
				return nil, status.Error(codes.DataLoss, "signed request changed in transit")
			}
			if upstreamFailure.Load() {
				return nil, status.Error(codes.PermissionDenied, "upstream admission denied")
			}
			calls.Add(1)
			return &catalogv1.AdmitResourcesResponse{}, nil
		case catalogv1.CatalogService_ValidateResources_FullMethodName:
			return &catalogv1.ValidateResourcesResponse{}, nil
		case catalogv1.CatalogService_ValidateRBACReceive_FullMethodName:
			return &catalogv1.ValidateRBACReceiveResponse{}, nil
		case catalogv1.CatalogService_ValidateResourceDeletions_FullMethodName:
			return &catalogv1.ValidateResourceDeletionsResponse{}, nil
		default:
			return nil, status.Error(codes.Unimplemented, "unrecognized test method")
		}
	})
	proxy := start(translationCallbackFaultInterceptor(upstream, func() (string, error) {
		if controlFailure.Load() {
			return "", errors.New("control unavailable")
		}
		return []string{"", "callback-loss", "reply-loss", "invalid"}[selected.Load()], nil
	}, func(_ string, forwarded bool, received *catalogv1.AdmitResourcesRequest) error {
		if recordingFailure.Load() {
			return errors.New("receipt unavailable")
		}
		if !proto.Equal(request, received) {
			return errors.New("fault receipt does not identify the original request")
		}
		deliveries <- forwarded
		return nil
	}))
	client := catalogv1.NewCatalogServiceClient(proxy)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(auth.RPCSignatureMetadata, "unchanged-signature"))
	selected.Store(1)
	_, err := client.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{})
	require.NoError(t, err, "callback injection cannot disable pre-receive validation")
	_, err = client.ValidateRBACReceive(ctx, &catalogv1.ValidateRBACReceiveRequest{})
	require.NoError(t, err)
	_, err = client.ValidateResourceDeletions(ctx, &catalogv1.ValidateResourceDeletionsRequest{})
	require.NoError(t, err)
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.Zero(t, calls.Load(), "callback loss must occur before upstream admission")
	require.False(t, <-deliveries)
	selected.Store(2)
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	require.EqualValues(t, 1, calls.Load(), "reply loss must follow actual upstream admission")
	require.True(t, <-deliveries)
	selected.Store(0)
	_, err = client.AdmitResources(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 2, calls.Load(), "restoration must allow ordinary idempotent retry")
	require.True(t, <-deliveries)
	selected.Store(3)
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	controlFailure.Store(true)
	_, err = client.ValidateResources(ctx, &catalogv1.ValidateResourcesRequest{})
	require.NoError(t, err, "invalid fault control cannot disable pre-receive validation")
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	controlFailure.Store(false)
	selected.Store(1)
	recordingFailure.Store(true)
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.Unavailable, status.Code(err))
	recordingFailure.Store(false)
	selected.Store(2)
	upstreamFailure.Store(true)
	_, err = client.AdmitResources(ctx, request)
	require.Equal(t, codes.PermissionDenied, status.Code(err), "reply loss cannot hide an actual upstream rejection")
	require.EqualValues(t, 2, calls.Load(), "failed controls cannot admit a request")
	require.Empty(t, deliveries, "failed injection cannot fabricate a successful fault receipt")
}

func translationCallbackProxyDuration(value string) (time.Duration, error) {
	if value == "" {
		return 5 * time.Minute, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 || duration > 70*time.Minute {
		return 0, errors.New("owned callback proxy requires a positive lifetime of at most seventy minutes")
	}
	return duration, nil
}

func TestTranslationCallbackProxyDuration(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 5 * time.Minute},
		{"30m", 30 * time.Minute},
		{"70m", 70 * time.Minute},
	} {
		duration, err := translationCallbackProxyDuration(tc.value)
		require.NoError(t, err)
		require.Equal(t, tc.want, duration)
	}
	for _, value := range []string{"0s", "-1m", "71m", "unbounded"} {
		_, err := translationCallbackProxyDuration(value)
		require.Error(t, err, "proxy lifetime must remain explicitly bounded")
	}
}

func TestTranslationNativeCallbackTransportProxy(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CALLBACK_PROXY") != "1" {
		t.Skip("owned callback transport fault proxy not selected")
	}
	require.Equal(t, "1", os.Getenv("TRANSLATION_TEST_OWNED_DEPLOYMENT"))
	duration, err := translationCallbackProxyDuration(os.Getenv("TRANSLATION_TEST_CALLBACK_PROXY_DURATION"))
	require.NoError(t, err)
	runID := os.Getenv("TRANSLATION_TEST_CALLBACK_RUN_ID")
	require.Regexp(t, "^[a-z0-9][a-z0-9-]{0,39}$", runID)
	listen, target := os.Getenv("TRANSLATION_TEST_CALLBACK_LISTEN"), os.Getenv("TRANSLATION_TEST_CALLBACK_UPSTREAM")
	require.NotEqual(t, listen, target)
	for _, address := range []string{listen, target} {
		host, _, err := net.SplitHostPort(address)
		require.NoError(t, err)
		require.True(t, net.ParseIP(host).IsLoopback(), "owned native proxy accepts only explicit loopback endpoints")
	}
	root := os.Getenv("TRANSLATION_TEST_CALLBACK_EVIDENCE_DIR")
	require.True(t, filepath.IsAbs(root))
	require.NoError(t, os.Mkdir(root, 0o700), "select a fresh private proxy evidence directory")
	write := func(name string, value any) error {
		file, err := os.CreateTemp(root, ".pending-")
		if err != nil {
			return err
		}
		defer os.Remove(file.Name())
		writeErr := json.NewEncoder(file).Encode(value)
		syncErr := file.Sync()
		if err := errors.Join(writeErr, syncErr, file.Close()); err != nil {
			return err
		}
		return os.Link(file.Name(), filepath.Join(root, name))
	}
	read := func(name string) (string, bool, error) {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 1024 {
			return "", false, errors.New("fault control must be a protected bounded regular file")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", false, err
		}
		var control struct{ RunID, Stage string }
		if json.Unmarshal(raw, &control) != nil || control.RunID != runID {
			return "", false, errors.New("fault control belongs to another run")
		}
		return control.Stage, true, nil
	}
	upstream, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = upstream.Close() })
	var sequence atomic.Int32
	interceptor := translationCallbackFaultInterceptor(upstream, func() (string, error) {
		stage, _, err := read("fault.json")
		return stage, err
	}, func(stage string, completed bool, request *catalogv1.AdmitResourcesRequest) error {
		index := sequence.Add(1)
		if index > 256 {
			return errors.New("owned fault receipt budget exhausted")
		}
		kind := "simulated"
		if stage == "" {
			kind = "forwarded"
		}
		return write(fmt.Sprintf("intercepted-%03d.json", index), map[string]any{
			"schemaVersion": 1, "runId": runID, "kind": kind, "stage": stage,
			"at": time.Now().UTC(), "upstreamCompleted": completed,
			"repositoryID": request.RepositoryId, "ref": request.RefName,
			"oldCommit": request.OldCommitSha, "newCommit": request.NewCommitSha,
		})
	})
	listener, err := net.Listen("tcp", listen)
	require.NoError(t, err)
	server := grpc.NewServer(grpc.UnaryInterceptor(interceptor), grpc.MaxRecvMsgSize(32<<20))
	catalogv1.RegisterCatalogServiceServer(server, &catalogv1.UnimplementedCatalogServiceServer{})
	t.Cleanup(server.Stop)
	failures := make(chan error, 1)
	go func() { failures <- server.Serve(listener) }()
	require.NoError(t, write("ready.json", map[string]any{
		"schemaVersion": 1, "runId": runID, "listen": listener.Addr().String(), "upstream": target,
		"scope": "owned-callback-fault-proxy", "evidenceClass": "diagnostic",
	}))
	timer := time.NewTimer(duration)
	defer timer.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-poll.C:
			_, stop, err := read("stop.json")
			require.NoError(t, err)
			if stop {
				require.NoError(t, write("stopped.json", map[string]any{"runId": runID, "at": time.Now().UTC(), "interceptions": sequence.Load()}))
				return
			}
		case err := <-failures:
			require.NoError(t, err)
			t.Fatal("owned callback proxy stopped without restoration coordination")
		case <-timer.C:
			t.Fatal("owned callback proxy exceeded its configured coordination deadline")
		}
	}
}

func TestTranslationNativeCallbackDelivery(t *testing.T) {
	if os.Getenv("TRANSLATION_TEST_CALLBACK_RUN") != "1" {
		t.Skip("owned native callback delivery not selected")
	}
	require.Equal(t, "1", os.Getenv("TRANSLATION_TEST_OWNED_DEPLOYMENT"))
	readPrivate := func(name string) []byte {
		filename := os.Getenv(name)
		info, err := os.Stat(filename)
		require.NoError(t, err)
		require.True(t, info.Mode().IsRegular())
		require.Zero(t, info.Mode().Perm()&0o077)
		require.LessOrEqual(t, info.Size(), int64(64<<10))
		raw, err := os.ReadFile(filename)
		require.NoError(t, err)
		return raw
	}
	var input struct {
		Namespace string
		Requests  []json.RawMessage
	}
	require.NoError(t, json.Unmarshal(readPrivate("TRANSLATION_TEST_CALLBACK_FILE"), &input))
	require.True(t, strings.HasPrefix(input.Namespace, "translation-"))
	require.Len(t, input.Requests, 2)
	var requests [2]catalogv1.AdmitResourcesRequest
	for index, raw := range input.Requests {
		request := &requests[index]
		require.NoError(t, protojson.Unmarshal(raw, request))
		require.Equal(t, "refs/heads/main", request.RefName)
		require.Len(t, request.ChangedPaths, 1)
		require.True(t, strings.HasPrefix(request.ChangedPaths[0], "translations/cpd-"))
		require.NotNil(t, request.Authorization)
		require.Equal(t, request.RepositoryId, request.Authorization.RepositoryId)
		require.Len(t, request.Authorization.Refs, 1)
		require.Equal(t, request.RefName, request.Authorization.Refs[0].RefName)
		require.Equal(t, request.OldCommitSha, request.Authorization.Refs[0].OldCommitSha)
		require.Equal(t, request.NewCommitSha, request.Authorization.Refs[0].NewCommitSha)
		require.Regexp(t, "^[a-f0-9]{40}$", request.OldCommitSha)
		require.Regexp(t, "^[a-f0-9]{40}$", request.NewCommitSha)
		_, err := uuid.Parse(request.RepositoryId)
		require.NoError(t, err, "signed RPCs use storage UUIDs, not GraphQL global IDs")
	}
	require.Equal(t, requests[0].RepositoryId, requests[1].RepositoryId)
	require.Equal(t, requests[0].ChangedPaths, requests[1].ChangedPaths)
	require.Equal(t, requests[0].NewCommitSha, requests[1].OldCommitSha)
	require.NotEqual(t, requests[0].NewCommitSha, requests[1].NewCommitSha)
	secret := strings.TrimSpace(string(readPrivate("TRANSLATION_TEST_RPC_SECRET_FILE")))
	require.NotEmpty(t, secret)
	endpoints := [2]string{os.Getenv("TRANSLATION_TEST_CATALOG_A"), os.Getenv("TRANSLATION_TEST_CATALOG_B")}
	require.NotEqual(t, endpoints[0], endpoints[1])
	var clients [2]catalogv1.CatalogServiceClient
	for index, endpoint := range endpoints {
		_, _, err := net.SplitHostPort(endpoint)
		require.NoError(t, err)
		connection, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { _ = connection.Close() })
		clients[index] = catalogv1.NewCatalogServiceClient(connection)
	}
	t.Run("concurrent-old-and-current", func(t *testing.T) {
		for replica, client := range clients {
			for index := range requests {
				t.Run(fmt.Sprintf("api-%d/revision-%d", replica, index), func(t *testing.T) {
					t.Parallel()
					request := &requests[index]
					signature, err := auth.SignRPC(secret, "gitstore-api", catalogv1.CatalogService_AdmitResources_FullMethodName, request)
					require.NoError(t, err)
					ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
					defer cancel()
					ctx = metadata.NewOutgoingContext(ctx, metadata.Pairs(auth.RPCSignatureMetadata, signature))
					_, err = client.AdmitResources(ctx, request)
					require.NoError(t, err)
				})
			}
		}
	})
}

func TestTranslationReplicaCallbacksPreserveAcceptedSourceAcrossReplacement(t *testing.T) {
	first := newTranslationCallbackReplica(t, newTranslationCallbackFixture(t))
	second := newTranslationCallbackReplica(t, first)
	target := &datastore.Product{
		APIVersion: "catalog.gitstore.dev/v1beta1", Kind: "Product", Namespace: "gitstore", Name: "laptop",
		UID: uuid.NewString(), ResourceVersion: "1", Generation: 1, CreationTimestamp: first.admCtx.Now,
		Spec: []byte(`{"Title":"Canonical","Options":[]}`),
	}
	require.NoError(t, first.store.CreateProduct(first.ctx, target))
	before, err := first.store.GetProduct(first.ctx, target.UID)
	require.NoError(t, err)
	zero, initialCommit := strings.Repeat("0", 40), first.admCtx.CommitSHA

	run := func(replicas []*translationProjectionFixture, old, next string) {
		t.Helper()
		start := make(chan struct{})
		errors := make(chan error, len(replicas))
		for _, replica := range replicas {
			go func() {
				<-start
				errors <- replica.callback(t, old, next, replica.entry.path)
			}()
		}
		close(start)
		var retry bool
		for range replicas {
			if err := <-errors; err != nil {
				require.True(t, strings.Contains(err.Error(), datastore.ErrAlreadyExists.Error()) ||
					strings.Contains(err.Error(), datastore.ErrConflict.Error()), "only CAS losers may need callback retry: %v", err)
				retry = true
			}
		}
		if retry {
			// A quiesced retry of the same accepted callback must be idempotent.
			for _, replica := range replicas {
				require.NoError(t, replica.callback(t, old, next, replica.entry.path))
			}
		}
	}
	run([]*translationProjectionFixture{first, second, first, second}, zero, initialCommit)
	accepted := first.source(t)
	require.EqualValues(t, 1, accepted.Generation)
	require.Equal(t, first.entry.parsed.RawSource, accepted.RawSource)
	require.Equal(t, string(first.entry.body), accepted.Body)
	require.Equal(t, accepted, second.source(t))

	newCommit := strings.Repeat("b", 40)
	next := mustParsedEntry(t, first.entry.path, strings.Replace(string(first.entry.parsed.RawSource), "Tragbarer Computer", "Replica accepted source", 1))
	first.git.trees[newCommit] = map[string][]byte{next.path: next.parsed.RawSource}
	first.git.tip = newCommit
	replacement := newTranslationCallbackReplica(t, first)
	run([]*translationProjectionFixture{first, replacement, first, replacement}, initialCommit, newCommit)
	current := replacement.source(t)
	require.Equal(t, accepted.UID, current.UID)
	require.EqualValues(t, 2, current.Generation)
	require.Equal(t, next.parsed.RawSource, current.RawSource)
	require.Equal(t, newCommit, current.GitCommitSHA)

	// Deliver both stale and current signed callbacks to independent handlers.
	start := make(chan struct{})
	errors := make(chan error, 4)
	for _, delivery := range []struct {
		replica   *translationProjectionFixture
		old, next string
	}{
		{first, zero, initialCommit}, {replacement, initialCommit, newCommit},
		{replacement, zero, initialCommit}, {first, initialCommit, newCommit},
	} {
		go func() {
			<-start
			errors <- delivery.replica.callback(t, delivery.old, delivery.next, next.path)
		}()
	}
	close(start)
	for range 4 {
		require.NoError(t, <-errors)
	}
	require.Equal(t, current, first.source(t), "delayed or duplicate delivery must not advance version/generation")
	require.Equal(t, current, replacement.source(t))
	after, err := first.store.GetProduct(first.ctx, target.UID)
	require.NoError(t, err)
	require.Equal(t, before, after, "accepted Translation callbacks must leave target authored/resolved data unchanged")
}

func newTranslationCallbackReplica(t *testing.T, original *translationProjectionFixture) *translationProjectionFixture {
	t.Helper()
	server, err := NewServer(ServerDeps{
		Store: original.store, GitReader: original.git, Registry: original.server.registry,
		ServiceSecret: original.server.serviceSecret, Logger: original.server.log,
		TranslationActivation: TranslationActivation{Enabled: true, DescriptorDigest: translation.CapabilityDigest()},
	})
	require.NoError(t, err)
	replica := *original
	replica.server = server
	return &replica
}
