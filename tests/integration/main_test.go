// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package integration

import (
	"os"
	"testing"
)

var (
	apiURL string // GraphQL API base URL (port 4000)
	gitURL string // Git smart HTTP base URL (port 5000)
)

func TestMain(m *testing.M) {
	apiURL = getEnv("API_URL", "http://localhost:4000")
	gitURL = getEnv("GIT_URL", "http://localhost:5000")

	os.Exit(m.Run())
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
