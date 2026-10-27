# GitStore Development Guidelines

## Read instructions by scope

This file contains repository-wide rules. Read the applicable nested `AGENTS.md`
when working in an area below, not every instruction file upfront. Nested
instructions supplement this file. `AGENTS.md` is canonical; `CLAUDE.md` and
`GEMINI.md` are symlinks, not separately maintained copies.

| Work area | Scoped instructions |
| --- | --- |
| API, datastore, GraphQL and admission | [gitstore-api/AGENTS.md](gitstore-api/AGENTS.md) |
| Controllers, list/watch and checkpoints | [gitstore-controller-manager/AGENTS.md](gitstore-controller-manager/AGENTS.md) |
| Native Git service | [gitstore-git-service/AGENTS.md](gitstore-git-service/AGENTS.md) |
| Shared modules, schemas and protobufs | [shared/AGENTS.md](shared/AGENTS.md) |
| Test suites and integration fixtures | [tests/AGENTS.md](tests/AGENTS.md) |
| Capacity, profiling, chaos and owned deployments | [tests/capacity/AGENTS.md](tests/capacity/AGENTS.md) |
| Makefile, Compose, configuration and helper scripts | [scripts/AGENTS.md](scripts/AGENTS.md) |
| CI, image publishing and releases | [.github/AGENTS.md](.github/AGENTS.md) |
| Documentation, runbooks and ADRs | [docs/AGENTS.md](docs/AGENTS.md) |
| Feature specifications and planning | [specs/AGENTS.md](specs/AGENTS.md) |

The scope table also applies to cross-cutting work outside those directories:
for example, read the capacity instructions before changing a capacity runner
in `scripts/` or `tests/integration/`. Put new detailed guidance in the closest
applicable instruction file rather than growing this root file.

<!-- MANUAL ADDITIONS START -->
## Repository workflow

- The root `Makefile` is the canonical command interface. Extend existing
  families with `TARGET`, `PROFILE`, `DATASTORE` and other selectors, not
  feature-specific public targets. Document command changes in the relevant
  scoped instructions and README.
- Reuse existing components, helpers, tests, runners and Compose files before
  adding files, dependencies or feature-specific overlays.
- Follow existing component style and standard language conventions.
- Use `make help` for available commands. Aggregate checks are `make build`,
  `make test`, `make lint` and `make check TARGET=all`. Run `make pr-ready`
  before creating a PR; do not replace its full default suite with capacity runs.
- Install hooks once per clone with `./scripts/install-git-hooks.sh`.
- Use Conventional Commits; PR titles are CI-enforced because squash-merge
  titles become the commit messages parsed by Release Please.
- Update the relevant documentation under `docs/` when implementing a feature.

## Architecture invariants

- The Git service is stateful and singleton-only: exactly one active process
  per deployment. Shared or separate repository volumes do not provide Git HA.
  Preserve storage and use non-overlapping replacement; do not claim
  zero-downtime failover or add replication, sharding or autoscaling scope
  without explicit approval.
- API and controller plans and tests must verify concurrent-replica correctness
  and rolling upgrades per path. All core-service changes must cover pluggable
  AuthN/AuthZ, bounded work and sustained push or reconciliation load where
  applicable; these are requirements, not blanket claims of implemented HA.
- Never calculate related-resource aggregates with live datastore queries in
  GraphQL resolvers. Materialize them asynchronously into `status.resolved`.

## Tool usage

- Prefer editor-based tools for file operations; reserve terminal commands
  primarily for build, lint and test workflows.
- When `graphify-out/graph.json` exists, first use `graphify query "<question>"`
  for codebase questions, `graphify path "<A>" "<B>"` for relationships or
  `graphify explain "<concept>"` for focused concepts.
- Use `graphify-out/wiki/index.md`, when present, for broad navigation.
  Read `GRAPH_REPORT.md` only for broad architecture review or when scoped
  queries are insufficient.
- After code changes, run `graphify update .` to refresh the AST-only graph.
<!-- MANUAL ADDITIONS END -->

<!-- SPECKIT START -->
For feature-specific technologies, structure and implementation context, read
the applicable plan; the current planning reference is
`specs/064-authz-action-vocabulary/plan.md`. Planning history and technology
baselines belong in `specs/AGENTS.md`, not this repository-wide entry point.
<!-- SPECKIT END -->
