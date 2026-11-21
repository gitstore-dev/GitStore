# Capacity, profiling, chaos and owned-deployment instructions

Read these for capacity work anywhere in the repository, including Go drivers
under `../integration/`, helpers under `../../scripts/`, and Compose or
Makefile wiring. Use [README.md](README.md) for supported scenarios, selectors
and evidence contracts; do not duplicate that catalogue in root instructions.

- Use the existing `make capacity TARGET=<target> PROFILE=<scenario>
  MODE=<diagnostic|alpha|production>` interface and supported selector pairs.
  Do not add a public target for an individual experiment.
- Keep bounded work, retries, deadlines, full acceptance durations and
  resource gates. Partial or instrumented diagnostics cannot replace
  unprofiled acceptance. Offered request rate is not sustainable throughput.
- Verify API/controller concurrent-replica correctness and rolling recovery
  for the paths exercised. Exactly one Git process may be active per
  deployment; retain storage and never overlap Git replacements.
- Use explicitly named test-owned deployments, fixtures and keyspaces.
  Verify ownership before faults or cleanup. Preserve unrelated containers,
  storage, receipts and evidence; never target an operator deployment or use
  broad cleanup/pruning commands.
- The expensive retained Product/Category baseline is a reusable test asset
  through GA. Preserve its records, identities, Git storage, original refs/index
  and recovery receipts; do not reseed it or use volume-removing cleanup.
  Reuse waits for any current owner to finish its finite workload and restoration
  checks. Verify terminal receipts and sole ownership before replacing processes;
  keep private runtime identifiers outside versioned documentation.
- Reuse existing runners, stack helpers and observability wiring. Check the
  current README and implementation before prescribing flags; a proposed
  selector or an experiment-local tool is not a contributor prerequisite.
- Preflight scripts share mode parsing and matching controller-topology
  plumbing through `lib/preflight.sh`. Keep profile-specific scale, memory,
  live-process and source-verification requirements in their own preflights.
- Keep credentials, raw profiles, request content and private runtime data
  out of versioned artifacts and public evidence. Use the existing protected
  diagnostic endpoints, bounded captures and redaction checks.
- Label diagnostic overhead and measurement scope. Server-native counters
  are not individual request timings; requested sizing is not measured
  allocation. Verify actual topology, telemetry and trace joins.
- Keep run evidence attributable to the tested source, configuration,
  workload and deployment. Do not reuse stale receipts to claim a new pass,
  weaken gates to match a result, or claim unexercised faults or recovery.
- Keep experiment diaries, run IDs and private evidence paths outside
  operator runbooks. Publish validated benchmarks separately with workload,
  hardware and scope limitations.
