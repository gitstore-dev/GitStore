# Native Git service instructions

- Use the pinned Rust toolchain and Cargo dependencies: the existing stack
  uses gix, Tokio, Tonic, tracing, anyhow, async-trait and serde, with HMAC,
  SHA-2 and getrandom for signed RPCs.
- Keep production repository operations in the native gix implementation.
  Do not reintroduce a Git subprocess; client-side Git in integration/load
  drivers is a separate test concern.
- Exactly one Git process may be active per deployment. Retain repository
  storage and use non-overlapping replacement; neither shared volumes nor
  separate volumes make replication or autoscaling supported.
- Preserve receiver-bound cross-service authentication and the shared
  `GITSTORE_GRPC_AUTH__HMAC_SECRET` contract.
- Keep Go and Rust protobuf stubs consistent using the existing Buf
  generation configurations; Cargo consumes the generated Rust stubs.
- Use `make git` for local foreground startup and `../tests/AGENTS.md` for
  test safety and evidence scope. Capacity and profiling work follows
  `../tests/capacity/AGENTS.md`.
