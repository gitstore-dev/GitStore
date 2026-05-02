package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestWebSocketHealthEndpoint covers contract C-005:
// GET /websocket/health must return 200, body.status non-empty,
// body.active_connections is a non-negative number.
func TestWebSocketHealthEndpoint(t *testing.T) {
	url := fmt.Sprintf("%s/websocket/health", gitServerURL)

	resp, err := http.Get(url)
	if err != nil {
		t.Skipf("gitstore-git-service unreachable (%s): %v — is docker compose up?", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 from %s, got %d", url, resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode websocket health response: %v", err)
	}

	if _, ok := body["status"]; !ok {
		t.Error("websocket health response missing 'status' field")
	}

	rawConns, ok := body["active_connections"]
	if !ok {
		t.Fatal("websocket health response missing 'active_connections' field")
	}
	// JSON numbers unmarshal as float64
	conns, ok := rawConns.(float64)
	if !ok || conns < 0 {
		t.Errorf("active_connections must be a non-negative number, got %v", rawConns)
	}
}
