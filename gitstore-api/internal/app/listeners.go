// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package app

import (
	"fmt"
	"net"
	"net/http"
)

type httpEndpoint struct {
	name     string
	server   *http.Server
	listener net.Listener
}

func bindHTTPServers(endpoints []httpEndpoint) ([]httpEndpoint, error) {
	for i := range endpoints {
		listener, err := net.Listen("tcp", endpoints[i].server.Addr)
		if err != nil {
			for j := 0; j < i; j++ {
				_ = endpoints[j].listener.Close()
			}
			return nil, fmt.Errorf("bind %s listener %s: %w", endpoints[i].name, endpoints[i].server.Addr, err)
		}
		endpoints[i].listener = listener
	}
	return endpoints, nil
}
