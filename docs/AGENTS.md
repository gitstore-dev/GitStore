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
- ADRs follow the lifecycle in `ADRs/README.md`. Implementing an ADR's core
  decision changes its status from `Proposed` to `Accepted (YYYY-MM-DD)` in
  the same PR. Superseding an ADR updates both its status and the index.
