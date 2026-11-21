// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"context"
	"io"
	"strings"
	"sync"
	"time"

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
type rpcMetrics struct {
	started, handled, received, sent metric.Int64Counter
	duration                         metric.Float64Histogram
}

func newRPCMetrics(meter metric.Meter) *rpcMetrics {
	return &rpcMetrics{
		started:  observability.MustInstrument(meter.Int64Counter("grpc_client_started_total")),
		handled:  observability.MustInstrument(meter.Int64Counter("grpc_client_handled_total")),
		received: observability.MustInstrument(meter.Int64Counter("grpc_client_msg_received_total")),
		sent:     observability.MustInstrument(meter.Int64Counter("grpc_client_msg_sent_total")),
		duration: observability.MustInstrument(meter.Float64Histogram("grpc_client_handling_seconds", metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30))),
	}
}

func rpcAttributes(method, kind string) []attribute.KeyValue {
	name := "unknown"
	service := "unknown"
	const prefix = "/gitstore.git.v1.GitService/"
	if strings.HasPrefix(method, prefix) {
		switch candidate := strings.TrimPrefix(method, prefix); candidate {
		case "CreateRepository", "DeleteRepository", "GetFile", "GetFiles", "GetFileStream", "ListFiles", "ResolveRefs", "CommitFile", "DeleteFile", "CreateTag", "ListTags", "GetLatestTag", "InfoRefs", "ReceivePack", "UploadPack":
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
	started := time.Now()
	err := invoke(ctx, method, request, reply, connection, options...)
	if err == nil {
		m.received.Add(ctx, 1, labels)
	}
	result := metric.WithAttributes(append(attrs, attribute.String("grpc_code", rpcStatus(err).String()))...)
	m.handled.Add(ctx, 1, result)
	m.duration.Record(ctx, time.Since(started).Seconds(), result)
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
	started := time.Now()
	var finished sync.Once
	finish := func(err error) {
		finished.Do(func() {
			code := rpcStatus(err)
			if err == io.EOF {
				code = codes.OK
			}
			result := metric.WithAttributes(append(attrs, attribute.String("grpc_code", code.String()))...)
			m.handled.Add(ctx, 1, result)
			m.duration.Record(ctx, time.Since(started).Seconds(), result)
		})
	}
	options = append([]grpc.CallOption{grpc.OnFinish(finish)}, options...)
	stream, err := streamer(ctx, desc, connection, method, options...)
	if err != nil {
		finish(err)
		return nil, err
	}
	return &rpcMetricStream{ClientStream: stream, metrics: m, ctx: ctx, attrs: attrs, finish: finish, serverStreams: desc.ServerStreams}, nil
}

type rpcMetricStream struct {
	grpc.ClientStream
	metrics       *rpcMetrics
	ctx           context.Context
	attrs         []attribute.KeyValue
	finish        func(error)
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
		s.finish(err)
	}
	return err
}
