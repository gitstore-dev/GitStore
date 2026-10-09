# Release Process

This page covers how GitStore's automated release pipeline (Release Please) works, how to graduate between alpha/beta/stable, and how to troubleshoot a stuck release.

## Overview

[Release Please](https://github.com/googleapis/release-please) watches every squash-merge to `main`. It maintains a standing **release PR** that accumulates a version bump and changelog from Conventional Commit PR titles since the last release. Merging that release PR is what actually cuts a release:

1. Release Please pushes a tag (e.g. `v0.1.0-alpha.1`) and creates the GitHub Release with generated notes.
2. `.github/workflows/cd.yml`'s existing `on.push.tags: ['v*']` trigger fires independently and builds+pushes the 3 core Docker images (`api`, `controller-manager`, `git-service`) to `ghcr.io`, tagged with that exact version. `.github/workflows/cd-optional.yml` fires on the same trigger, decoupled from the core pipeline, and builds+pushes images for optional services (`admin`, `oidc-bridge`) the same way — an optional service failing to build never blocks or gates the core release.

All three workflows (`release-please.yml`, `cd.yml`, `cd-optional.yml`) are fully decoupled from each other — `release-please.yml` never invokes or waits on either CD workflow; it only pushes a tag, and each CD workflow's own trigger does the rest. `cd.yml` and `cd-optional.yml` are likewise independent of each other, by design: a build failure in the optional-images workflow must never block or gate the core release.

## Required One-Time Setup (manual, not in version control)

**A GitHub App installation token — required, or nothing downstream ever fires.** `release-please.yml` mints a fresh token via `actions/create-github-app-token` and passes it to the release-please action. Without a real token here, the action falls back to the default `GITHUB_TOKEN` — which GitHub explicitly documents as **not triggering other workflows** (its own recursion-prevention: commits/tags/PRs created by the default token don't fire `on: push`/`on: pull_request` events). Concretely, without this: the release PR never gets CI checks run on it, and the tag Release Please pushes on merge never triggers `cd.yml` — so no versioned Docker images ever get built. This is [Release Please's own documented recommendation](https://github.com/googleapis/release-please-action#github-credentials), chosen over a plain Personal Access Token specifically because GitHub App installation tokens are minted fresh (1-hour lived) on every workflow run — no manual rotation, no expiry to track, ever.

**One-time setup, by a repo admin:**

1. **Create the App**: GitHub → Settings → Developer settings → **GitHub Apps** → **New GitHub App** (org-level: `github.com/organizations/gitstore-dev/settings/apps/new`). Give it a name (e.g. `gitstore-release-bot`), any homepage URL, **uncheck "Active" under Webhook** (not needed).
2. **Permissions** (under "Repository permissions" on the same creation page — same 3 categories you were configuring for the PAT):
   - **Contents**: Read and write
   - **Issues**: Read and write
   - **Pull requests**: Read and write
   - (**Metadata**: Read-only — set automatically, required, not optional.)
3. **Generate a private key**: after creating the App, on its settings page, scroll to "Private keys" → **Generate a private key**. This downloads a `.pem` file — save it, you can't re-download it later (only generate a new one).
4. **Install the App on this repo**: on the App's settings page → **Install App** → select `gitstore-dev/GitStore` → **Only select repositories** (not "All repositories").
5. **Store 2 values in repo settings** (Settings → Secrets and variables → Actions):
   - A repository **variable** (not secret — the client ID isn't sensitive) named `RELEASE_PLEASE_APP_CLIENT_ID`, value = the App's **Client ID** (shown on the App's settings page, *not* the numeric App ID).
   - A repository **secret** named `RELEASE_PLEASE_APP_PRIVATE_KEY`, value = the full contents of the `.pem` file from step 3 (paste it as-is, including the `-----BEGIN/END-----` lines).

This must be done before the pipeline works end to end; it can't be expressed as a file in this repo.

## Versioning Scheme

GitStore uses **one unified semantic version** for the whole project, not independent per-service versions. That single version drives the GitHub Release and all 4 Docker image tags simultaneously.

- **Prerelease numbering is dot-separated**: `0.1.0-alpha.1`, `0.1.0-alpha.2`, ... — never `0.1.0-ALPHA1`. This matters: semver compares non-numeric prerelease identifiers as whole strings, so `ALPHA10` would sort *before* `ALPHA2`. The dot makes the trailing number its own identifier, which compares numerically at any scale.
- **Starts at `0.1.0-alpha.N`, not `0.0.1-alpha.N`.** This is a hard requirement of how release-please's `versioning: prerelease` strategy actually works (verified against its source, not just docs — see the next bullet), not a stylistic choice.
- **Only `feat`, `fix`, and breaking (`!`) commits trigger a version bump by default** (Release Please's standard type-to-bump mapping). `chore`, `docs`, `refactor`, `test`, `ci`, `build`, and `style` commits are recorded in the changelog under hidden/miscellaneous sections but don't move the version on their own.
- **During a prerelease phase, qualifying commits only increment the trailing prerelease counter — but only if the semver core is already anchored at the right boundary for that commit's severity.** Specifically (from `PrereleaseMinorVersionUpdate`/`PrereleaseMajorVersionUpdate`/`PrereleasePatchVersionUpdate` in release-please's source):
  - `fix:` commits always just bump the prerelease counter, regardless of the current patch value.
  - `feat:` commits only just bump the prerelease counter if `patch === 0`; otherwise they do a normal minor bump (reset patch to 0, increment minor) and restart the prerelease counter at `.0`. **This is exactly what broke the very first release attempt** — the manifest was originally seeded at `0.0.1-alpha.0` (patch `1`), so the first `feat:` commit bumped it to `0.1.0-alpha.0` instead of staying at `0.0.1-alpha.1`.
  - Breaking changes only just bump the prerelease counter if `minor === 0 && patch === 0`; otherwise, **without `bump-minor-pre-major: true` set, release-please does a normal major bump + counter restart even though `major` is still `0`.** This is exactly what happened on 2026-10 (PR #439): the manifest was at `0.1.0-alpha.3` (`minor === 1`), a legitimate breaking-change PR landed, and release-please correctly-per-its-code bumped straight to `1.0.0-alpha.3` — a real release, tagged and shipped with Docker images, well before the project was ready to leave alpha semantics.
  - **`bump-minor-pre-major: true`** (set in `release-please-config.json` since the above incident) changes this: while `version.isPreMajor` (`major === 0`), a breaking change bumps **minor** instead of major, regardless of the current minor/patch values — this is release-please's standard "pre-1.0, breaking changes don't count as `major`" semantics, matching how most pre-1.0 Conventional-Commits projects behave. `feat:`'s patch-anchoring behavior (previous bullet) is unaffected by this flag.
  - **Practical consequence**: because this repo will have ongoing `feat:` commits throughout the whole alpha phase, the version must stay minor-anchored (`0.1.0-alpha.N`, patch always `0`) for the "just bump the counter" behavior to hold indefinitely. Never manually edit the manifest to a non-zero patch while a prerelease is active — the next `feat:` commit will silently core-bump again. `bump-minor-pre-major` only protects against breaking changes bumping `major`; it does nothing for patch-anchoring.
- **Phase names (`alpha` → `beta` → stable) never change automatically.** Moving from one phase to the next is a deliberate human action — see Graduation below.

## Reviewing and Merging a Release PR

The release PR is titled something like `chore: release 0.1.0-alpha.4` and its diff only ever touches: `.release-please-manifest.json`, `CHANGELOG.md`, and the 3 version-marker files it keeps in sync (`gitstore-git-service/Cargo.toml`, `gitstore-api/internal/app/server.go`'s marker line, `gitstore-controller-manager/internal/version/version.go`'s marker line).

- **Don't hand-edit the release PR.** Release Please force-resyncs it on every subsequent push to `main` — any manual edit to its diff will be overwritten. If the proposed version or changelog content is wrong, fix it via `release-please-config.json` or a `Release-As` override (below), not by editing the PR directly.
- Merging the release PR is the only action that actually cuts a release. Nothing else in this pipeline does.

## Graduating: Alpha → Beta → Stable

This is a manual, operator-driven action — never automatic. To force the next release to a specific version regardless of what the accumulated commits would otherwise compute, push a commit carrying a `Release-As:` footer. **Direct pushes to `main` are blocked by the "PR to main" repository ruleset**, so this has to land via a merged PR, not `git push origin main` directly.

**Do not use `git commit --allow-empty` for this.** An empty commit (no file diff) merged via `gh pr merge --rebase` is silently dropped. This is specific to GitHub's server-side "Rebase and merge": it reports the PR as merged, but `main`'s tip never actually advances, so the `Release-As:` footer never reaches a commit release-please can see. (Ordinary local `git rebase` is not the same: it keeps a commit that *started* empty by default — only a commit that *becomes* empty during the rebase, because its change already exists upstream, gets dropped by default. Don't confuse the two.) This happened for real: the first attempt at this override, PR #444, "merged" successfully but added nothing to `main` — confirmed via `gh api repos/<owner>/<repo>/pulls/444` showing `merge_commit_sha` equal to the PR's own base SHA.

Instead, bundle the footer with a real, trivial content change — e.g. a dated note in this file recording the override — so the commit has a genuine diff and survives any merge strategy:

```bash
git checkout -b release-as-override
# Make some real, trivial edit here (e.g. append a dated line to this doc) —
# the point is a non-empty diff, not the specific content.
git commit -m "chore: release main" -m "Release-As: 0.1.0-beta.1"
git push -u origin release-as-override
gh pr create --title "chore: release main" --body "Release-As: 0.1.0-beta.1"
gh pr merge --rebase
```

If you genuinely need a no-file-change override, merge via `gh pr merge --merge` (a real merge commit) instead of `--rebase` or `--squash` — a merge commit always adds the original commit to `main`'s history as a parent regardless of its diff, so an empty commit survives that strategy specifically. Squash merge has the same empty-diff risk as rebase and additionally may drop the footer into the PR title/body instead of the commit `main` actually receives — avoid it for Release-As overrides either way.

(Verify the exact `Release-As:` footer syntax against the pinned `googleapis/release-please-action` version's current docs before relying on it — this convention has been stable across recent majors but confirm before your first graduation.)

Use the same mechanism for:
- **Alpha → beta**: `Release-As: 0.1.0-beta.1` (keep patch at `0` — the same minor-anchoring requirement applies to beta, since `feat:` commits will keep landing there too)
- **Beta → stable**: `Release-As: 0.1.0` (no prerelease suffix — this is what permanently flips Docker's `latest` tag behavior, see below). **Do not remove `bump-minor-pre-major` here** — `0.1.0` is still `major === 0` (a pre-1.0 stable release), so the flag still applies; removing it now would let the very next breaking-change commit jump straight to a premature `1.0.0-alpha`, recreating the incident above. Release-please's own docs describe this option as applying for the entire `0.x` line, not just prereleases. Keep it set through every `0.x` release (alpha, beta, and stable `0.y.z`) and only remove it when actually graduating to `1.0.0` — which itself should be a deliberate `Release-As: 1.0.0` override, not something an organic breaking commit is left to trigger.
- **Any one-off correction**: e.g. a bad automatic bump, or a hotfix that needs a specific version out of band.

## Docker `latest` Tag Behavior

`docker/metadata-action`'s built-in `flavor: latest=auto` does **not** do what this repo needs: by design, it never applies `latest` to a prerelease tag at all, regardless of whether a stable release exists yet (confirmed against its own docs — prerelease tags "will only extend `{{version}}` as tag"). So `cd.yml` and `cd-optional.yml` each compute this explicitly instead, in their own `compute-latest-eligibility` job that runs before that workflow's image builds and feeds each one's `flavor: | latest=${{ ... }}` with an explicit `true`/`false` (the logic is duplicated between the two workflows rather than shared, since they're deliberately independent pipelines):

- **A stable tag** (no `-alpha`/`-beta`/`-rc` suffix) is always latest-eligible.
- **A prerelease tag** is latest-eligible only if `gh release list` shows no stable (non-prerelease) release has ever been published to this repo.
- **Before any stable release exists**: `latest` tracks whichever alpha/beta build was published most recently.
- **After the first stable release** (e.g. `0.1.0`): `latest` permanently stops tracking prereleases. Any alpha/beta/rc published *after* that point will never become `latest` again — only a newer stable release can move it.
- **Practical implication for consumers**: once a stable release exists, anyone who wants "whatever's newest, prereleases included" must pin to the exact version tag (e.g. `:0.2.0-beta.1`), not `:latest`.

## Override Log

A running record of `Release-As:` overrides applied via the mechanism above, for audit purposes.

| Date       | Override           | Reason                                                                                                                                                           |
|------------|---------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------------------|
| 2026-10-04 | `0.1.0-alpha.4`     | Resets the manifest below `1.0.0` after PR #439 (`feat(secrets)!`) hit the `bump-minor-pre-major`-less `prerelease` strategy and bumped straight to `1.0.0-alpha.3` — see the breaking-change bullet above. The first attempt at this override (PR #444) used `git commit --allow-empty` and was silently dropped by rebase-merge (`main` never advanced); this entry corresponds to the corrected, non-empty-commit attempt. |

## Troubleshooting

**No release PR appears after merging PRs to `main`.**
Check that at least one merged PR title since the last release was a `feat:` or `fix:` (or `!`-breaking) Conventional Commit — `chore`/`docs`/etc.-only merges don't trigger a version bump, so Release Please has nothing to propose.

**The release PR has a merge conflict.**
Usually means one of the 4 `extra-files`-tracked files (`Cargo.toml`, `package.json`, or either version-marker line) was hand-edited in a separate, competing PR on `main`. Resolution: close the release PR and let Release Please recreate it on its next run, or manually rebase it.

**A PR won't merge because of the "PR Title Lint" check.**
The title isn't a valid Conventional Commit. Retitle the PR to `type(scope): description` (see `.github/workflows/pr-title-lint.yml` for the allowed types) and the check re-runs automatically.

**I need this check to actually block merges, not just report a status.**
Adding the `PR Title Lint` workflow only makes the check run and report — it does **not** block merging by itself. A repo admin must separately add it as a **required status check** in `main`'s branch protection settings (Settings → Branches → Branch protection rules). This is a manual GitHub settings change; no file in this repo can do it.

## Upgrading to the next release

Hand-written upgrade notes for changes that need operator or client action.
`CHANGELOG.md` is generated by Release Please from commit messages and lists
breaking changes only by title.

### Rollout order

Roll out the API before the controller manager. The controller completes
category deletion through the new `completeCategoryDeletion` mutation, which
requires the `categoryTaxonomy.purge` action on the controller role. The
reference `config/policy.yaml` and `gitstore-api/policy.yaml.example` already
grant it. If you maintain your own policy, grant `categoryTaxonomy.purge` to the
controller role before upgrading controllers, otherwise category deletions stay
in the terminating state.

Other authorization changes: `deleteCategory` is now authorized as
`categoryTaxonomy.delete` (previously `category.delete`), and `createCategory`
and `updateCategory` require `categoryTaxonomy.create` and
`categoryTaxonomy.update`.

### Breaking API changes

- Category reads are now authorized. `categories` (filtered or not) requires
  `categoryTaxonomy.list` and `category(by:)` requires `categoryTaxonomy.read`,
  both scoped to the namespace; neither was checked before. Grant them to every
  caller that reads categories, including storefront and anonymous roles and the
  controller role (its list-watcher calls `categories`). The reference
  `config/policy.yaml` controller role and the controller, developer,
  namespace-owner and anonymous roles in `gitstore-api/policy.yaml.example`
  already have them.

- `Category.path` and `Category.depth` are removed. Read
  `status.resolved.path` and `status.resolved.depth`; they are `null` until a
  category is first reconciled and are eventually consistent after an
  ancestor moves.
- `DeleteCategoryPayload.deletedCategoryId` and `orphanedProductIds` are
  replaced by `{ category, outcome }`. `outcome` is `TERMINATION_STARTED` or
  `ALREADY_TERMINATING`. `deleteCategory` now removes the manifest from Git and
  requires the `categoryTaxonomy.delete` permission (previously
  `category.delete`).
- New mutations `createCategory`, `updateCategory` and `completeCategoryDeletion`
  require `categoryTaxonomy.create`, `categoryTaxonomy.update` and
  `categoryTaxonomy.purge`. Grant `purge` to the controller manager identity
  and add the new actions to custom RBAC policies. `categories` accepts a new
  `filter` argument.
- `UpdateCategoryStatusInput.completeDeletion` is removed. Controllers complete
  category deletion through the `completeCategoryDeletion` mutation, which
  requires `categoryTaxonomy.purge`.
- Namespace mutation errors use the shared error envelope. The
  `NAMESPACE_*` codes are replaced by `ADMISSION_REJECTED`, `ALREADY_EXISTS`,
  `NOT_FOUND`, `CONFLICT` and `FAILED_PRECONDITION`, and `phase`, `reason` and
  `reasons` are replaced by `diagnostics[].reason`. Reason values are
  unchanged. See [Namespace admission](namespace-admission.md#migrating-from-the-earlier-codes).
- `RESOURCE_VERSION_CONFLICT` on status writes and `complete*Deletion` is now
  reported as code `CONFLICT` with diagnostic reason `RESOURCE_VERSION_CONFLICT`;
  the `resourceVersion` extension key is gone and the current version is in
  the message. Controllers accept both forms, so the API and controller manager
  can be rolled out in either order. Update any other client that matched the old code.
- Git pushes now reject a category whose `spec.parentRef.namespace` differs from
  its own namespace, and a change to `metadata.name` or `metadata.namespace` at
  the same file path.

### Metrics

- `gitstore_namespace_validation_rejections_total` is now labelled
  `{code,reason}` (was `{phase,reason}`).
- `gitstore_namespace_validation_duration_seconds` is now labelled `{stage}`
  (was `{phase}`), with values `STRUCTURAL` and `POLICY`.
- New: `gitstore_admission_rejections_total{kind,phase}`,
  `gitstore_category_ancestor_index_writes_total{result}` and
  `gitstore_category_ancestor_index_repair_required_total`.

### After the rollout (Scylla)

Once every API replica runs the new version, audit and repair projections so
the category subtree index covers categories written before the upgrade or by
replicas that predate it. Run from `gitstore-api/`; see
[Category ancestor index](scylla-projection-repair.md#category-ancestor-index).

```bash
go run ./cmd/gitctl scylla-projection-audit > projection-audit.json
go run ./cmd/gitctl scylla-projection-repair --dry-run > projection-repair-plan.json
go run ./cmd/gitctl scylla-projection-repair --confirm
go run ./cmd/gitctl scylla-projection-audit   # expect zero findings
```

Schema migrations apply on API startup unless
`GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false`; in that case run
`gitctl migrate` first. In-memory deployments need no extra step.

## Related

- `.github/workflows/release-please.yml` — the release-PR/tag/release-creation workflow.
- `.github/workflows/cd.yml` — the core Docker build/push pipeline (`api`, `controller-manager`, `git-service`), triggered independently by the tag Release Please pushes.
- `.github/workflows/cd-optional.yml` — the optional-services Docker build/push pipeline (`admin`, `oidc-bridge`), same trigger, fully decoupled from `cd.yml`.
- `.github/workflows/pr-title-lint.yml` — Conventional Commits enforcement on PR titles.
- `release-please-config.json` / `.release-please-manifest.json` — repo root, the source of truth for the current version and per-file sync targets.
- [Docker Deployment Troubleshooting](docker-troubleshooting.md)
