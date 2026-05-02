package integration

import (
	"os"
	"testing"
)

var (
	gitServerURL   string
	gitServerGitURL string
	gitServerWSURL string
	apiURL         string
)

func TestMain(m *testing.M) {
	gitServerURL = getEnv("GIT_SERVER_URL", "http://localhost:9418")
	gitServerGitURL = getEnv("GIT_SERVER_GIT_URL", "http://localhost:9418")
	gitServerWSURL = getEnv("GIT_SERVER_WS_URL", "ws://localhost:8080")
	apiURL = getEnv("API_URL", "http://localhost:4000")

	os.Exit(m.Run())
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
