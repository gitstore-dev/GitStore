// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors

// Integration test: concurrent gRPC CommitFile calls.
// 10 simultaneous commits must all succeed with distinct SHAs and no conflicts.
// Requires Docker. Run with: go test -tags integration ./tests/integration/...

//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/gitstore-dev/gitstore/api/internal/gitclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGRPCConcurrentCommitFileDistinctSHAs asserts that 10 simultaneous CommitFile RPCs
// each produce a distinct commit SHA without conflicts or errors.
func TestGRPCConcurrentCommitFileDistinctSHAs(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()
	const workers = 10

	type result struct {
		sha string
		err error
	}
	results := make([]result, workers)

	var wg sync.WaitGroup
	ready := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			<-ready
			sha, err := client.CommitFile(ctx, gitclient.CommitFileParams{
				Path: fmt.Sprintf("products/concurrent-sha-%02d.md", idx),
				Content: []byte(fmt.Sprintf(
					"---\nid: sha-%02d\nsku: SHA-%02d\ntitle: SHA Product %d\nprice: 1.00\ncurrency: USD\n---\n",
					idx, idx, idx,
				)),
				CommitMessage: fmt.Sprintf("Create product: SHA Product %d", idx),
			})
			results[idx] = result{sha: sha, err: err}
		}()
	}
	close(ready)
	wg.Wait()

	// All commits must succeed.
	for i, r := range results {
		require.NoError(t, r.err, "worker %d returned error", i)
		assert.NotEmpty(t, r.sha, "worker %d returned empty SHA", i)
		assert.Len(t, r.sha, 40, "worker %d returned non-hex SHA", i)
	}

	// All SHAs must be distinct.
	seen := make(map[string]int)
	for i, r := range results {
		if prev, dup := seen[r.sha]; dup {
			t.Errorf("worker %d returned same SHA %s as worker %d", i, r.sha, prev)
		}
		seen[r.sha] = i
	}
	assert.Len(t, seen, workers, "expected %d distinct commit SHAs", workers)

	// All committed files must be readable.
	for i := 0; i < workers; i++ {
		path := fmt.Sprintf("products/concurrent-sha-%02d.md", i)
		_, err := client.ReadFile(ctx, path, "HEAD")
		assert.NoError(t, err, "worker %d file should be readable after commit", i)
	}
}

// TestGRPCConcurrentMixedOps asserts that interleaved CommitFile and DeleteFile
// operations complete without deadlock or panic.
func TestGRPCConcurrentMixedOps(t *testing.T) {
	client, cleanup := startMutationContainer(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-create files that will be deleted.
	const toDelete = 5
	for i := 0; i < toDelete; i++ {
		_, err := client.CommitFile(ctx, gitclient.CommitFileParams{
			Path:          fmt.Sprintf("products/to-delete-%02d.md", i),
			Content:       []byte(fmt.Sprintf("---\nsku: DEL-MIX-%02d\n---\n", i)),
			CommitMessage: fmt.Sprintf("Create to-delete product %d", i),
		})
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	errs := make([]error, toDelete*2)

	for i := 0; i < toDelete; i++ {
		idx := i
		// Concurrent creates.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[idx] = client.CommitFile(ctx, gitclient.CommitFileParams{
				Path:          fmt.Sprintf("products/mix-create-%02d.md", idx),
				Content:       []byte(fmt.Sprintf("---\nsku: MIX-CREATE-%02d\n---\n", idx)),
				CommitMessage: fmt.Sprintf("Create mixed product %d", idx),
			})
		}()
		// Concurrent deletes.
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[toDelete+idx] = client.DeleteFile(ctx, gitclient.DeleteFileParams{
				Path:          fmt.Sprintf("products/to-delete-%02d.md", idx),
				CommitMessage: fmt.Sprintf("Delete mixed product %d", idx),
			})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "mixed op %d failed", i)
	}
}
