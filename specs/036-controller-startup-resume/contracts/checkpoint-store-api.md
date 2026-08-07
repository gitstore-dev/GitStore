# Contract: CheckpointStore

**Package**: `github.com/gitstore-dev/gitstore/controller-manager/internal/checkpoint`
**Stability**: New in this spec

## Interface

```go
type Record struct {
    Kind            string
    ResourceVersion string
    Snapshot        json.RawMessage
    ReplayKeys      []types.WorkItemKey
    WrittenAt       time.Time
}

type Store interface {
    // Load returns the persisted Record for kind. Returns a non-nil error if
    // no record exists, the file/entry cannot be read, or its contents cannot
    // be parsed. Callers MUST treat every error identically — there is no
    // distinction between "missing" and "corrupt" (FR-008).
    Load(ctx context.Context, kind string) (Record, error)

    // Save persists rec, keyed by rec.Kind. MUST be atomic: a concurrent Load
    // for the same kind observes either the fully-written new Record or the
    // previous one — never a partial write (FR-006).
    Save(ctx context.Context, rec Record) error
}
```

## Implementations

### FilesystemStore

```go
type FilesystemStore struct { Dir string }

func NewFilesystemStore(dir string) (*FilesystemStore, error) // MkdirAll(dir, 0o755)
```

- One file per kind: `<Dir>/<kind>.checkpoint.json`.
- `Save`: marshal `Record` to JSON, write to a temp file in `Dir` (`os.CreateTemp`), `Sync`, `Close`, then `os.Rename` to the final path. Rename is atomic on the same filesystem — this is the write-temp-then-rename guarantee required by FR-006 and the "killed mid-write" edge case.
- `Load`: `os.ReadFile` the final path; `json.Unmarshal`; validate the kind, non-empty cursor, cache snapshot, and replay-key kinds. Missing, malformed, or semantically invalid files surface as a returned `error` — no sentinel differentiation.
- A write or corrupted file for one kind MUST NOT affect any other kind's file (structurally guaranteed — distinct file paths).

### MemoryStore

```go
type MemoryStore struct{} // internally: mutex-guarded map[string]Record

func NewMemoryStore() *MemoryStore
```

- Same `Store` interface; `Save` overwrites the map entry for `rec.Kind` under a write lock, `Load` reads under a read lock. Used only in tests — no production wiring.

## Caller Obligations (the `Runner`)

1. Call `Load(ctx, kind)` exactly once, at the start of `Run(ctx)`, to decide bootstrap vs. resume (FR-007/FR-008).
2. Never call `Load` again during the lifetime of a single `Run(ctx)` call — reconnects use the in-memory cursor (FR-011, see `runner-contract.md`).
3. Persist the bootstrap list snapshot before marking the cache synced or opening the watch.
4. Call `Save` at the configured flush interval and on clean shutdown (FR-005). Each record includes the current cache snapshot and deletion replay keys so a restart can rebuild volatile state and replay work that may not have completed before the process stopped. On `Save` error, retry with backoff before consuming the next watch event (backpressure — FR-005, SC-004) rather than treating the failure as fatal.
5. On `ErrWatchExpired` recovery, call `Save` with the fresh `resourceVersion` and rebuilt snapshot from the re-list before resuming `Watch` (FR-009).
