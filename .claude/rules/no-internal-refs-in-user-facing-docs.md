---
paths:
  - "shared/schemas/**/*.graphqls"
  - "docs/**/*.md"
---

# No internal planning references in user-facing documentation

GraphQL schema docstrings/comments (`shared/schemas/*.graphqls`) and every page under
`docs/` are read by API consumers and operators. Do not cite internal planning artifacts there:

- spec numbers ("spec 040", "spec 047's ..."), requirement/success IDs (`FR-006`, `SC-005`), task IDs (`T041`)
- links or paths into `specs/` (`plan.md`, `tasks.md`, `research.md`, `data-model.md`, spec `quickstart.md`)
- ADR citations in schema docs (`ADR-0010 §14`)
- internal evidence/run paths (e.g. `.gitstore/spec063-...`) and notes about who is implementing what

Describe the behaviour plainly instead. Traceability belongs in Go comments, specs, and ADRs.

**Exception:** `docs/ADRs/` and `docs/implementation/` are contributor documents; references there are fine.

Before finishing an edit to these files, grep for
`\b(FR|SC|NFR)-[0-9]+|\b[Ss]pecs? ?0?[0-9]{2,3}\b|specs/[0-9]{3}|\bT[0-9]{3}\b|ADR-?[0-9]+`.
After editing `.graphqls` docstrings, run `go generate ./...` in `gitstore-api` (`root_.generated.go` embeds the schema).
