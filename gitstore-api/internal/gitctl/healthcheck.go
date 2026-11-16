// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitctl

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

func runHealthcheck(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("healthcheck", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("url", "http://127.0.0.1:4000/ready", "readiness URL")
	timeout := flags.Duration("timeout", time.Second, "request timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || *timeout <= 0 || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "healthcheck requires an HTTP(S) URL and a positive timeout")
		return 2
	}
	client := &http.Client{
		Timeout: *timeout,
		// Readiness is a direct probe, not a proxy or login redirect check.
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Get(*endpoint)
	if err != nil {
		fmt.Fprintln(stderr, "readiness request failed")
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "readiness returned HTTP %d\n", response.StatusCode)
		return 1
	}
	return 0
}
