// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/gitstore-dev/gitstore/observability/observabilitytest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSDKUnaryMetricsCountActualResponsesAndBoundMethods(t *testing.T) {
	require.Equal(t, codes.Unknown, rpcStatus(status.Error(codes.Code(12345), "untrusted status")))
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := newRPCMetrics(observabilitytest.Meter(t, registry, "gitstore/grpc-client"))
			var result error
			if failure {
				result = status.Error(codes.Unavailable, "private detail")
			}
			err := metrics.unary(t.Context(), "/gitstore.git.v1.GitService/GetFile", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return result })
			require.Equal(t, result, err)
			labels := map[string]string{"grpc_type": "unary", "grpc_service": "gitstore.git.v1.GitService", "grpc_method": "GetFile"}
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_started_total", labels))
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_msg_sent_total", labels))
			received := float64(1)
			if failure {
				received = 0
			}
			require.Equal(t, received, observabilitytest.Value(t, registry, "grpc_client_msg_received_total", labels))
			labels["grpc_code"] = status.Code(result).String()
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_handled_total", labels))
		})
	}
	for _, attribute := range rpcAttributes("/private/repository/principal/"+strings.Repeat("x", 1024), "unary") {
		require.NotContains(t, attribute.Value.AsString(), "private")
		require.Less(t, len(attribute.Value.AsString()), 64)
	}
}

type metricTestStream struct {
	grpc.ClientStream
	responses []error
}

func (s *metricTestStream) SendMsg(any) error { return nil }
func (s *metricTestStream) RecvMsg(any) error {
	if len(s.responses) == 0 {
		return io.EOF
	}
	err := s.responses[0]
	s.responses = s.responses[1:]
	return err
}

func TestSDKStreamRecordsOneCompletionIncludingClientStreamSuccess(t *testing.T) {
	for _, serverStreams := range []bool{false, true} {
		t.Run(map[bool]string{false: "client", true: "server"}[serverStreams], func(t *testing.T) {
			registry := prometheus.NewRegistry()
			metrics := newRPCMetrics(observabilitytest.Meter(t, registry, "gitstore/grpc-client"))
			inner := &metricTestStream{responses: []error{nil, io.EOF, io.EOF}}
			stream, err := metrics.stream(t.Context(), &grpc.StreamDesc{ClientStreams: !serverStreams, ServerStreams: serverStreams}, nil, "/gitstore.git.v1.GitService/UploadPack", func(context.Context, *grpc.StreamDesc, *grpc.ClientConn, string, ...grpc.CallOption) (grpc.ClientStream, error) {
				return inner, nil
			})
			require.NoError(t, err)
			require.NoError(t, stream.SendMsg(nil))
			require.NoError(t, stream.RecvMsg(nil))
			kind := "client_stream"
			if serverStreams {
				kind = "server_stream"
			}
			labels := map[string]string{"grpc_type": kind, "grpc_service": "gitstore.git.v1.GitService", "grpc_method": "UploadPack", "grpc_code": "OK"}
			if !serverStreams {
				require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_handled_total", labels))
			}
			require.ErrorIs(t, stream.RecvMsg(nil), io.EOF)
			require.ErrorIs(t, stream.RecvMsg(nil), io.EOF)
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_handled_total", labels))
			delete(labels, "grpc_code")
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_msg_received_total", labels))
			require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_msg_sent_total", labels))
		})
	}
}

func TestSDKStreamRecordsTransportCompletionWithoutAnotherReceive(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := newRPCMetrics(observabilitytest.Meter(t, registry, "gitstore/grpc-client"))
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	server.RegisterService(&grpc.ServiceDesc{
		ServiceName: "gitstore.git.v1.GitService",
		HandlerType: (*interface{})(nil),
		Streams: []grpc.StreamDesc{{
			StreamName:    "UploadPack",
			ServerStreams: true,
			Handler: func(_ any, stream grpc.ServerStream) error {
				if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
					return err
				}
				if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
					return err
				}
				<-stream.Context().Done()
				return status.Error(codes.Canceled, "stream canceled")
			},
		}},
	}, nil)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, listener.Close())
	})
	connection, err := grpc.NewClient("passthrough:///rpc-metrics",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }),
		grpc.WithStreamInterceptor(metrics.stream))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := connection.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true},
		"/gitstore.git.v1.GitService/UploadPack")
	require.NoError(t, err)
	require.NoError(t, stream.SendMsg(&emptypb.Empty{}))
	require.NoError(t, stream.CloseSend())
	require.NoError(t, stream.RecvMsg(&emptypb.Empty{}))
	cancel()
	labels := map[string]string{
		"grpc_type": "server_stream", "grpc_service": "gitstore.git.v1.GitService",
		"grpc_method": "UploadPack", "grpc_code": "Canceled",
	}
	require.Eventually(t, func() bool {
		return observabilitytest.Value(t, registry, "grpc_client_handled_total", labels) == 1
	}, 5*time.Second, 10*time.Millisecond, "transport completion must not require another application receive")
	require.Error(t, stream.RecvMsg(&emptypb.Empty{}))
	require.Equal(t, float64(1), observabilitytest.Value(t, registry, "grpc_client_handled_total", labels),
		"transport callback and terminal receive must share one completion")
	families, err := registry.Gather()
	require.NoError(t, err)
	found := false
	for _, family := range families {
		if family.GetName() == "grpc_client_handling_seconds" {
			require.Len(t, family.Metric, 1)
			require.Equal(t, uint64(1), family.Metric[0].GetHistogram().GetSampleCount(),
				"transport callback and terminal receive must share one duration sample")
			require.Positive(t, family.Metric[0].GetHistogram().GetSampleSum())
			found = true
		}
	}
	require.True(t, found)
}
