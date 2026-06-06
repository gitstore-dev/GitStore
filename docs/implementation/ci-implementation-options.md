# CI Implementation Options for GitStore Actions

**Date**: 2026-06-06
**Status**: Exploration

## Goal

Evaluate CI engines and runner architectures that could be adapted for GitStore's
future GitStore Actions feature.

GitStore stores catalogue state in Git repositories and already has a native
Git service, hook pipeline, API, and Kubernetes-oriented controller work. A CI
implementation should fit that shape: repository events trigger isolated jobs,
job state is observable through GitStore APIs, and older local-first workflows
remain possible.

## Baseline: `act`

[`act`](https://github.com/nektos/act) runs GitHub Actions workflows locally.
It is useful as a compatibility reference because it understands much of the
GitHub Actions YAML model and runs jobs in containers.

For GitStore, `act` is a reasonable starting point for experiments, but it is
not a full CI control plane. GitStore would still need scheduling, job state,
secrets, logs, artifacts, status reporting, cancellation, retries, concurrency
control, and repository event integration.

## Implementation Candidates

| Option | Fit for GitStore | Pros | Cons |
| --- | --- | --- | --- |
| Gitea / Forgejo Actions runners | High if GitStore wants GitHub Actions-like UX | Already adapted for self-hosted Git forges; workflow syntax is close to GitHub Actions; runner registration model exists | Still largely `act`-family behavior; compatibility gaps vs real GitHub Actions; would require adapting Gitea/Forgejo APIs to GitStore |
| Woodpecker CI | High | Lightweight Go CI; designed around Git forges; server/agent model; container-native; simpler than Jenkins/GitLab | Uses Woodpecker/Drone-style pipeline YAML, not GitHub Actions; GitStore would need a forge adapter, status API, secrets, logs, and artifacts integration |
| Tekton Pipelines | High if GitStore is Kubernetes-first | Kubernetes-native; strong fit with `gitstore-controller-manager`; each task runs as pods; good isolation and scale model | Not a complete forge CI product; GitStore must build pipeline UX, eventing, logs, status, secrets, and YAML-to-CRD translation |
| Argo Workflows | Medium-high for Kubernetes batch workflows | Mature Kubernetes workflow engine; good DAG support; good for heavier workflows | Less CI-specific than Tekton; GitStore still owns CI semantics, checkout, statuses, logs, artifacts, and secrets |
| GitLab Runner | Medium | Very mature runner; supports shell, Docker, Kubernetes, and custom executors; `.gitlab-ci.yml` is widely known | Runner expects GitLab coordinator APIs; adapting means implementing enough GitLab Runner API or forking; syntax/ecosystem becomes GitLab-specific |
| Concourse CI | Medium | Clean resource model; reproducible container tasks; strong external-state model | Separate CI system rather than embeddable runner; GitStore would need a GitStore resource/plugin; less familiar UX |
| Dagger | Medium as execution backend | Programmable pipelines in Go/Python/TypeScript and other SDKs; local-first; container-based execution | Not a CI coordinator by itself; GitStore must provide scheduler, triggers, secrets, logs, artifacts, and statuses |
| Buildkite-style agent model | Medium as architecture inspiration | Strong split between control plane and self-hosted agents; dynamic pipelines; good operational model | Buildkite itself is SaaS/commercial control plane; useful to integrate with, less useful to embed directly |
| Zuul | Medium-low unless GitStore needs gated merge queues | Excellent dependency-aware gating and cross-repo testing | Complex; strongest with Gerrit/GitHub-style review workflows; likely overkill before GitStore has first-class PR/change review |
| Jenkins | Low as embedded CI, medium as external integration | Huge plugin ecosystem; familiar to enterprises | Heavy operational burden; large plugin/security surface; not a clean foundation for GitStore-native CI |

## Recommended Direction

### GitStore-Native Path: Tekton

Tekton is the best low-level execution substrate if GitStore continues to lean
into Kubernetes. GitStore can own the product model:

1. Git push, tag, or repository event arrives.
2. GitStore resolves the repository, ref, and workflow definition.
3. GitStore creates a `PipelineRun` or equivalent CRD.
4. Tekton schedules isolated pod-based tasks.
5. GitStore records job state, logs, artifacts, and commit statuses.

This approach fits the existing controller-manager direction and keeps GitStore
from building a container scheduler from scratch.

### Faster Self-Hosted CI Product Path: Woodpecker-Style Architecture

Woodpecker is a strong reference for a forge-oriented CI product. Its split
between server and agents maps well to GitStore:

1. GitStore API acts as the CI coordinator.
2. Agents poll for work or receive assignments.
3. Jobs run in containers.
4. GitStore stores logs, artifacts, and status.

This is likely simpler to ship than a full Kubernetes-native CI if GitStore
wants a local-first, Docker-friendly feature before committing to a cluster
runtime.

## Design Considerations for GitStore

### Workflow Syntax

GitStore needs to decide whether workflow files should use:

- GitHub Actions-compatible YAML, using `act`/Gitea/Forgejo behavior as a
  compatibility target.
- Woodpecker/Drone-style YAML, which is simpler but less familiar to GitHub
  users.
- GitStore-native Kubernetes-style resources, matching the catalogue
  frontmatter model.
- A translation layer from GitHub Actions-like YAML to Tekton or another engine.

The safest initial approach is to support a minimal GitStore workflow schema and
avoid promising full GitHub Actions compatibility until compatibility tests
prove it.

### Execution Isolation

CI jobs must not run with direct write access to repository storage. Jobs should
receive a clone/fetch checkout over Git transport or through a controlled
workspace volume. Secrets should be injected only into the jobs that request
them and should never be persisted into logs or artifacts.

### Status and Observability

GitStore should expose:

- Workflow run state.
- Per-job state.
- Per-step logs.
- Commit/ref status.
- Artifact metadata.
- Cancellation and retry state.

These should be first-class GitStore concepts even when the execution backend is
external.

### Local-First Behavior

The CI design should not force Kubernetes for local development. A practical
shape is:

- Local mode: Docker/container runner or `act`-style execution.
- Production mode: Kubernetes/Tekton or registered remote agents.

## External Integration Option

GitStore can also integrate with external CI systems instead of embedding one.
This path is useful for early production adoption:

- Emit webhooks for push/tag/repository events.
- Provide commit status APIs.
- Provide repository clone URLs and short-lived credentials.
- Let Jenkins, Buildkite, Woodpecker, GitLab CI, or other systems run jobs.

This does not provide GitStore-native CI, but it reduces immediate scope and
lets users keep existing automation.

## Open Questions

- Should GitStore optimize for GitHub Actions compatibility or define a smaller
  native workflow format?
- Should the first implementation be local Docker-based, Kubernetes-based, or
  both behind the same API?
- Where should logs and artifacts live: GitStore API storage, object storage, or
  backend-specific storage?
- How should workflow permissions map to GitStore namespaces and repositories?
- Should workflow execution be triggered only by Git pushes, or also by
  catalogue object changes after validation?

## References

- `act`: <https://github.com/nektos/act>
- Gitea Actions: <https://docs.gitea.com/usage/actions/quickstart>
- Forgejo Actions: <https://forgejo.org/docs/latest/user/actions/overview/>
- Woodpecker CI: <https://woodpecker-ci.org/docs/3.10/usage/intro>
- Tekton Pipelines concepts: <https://tekton.dev/docs/concepts/concept-model/>
- Argo Workflows: <https://argoproj.github.io/workflows/>
- GitLab Runner: <https://docs.gitlab.com/ci/runners/>
- Concourse CI: <https://concourse-ci.org/docs/>
- Dagger: <https://docs.dagger.io/>
- Jenkins Pipeline: <https://www.jenkins.io/doc/book/pipeline/>
