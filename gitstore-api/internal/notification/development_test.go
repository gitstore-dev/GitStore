// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package notification

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDevelopmentSMTPAcceptanceAndDuplicateDelivery(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	delivered := make(chan string, 2)
	go func() {
		for range 2 {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_ = connection.SetDeadline(time.Now().Add(time.Second))
			client := textproto.NewConn(connection)
			_ = client.PrintfLine("220 owned SMTP")
			for {
				line, err := client.ReadLine()
				if err != nil {
					break
				}
				switch {
				case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"),
					strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
					_ = client.PrintfLine("250 accepted")
				case line == "DATA":
					_ = client.PrintfLine("354 data")
					body, err := io.ReadAll(client.DotReader())
					if err != nil {
						break
					}
					delivered <- string(body)
					_ = client.PrintfLine("250 delivered")
				case line == "QUIT":
					_ = client.PrintfLine("221 bye")
					_ = client.Close()
				}
			}
			_ = client.Close()
		}
	}()
	provider, err := NewDevelopment("smtp://"+listener.Addr().String(), "gitstore@example.test")
	require.NoError(t, err)
	message := Message{Recipient: "staff@example.test", Subject: "Invitation", Body: "private setup URL", HTMLBody: "<p>private setup URL</p>"}
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		require.NoError(t, provider.Send(ctx, message))
		cancel()
		select {
		case body := <-delivered:
			require.Contains(t, body, "private setup URL")
			require.Contains(t, body, "multipart/alternative")
		case <-time.After(time.Second):
			t.Fatal("SMTP delivery was not accepted")
		}
	}
}

func TestDevelopmentSMTPOutageAndCanceledHandshakeAreBounded(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	provider, err := NewDevelopment("smtp://"+listener.Addr().String(), "gitstore@example.test")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			close(done)
			return
		}

		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
		close(done)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	require.Error(t, provider.Send(ctx, Message{Recipient: "a@example.test", Subject: "Hello", Body: "body"}))
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled SMTP handshake leaked a connection")
	}
	require.NoError(t, listener.Close())
	require.Error(t, provider.Send(t.Context(), Message{Recipient: "a@example.test", Subject: "Hello", Body: "body"}))
}

func TestDevelopmentSMTPSDoesNotTrustSelfSignedCertificatesImplicitly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	provider, err := NewDevelopment(strings.Replace(server.URL, "https://", "smtps://", 1), "gitstore@example.test")
	require.NoError(t, err)
	require.Error(t, provider.Send(t.Context(), Message{Recipient: "a@example.test", Subject: "Hello", Body: "body"}))
}

func TestDevelopmentSaturationRejectsWithoutWaitingAndReleasesCapacity(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		close(accepted)
		_, _ = io.Copy(io.Discard, connection)
	}()
	provider, err := NewDevelopment("smtp://"+listener.Addr().String(), "gitstore@example.test", 1)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	message := Message{Recipient: "a@example.test", Subject: "Invitation", Body: "private setup"}
	done := make(chan error, 1)
	go func() { done <- provider.Send(ctx, message) }()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("first SMTP request did not connect")
	}
	start := time.Now()
	require.Error(t, provider.Send(t.Context(), message))
	require.Less(t, time.Since(start), 100*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("cancellation did not release SMTP capacity")
	}
	acceptedAgain := make(chan struct{})
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		connection.Close()
		close(acceptedAgain)
	}()
	require.Error(t, provider.Send(t.Context(), message))
	select {
	case <-acceptedAgain:
	case <-time.After(time.Second):
		t.Fatal("canceled request did not release provider admission capacity")
	}
}
