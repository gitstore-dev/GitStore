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
}

// Read implements io.Reader for UploadPackReceiver, yielding chunks from the gRPC stream.
func (r *UploadPackReceiver) Read(p []byte) (int, error) {
	if len(r.buf) > 0 {
		n := copy(p, r.buf)
		r.buf = r.buf[n:]
		return n, nil
	}
	chunk, err := r.stream.Recv()
	if err != nil {
		return 0, err
	}
	n := copy(p, chunk.Data)
	if n < len(chunk.Data) {
		r.buf = chunk.Data[n:]
	}
	if chunk.IsLast {
		return n, io.EOF
	}
	return n, nil
}
