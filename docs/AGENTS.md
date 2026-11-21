# Documentation instructions

- Update the relevant documentation when implementing a feature.
- User-facing documentation describes supported behavior, not internal
  planning or investigation history. Read
  `../.claude/rules/no-internal-refs-in-user-facing-docs.md` before editing.
  Architecture decisions and implementation documentation retain the
  contributor-document exceptions described there.
- All runbooks are operator-facing. Read
  `../.claude/rules/operator-facing-runbooks.md`; include only material that
  helps operators deploy, configure, monitor, diagnose, recover or safely
  maintain a supported deployment.
- Do not append task progress, experiment histories, run IDs, private evidence
  paths or profiler dumps to runbooks. Keep validated benchmarks separate,
  with workload, hardware and scope limitations. Consolidate superseded
  content instead of appending another results diary.
- ADRs follow the lifecycle and acceptance dating rules in
  [ADRs/README.md](ADRs/README.md). Accept only a core decision implemented
  and in force, not merely a plan or proposed capability. Include the status
  change in the implementing PR; do not backdate acceptance before the ADR's
  own date. Superseding an ADR updates both its status and the index.
- Keep [README.md](README.md) navigation aligned with current user, operator
  and contributor guides. Put unimplemented workflows and roadmap exploration
  under `implementation/`, clearly marked as proposals; do not present their
  example commands or manifests as supported runtime procedures.
