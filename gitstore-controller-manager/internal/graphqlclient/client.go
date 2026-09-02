// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Package graphqlclient is a minimal GraphQL client: POST-based
// query/mutation requests and graphql-transport-ws subscriptions, speaking
// exactly the protocol gitstore-api's gqlgen transport.Websocket serves
// (spec 040). It intentionally implements only the two operations this
// controller-manager needs, rather than a general-purpose GraphQL SDK
// (spec 039 research.md R1).
package graphqlclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// Client issues GraphQL query/mutation requests over HTTP and opens
// graphql-transport-ws subscriptions against a single gitstore-api base URL.
type Client struct {
	baseURL     string
	credentials CredentialSource
	http        *http.Client
	dialer      *websocket.Dialer

	subIDs atomic.Uint64
}

// New returns a Client targeting baseURL (e.g. "http://localhost:4000/graphql"
// or "ws://localhost:4000/graphql" for a dedicated subscription dialer —
// Subscribe rewrites an http(s) baseURL to ws(s) automatically).
// credentials must not be nil. NewStaticToken is available for isolated tests.
func New(baseURL string, credentials CredentialSource) *Client {
	if credentials == nil {
		panic("graphqlclient.New: credentials must not be nil")
	}
	return &Client{
		baseURL:     baseURL,
		credentials: credentials,
		http:        http.DefaultClient,
		dialer:      websocket.DefaultDialer,
	}
}

// Error is a single GraphQL response error, as returned by Query/Mutate and
// by Subscription.Err().
type Error struct {
	Message    string         `json:"message"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data,omitempty"`
	Errors []*Error        `json:"errors,omitempty"`
}

// Query issues a GraphQL query via POST and decodes the "data" field into out.
func (c *Client) Query(ctx context.Context, query string, vars map[string]any, out any) error {
	return c.do(ctx, query, vars, out)
}

// Mutate issues a GraphQL mutation via POST and decodes the "data" field into out.
func (c *Client) Mutate(ctx context.Context, mutation string, vars map[string]any, out any) error {
	return c.do(ctx, mutation, vars, out)
}

