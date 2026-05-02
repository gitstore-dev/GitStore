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
// A valid commit pushed to gitstore-git-service must trigger a WebSocket
// notification containing repository, ref, and commit_sha fields within 5s.
func TestValidPushEmitsWebSocketNotification(t *testing.T) {
	// Subscribe to WebSocket before pushing so we don't miss the event.
	wsURL := fmt.Sprintf("%s/ws", gitServerWSURL)
	origin := fmt.Sprintf("%s/", strings.Replace(gitServerWSURL, "ws://", "http://", 1))

	ws, err := websocket.Dial(wsURL, "", origin)
	if err != nil {
		t.Skipf("cannot connect to WebSocket at %s: %v — is docker compose up?", wsURL, err)
	}
	defer ws.Close()

	// Push a valid product commit.
	h := newPushHelper(t)
	h.commitProduct("inttest-valid.md", validProductFrontmatter)
	out, err := h.push()
	if err != nil {
		t.Fatalf("push failed unexpectedly: %v\n%s", err, out)
	}

	// Wait for notification.
	type notification struct {
		Repository string `json:"repository"`
		Ref        string `json:"ref"`
		CommitSHA  string `json:"commit_sha"`
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
		if n.Repository == "" {
			t.Error("WebSocket notification missing 'repository' field")
		}
		if n.Ref == "" {
			t.Error("WebSocket notification missing 'ref' field")
		}
		if len(n.CommitSHA) != 40 {
			t.Errorf("WebSocket notification 'commit_sha' expected 40-char hex, got %q", n.CommitSHA)
		}
	case <-time.After(5 * time.Second):
		t.Error("no WebSocket notification received within 5 seconds after push")
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
