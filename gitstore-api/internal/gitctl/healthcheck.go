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
	return runHTTPProbe("healthcheck", "http://127.0.0.1:4000/ready", time.Second, args, nil, stderr)
}

func runHTTPProbe(name, defaultURL string, defaultTimeout time.Duration, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("url", defaultURL, "probe URL")
	timeout := flags.Duration("timeout", defaultTimeout, "request timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	u, err := url.Parse(*endpoint)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || *timeout <= 0 || flags.NArg() != 0 {
		fmt.Fprintf(stderr, "%s requires an HTTP(S) URL and a positive timeout\n", name)
		return 2
	}
	client := &http.Client{
		Timeout: *timeout,
		// Probe the selected process directly, without a proxy or login redirect.
		Transport: &http.Transport{},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	response, err := client.Get(*endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "%s request failed\n", name)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		fmt.Fprintf(stderr, "%s returned HTTP %d\n", name, response.StatusCode)
		return 1
	}
	if stdout != nil {
		const maxBodyBytes = 16 << 20
		body, err := io.ReadAll(io.LimitReader(response.Body, maxBodyBytes+1))
		if err != nil || len(body) > maxBodyBytes {
			fmt.Fprintln(stderr, "metrics response unreadable or exceeds 16 MiB")
			return 1
		}
		if _, err := stdout.Write(body); err != nil {
			fmt.Fprintln(stderr, "metrics output failed")
			return 1
		}
	}
	return 0
}
