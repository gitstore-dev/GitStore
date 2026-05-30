// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"os"
	"testing"
)

var (
	gitServerURL    string
	gitServerGitURL string
	apiURL          string
)

func TestMain(m *testing.M) {
	gitServerURL = getEnv("GIT_SERVER_URL", "http://localhost:50051")
	gitServerGitURL = getEnv("GIT_SERVER_GIT_URL", "http://localhost:5000")
	apiURL = getEnv("API_URL", "http://localhost:4000")

	os.Exit(m.Run())
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