func (c *Client) do(ctx context.Context, doc string, vars map[string]any, out any) error {
	body, err := json.Marshal(gqlRequest{Query: doc, Variables: vars})
	if err != nil {
		return fmt.Errorf("graphqlclient: marshal request: %w", err)
	}

	token, err := c.credentials.Current(ctx)
	if err != nil {
		return fmt.Errorf("graphqlclient: acquire credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("graphqlclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("graphqlclient: request failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 300 {
		return fmt.Errorf("graphqlclient: unexpected HTTP status %d", resp.StatusCode)
	}

	var gr gqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return fmt.Errorf("graphqlclient: decode response: %w", err)
	}
	if len(gr.Errors) > 0 {
		return gr.Errors[0]
	}
	if out != nil && len(gr.Data) > 0 {
		if err := json.Unmarshal(gr.Data, out); err != nil {
			return fmt.Errorf("graphqlclient: decode data: %w", err)
		}
	}
	return nil
}

// Subscription is an open graphql-transport-ws subscription stream, mirroring
// the shape of listwatch.Watcher[T] (spec 036) so adapters wrapping it are
// thin, not reimplementations.
type Subscription interface {
	// Next delivers each "next" message's raw payload JSON. Closed when the
	// server sends "complete", sends "error", or Stop is called.
	Next() <-chan json.RawMessage
	// Err reports why Next's channel closed. Valid only after it closes.
	// nil means a clean completion or an explicit Stop call.
	Err() error
	// Stop ends the subscription. Safe to call multiple times.
	Stop()
}

type subscription struct {
	conn *websocket.Conn
	id   string

	next    chan json.RawMessage
	errOnce sync.Once
	err     error

	stopOnce sync.Once
	done     chan struct{}
}

func (s *subscription) Next() <-chan json.RawMessage { return s.next }
func (s *subscription) Err() error                   { return s.err }

func (s *subscription) Stop() {
	s.stopOnce.Do(func() {
		close(s.done)
		_ = s.conn.WriteJSON(wsMessage{ID: s.id, Type: "complete"})
		_ = s.conn.Close()
	})
}

func (s *subscription) setErr(err error) {
	s.errOnce.Do(func() { s.err = err })
}

// wsMessage is the graphql-transport-ws envelope
// (https://github.com/enisdenjo/graphql-ws/blob/master/PROTOCOL.md).
type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type subscribePayload struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// Subscribe dials the server as a graphql-transport-ws WebSocket, performs
// the connection_init/connection_ack handshake, and sends a subscribe
// message for the given subscription document. The returned Subscription
// streams "next" payloads until Stop is called or the server closes the
// stream (via "complete" or "error").
func (c *Client) Subscribe(ctx context.Context, subscriptionDoc string, vars map[string]any) (Subscription, error) {
	token, err := c.credentials.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("graphqlclient: acquire credentials: %w", err)
	}

	wsURL := toWebsocketURL(c.baseURL)

	header := http.Header{}
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")

	conn, _, err := c.dialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return nil, fmt.Errorf("graphqlclient: dial: %w", err)
	}

	initPayload := map[string]any{}
	if token != "" {
		initPayload["Authorization"] = "Bearer " + token
	}
	initPayloadJSON, err := json.Marshal(initPayload)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: marshal connection_init payload: %w", err)
	}
	if err := conn.WriteJSON(wsMessage{Type: "connection_init", Payload: initPayloadJSON}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: send connection_init: %w", err)
	}

	var ack wsMessage
	if err := conn.ReadJSON(&ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: read connection_ack: %w", err)
	}
	if ack.Type != "connection_ack" {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: expected connection_ack, got %q", ack.Type)
	}

	id := strconv.FormatUint(c.subIDs.Add(1), 10)
	subPayload, err := json.Marshal(subscribePayload{Query: subscriptionDoc, Variables: vars})
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: marshal subscribe payload: %w", err)
	}
	if err := conn.WriteJSON(wsMessage{ID: id, Type: "subscribe", Payload: subPayload}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("graphqlclient: send subscribe: %w", err)
	}

	sub := &subscription{
		conn: conn,
		id:   id,
		next: make(chan json.RawMessage, 16),
		done: make(chan struct{}),
	}
	go sub.readLoop()
	return sub, nil
}

func (s *subscription) readLoop() {
	defer close(s.next)
	for {
		var msg wsMessage
		if err := s.conn.ReadJSON(&msg); err != nil {
			select {
			case <-s.done:
				// Stop() already closed the connection; not a real error.
			default:
				s.setErr(fmt.Errorf("graphqlclient: subscription read failed: %w", err))
			}
			return
		}
		switch msg.Type {
		case "next":
			var payload struct {
				Data   json.RawMessage `json:"data"`
				Errors []*Error        `json:"errors"`
			}
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				s.setErr(fmt.Errorf("graphqlclient: decode next payload: %w", err))
				return
			}
			// gqlgen's websocket transport delivers a resolver-returned
			// GraphQL error (e.g. WATCH_EXPIRED, raised before the
			// subscription channel even opens) as a "next" message with a
			// populated errors array and no data, not as a protocol-level
			// "error" message — the latter is reserved for panics/executor
			// failures during an already-open stream. Treat both the same
			// way: surface the error and end the subscription.
			if len(payload.Errors) > 0 {
				s.setErr(payload.Errors[0])
				return
			}
			select {
			case s.next <- payload.Data:
			case <-s.done:
				return
			}
		case "error":
			var gqlErrs []*Error
			if err := json.Unmarshal(msg.Payload, &gqlErrs); err != nil || len(gqlErrs) == 0 {
				s.setErr(fmt.Errorf("graphqlclient: subscription error (undecodable payload)"))
				return
			}
			s.setErr(gqlErrs[0])
			return
		case "complete":
			return
		}
	}
}

func toWebsocketURL(httpURL string) string {
	switch {
	case strings.HasPrefix(httpURL, "https://"):
		return "wss://" + strings.TrimPrefix(httpURL, "https://")
	case strings.HasPrefix(httpURL, "http://"):
		return "ws://" + strings.TrimPrefix(httpURL, "http://")
	default:
		return httpURL
	}
}
