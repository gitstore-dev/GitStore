// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"io"
	"strings"
	"sync"

	"github.com/gitstore-dev/gitstore/observability"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Keep the existing client signal names and fixed protobuf-method attributes.
// SDK counters record actual successful message transfers and at most one
// observed terminal result, including client-streaming success without a
// second RecvMsg.
type rpcMetrics struct{ started, handled, received, sent metric.Int64Counter }

func newRPCMetrics(meter metric.Meter) *rpcMetrics {
	return &rpcMetrics{
		started:  observability.MustInstrument(meter.Int64Counter("grpc_client_started_total")),
		handled:  observability.MustInstrument(meter.Int64Counter("grpc_client_handled_total")),
		received: observability.MustInstrument(meter.Int64Counter("grpc_client_msg_received_total")),
		sent:     observability.MustInstrument(meter.Int64Counter("grpc_client_msg_sent_total")),
	}
}

func rpcAttributes(method, kind string) []attribute.KeyValue {
	name := "unknown"
	service := "unknown"
	const prefix = "/gitstore.git.v1.GitService/"
	if strings.HasPrefix(method, prefix) {
		switch candidate := strings.TrimPrefix(method, prefix); candidate {
		case "CreateRepository", "DeleteRepository", "GetFile", "GetFileStream", "ListFiles", "CommitFile", "DeleteFile", "CreateTag", "ListTags", "GetLatestTag", "InfoRefs", "ReceivePack", "UploadPack":
			name = candidate
			service = "gitstore.git.v1.GitService"
		}
	}
	return []attribute.KeyValue{attribute.String("grpc_type", kind), attribute.String("grpc_service", service), attribute.String("grpc_method", name)}
}

func rpcStatus(err error) codes.Code {
	code := status.Code(err)
	if code > codes.Unauthenticated {
		return codes.Unknown
	}
	return code
}

func (m *rpcMetrics) unary(ctx context.Context, method string, request, reply any, connection *grpc.ClientConn, invoke grpc.UnaryInvoker, options ...grpc.CallOption) error {
	attrs := rpcAttributes(method, "unary")
	labels := metric.WithAttributes(attrs...)
	m.started.Add(ctx, 1, labels)
	m.received.Add(ctx, 0, labels)
	m.sent.Add(ctx, 1, labels)
	err := invoke(ctx, method, request, reply, connection, options...)
	if err == nil {
		m.received.Add(ctx, 1, labels)
	}
	m.handled.Add(ctx, 1, metric.WithAttributes(append(attrs, attribute.String("grpc_code", rpcStatus(err).String()))...))
	return err
}

func (m *rpcMetrics) stream(ctx context.Context, desc *grpc.StreamDesc, connection *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
	kind := "bidi_stream"
	if desc.ClientStreams && !desc.ServerStreams {
		kind = "client_stream"
	} else if !desc.ClientStreams && desc.ServerStreams {
		kind = "server_stream"
	}
	attrs := rpcAttributes(method, kind)
	labels := metric.WithAttributes(attrs...)
	m.started.Add(ctx, 1, labels)
	m.sent.Add(ctx, 0, labels)
	m.received.Add(ctx, 0, labels)
	stream, err := streamer(ctx, desc, connection, method, options...)
	if err != nil {
		m.handled.Add(ctx, 1, metric.WithAttributes(append(attrs, attribute.String("grpc_code", rpcStatus(err).String()))...))
		return nil, err
	}
	return &rpcMetricStream{ClientStream: stream, metrics: m, ctx: ctx, attrs: attrs, serverStreams: desc.ServerStreams}, nil
}

type rpcMetricStream struct {
	grpc.ClientStream
	metrics       *rpcMetrics
	ctx           context.Context
	attrs         []attribute.KeyValue
	finished      sync.Once
	serverStreams bool
}

func (s *rpcMetricStream) SendMsg(message any) error {
	err := s.ClientStream.SendMsg(message)
	if err == nil {
		s.metrics.sent.Add(s.ctx, 1, metric.WithAttributes(s.attrs...))
	}
	return err
}

func (s *rpcMetricStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if err == nil {
		s.metrics.received.Add(s.ctx, 1, metric.WithAttributes(s.attrs...))
	}
	if err != nil || !s.serverStreams {
		s.finished.Do(func() {
			code := rpcStatus(err)
			if err == io.EOF {
				code = codes.OK
			}
			s.metrics.handled.Add(s.ctx, 1, metric.WithAttributes(append(s.attrs, attribute.String("grpc_code", code.String()))...))
		})
	}
	return err
}
