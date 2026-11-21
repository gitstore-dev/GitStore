---
paths:
  - "**/runbooks/**/*.md"
---

# Runbooks are operator-facing

All existing and new runbooks are for GitStore operators, not implementation
agents or developers recording their work. Add or update only information that
helps an operator deploy, configure, monitor, diagnose, recover or safely
maintain a supported GitStore deployment.

- Include prerequisites, supported commands and configuration, symptoms,
  actionable metrics and thresholds, safety precautions, recovery steps,
  rollback procedures and verification of the outcome where relevant.
- Describe current supported behavior concisely. Explain implementation details
  only when they change an operator's decision or action.
- Do not append task progress, investigation diaries, experiment histories,
  intermediate successes or failures, run IDs, commit-by-commit comparisons,
  profiler dumps, internal planning references or private evidence paths.
- Keep implementation investigations and raw evidence outside runbooks. Publish
  validated benchmarks separately with workload, hardware, throughput, latency
  and scope limitations; link them only when useful to an operator. Never
  present a diagnostic offered rate as a sustainable capacity limit.
- When updating a runbook, consolidate or remove superseded and non-operator
  material in the affected section instead of appending another historical
  update. Do not add a results section merely because a task or test finished.

Before finishing, check every changed paragraph: what operator question does it
answer, or what operator action does it enable? Remove or relocate content that
does neither.
