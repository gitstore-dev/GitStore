// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

package gitclient

import (
	"io"

	gitv1 "github.com/gitstore-dev/gitstore/api/gen/gitstore/git/v1"
)

// UploadPackReceiver wraps a server-streaming UploadPack gRPC call.
type UploadPackReceiver struct {
	stream gitv1.GitService_UploadPackClient
	buf    []byte
	done   bool
}

// Read implements io.Reader for UploadPackReceiver, yielding chunks from the gRPC stream.
func (r *UploadPackReceiver) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	if r.done {
		return 0, io.EOF
	}
	for {
		chunk, err := r.stream.Recv()
		if err != nil {
			return 0, err
		}
		if chunk.IsLast {
			r.done = true
		}
		if len(chunk.Data) == 0 {
			if r.done {
				return 0, io.EOF
			}
			continue
		}
		n := copy(p, chunk.Data)
		if n < len(chunk.Data) {
			r.buf = chunk.Data[n:]
		}
		return n, nil
	}
}
