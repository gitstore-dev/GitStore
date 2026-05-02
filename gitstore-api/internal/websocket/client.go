// Websocket client - connects to git server for catalog update notifications

package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

// GitEvent represents a git event notification
type GitEvent struct {
	Event     string    `json:"event"`
	Tag       string    `json:"tag"`
	Commit    string    `json:"commit,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// EventHandler is called when a git event is received
type EventHandler func(event GitEvent)

// Client connects to git server websocket
type Client struct {
	url            string
	logger         *zap.Logger
	handler        EventHandler
	conn           *websocket.Conn
	reconnectDelay time.Duration
	maxReconnect   time.Duration
}

// NewClient creates a new websocket client
func NewClient(url string, handler EventHandler, logger *zap.Logger) *Client {
	return &Client{
		url:            url,
		logger:         logger,
		handler:        handler,
		reconnectDelay: 1 * time.Second,
		maxReconnect:   30 * time.Second,
	}
}

// Start connects to websocket and listens for events
func (c *Client) Start(ctx context.Context) error {
	c.logger.Info("Starting websocket client", zap.String("url", c.url))

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("Websocket client stopping")
			if c.conn != nil {
				c.conn.Close()
			}
			return ctx.Err()
		default:
			if err := c.connect(ctx); err != nil {
				c.logger.Error("Connection failed", zap.Error(err))
				c.waitBeforeReconnect(ctx)
				continue
			}

			// Connected successfully, listen for messages
			if err := c.listen(ctx); err != nil {
				c.logger.Warn("Connection lost", zap.Error(err))
				c.waitBeforeReconnect(ctx)
			}
		}
	}
}

// connect establishes websocket connection
func (c *Client) connect(ctx context.Context) error {
	c.logger.Info("Connecting to websocket", zap.String("url", c.url))

	dialer := websocket.DefaultDialer
	dialer.HandshakeTimeout = 10 * time.Second

	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	c.conn = conn
	c.logger.Info("Websocket connected")

	return nil
}

// listen reads messages from websocket
func (c *Client) listen(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				return fmt.Errorf("read failed: %w", err)
			}

			c.logger.Debug("Received message", zap.ByteString("message", message))

			// Parse event
			var event GitEvent
			if err := json.Unmarshal(message, &event); err != nil {
				c.logger.Warn("Failed to parse event", zap.Error(err))
				continue
			}

			// Handle event
			c.logger.Info("Received git event",
				zap.String("event", event.Event),
				zap.String("tag", event.Tag),
			)

			if c.handler != nil {
				c.handler(event)
			}
		}
	}
}

// waitBeforeReconnect implements exponential backoff
func (c *Client) waitBeforeReconnect(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(c.reconnectDelay):
		// Exponential backoff
		c.reconnectDelay *= 2
		if c.reconnectDelay > c.maxReconnect {
			c.reconnectDelay = c.maxReconnect
		}
		c.logger.Info("Reconnecting", zap.Duration("delay", c.reconnectDelay))
	}
}

// Close closes the websocket connection
func (c *Client) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
