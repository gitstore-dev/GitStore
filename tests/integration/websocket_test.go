package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// TestValidPushEmitsWebSocketNotification covers contract C-002:
// Pushing a v-prefixed annotated tag to gitstore-git-service must broadcast a
// WebSocket notification with event="release_created", a non-empty tag, and a
// 40-char commit SHA within 5 seconds.
func TestValidPushEmitsWebSocketNotification(t *testing.T) {
	// Subscribe to WebSocket before pushing so we don't miss the event.
	wsURL := fmt.Sprintf("%s/ws", gitServerWSURL)
	origin := fmt.Sprintf("%s/", strings.Replace(gitServerWSURL, "ws://", "http://", 1))

	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		t.Skipf("cannot connect to WebSocket at %s: %v — is docker compose up?", wsURL, err)
	}
	defer ws.Close()

	// Push a valid product commit then an annotated v-tag to trigger the event.
	// The git-service only broadcasts for v-prefixed annotated tags.
	ts := time.Now().UnixMilli()
	h := newPushHelper(t)
	h.commitProduct(fmt.Sprintf("inttest-ws-%d.md", ts), uniqueValidProduct(ts))
	out, err := h.push()
	if err != nil {
		t.Fatalf("push failed unexpectedly: %v\n%s", err, out)
	}
	tag := fmt.Sprintf("v0.0.1-ws-%d", ts)
	out, err = h.pushTag(tag)
	if err != nil {
		t.Fatalf("tag push failed unexpectedly: %v\n%s", err, out)
	}

	// The actual event JSON is: {"event":"release_created","tag":"...","commit":"<40-char SHA>","timestamp":"..."}
	type notification struct {
		Event     string `json:"event"`
		Tag       string `json:"tag"`
		Commit    string `json:"commit"`
		Timestamp string `json:"timestamp"`
	}

	done := make(chan notification, 1)
	go func() {
		var msg []byte
		if readErr := websocket.Message.Receive(ws, &msg); readErr != nil {
			return
		}
		var n notification
		if jsonErr := json.Unmarshal(msg, &n); jsonErr == nil {
			done <- n
		}
	}()

	select {
	case n := <-done:
		if n.Event != "release_created" {
			t.Errorf("WebSocket notification 'event' expected %q, got %q", "release_created", n.Event)
		}
		if n.Tag == "" {
			t.Error("WebSocket notification missing 'tag' field")
		}
		if len(n.Commit) != 40 {
			t.Errorf("WebSocket notification 'commit' expected 40-char hex, got %q", n.Commit)
		}
	case <-time.After(5 * time.Second):
		t.Error("no WebSocket notification received within 5 seconds after tag push")
	}
}

// isWebSocketAvailable is a lightweight check used by other tests.
func isWebSocketAvailable(t *testing.T) bool {
	t.Helper()
	url := fmt.Sprintf("%s/health", gitServerURL)
	resp, err := http.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
