# Release Process

This page covers how GitStore's automated release pipeline (Release Please) works, how to graduate between alpha/beta/stable, and how to troubleshoot a stuck release.

## Overview

[Release Please](https://github.com/googleapis/release-please) watches every squash-merge to `main`. It maintains a standing **release PR** that accumulates a version bump and changelog from Conventional Commit PR titles since the last release. Merging that release PR is what actually cuts a release:

1. Release Please pushes a tag (e.g. `v0.0.1-alpha.1`) and creates the GitHub Release with generated notes.
2. `.github/workflows/cd.yml`'s existing `on.push.tags: ['v*']` trigger fires independently and builds+pushes all 4 Docker images (`api`, `controller-manager`, `git-service`, `admin`) to `ghcr.io`, tagged with that exact version.

The two workflows (`release-please.yml` and `cd.yml`) are fully decoupled — `release-please.yml` never invokes or waits on `cd.yml`; it only pushes a tag, and `cd.yml`'s own trigger does the rest.

## Required One-Time Setup (manual, not in version control)

**`RELEASE_PLEASE_TOKEN` repo secret — required, or nothing downstream ever fires.** `release-please.yml` passes `token: ${{ secrets.RELEASE_PLEASE_TOKEN }}` to the action. If this secret doesn't exist, the action silently falls back to nothing and the step fails, or (if you remove the `token:` line entirely) falls back to the default `GITHUB_TOKEN` — which GitHub explicitly documents as **not triggering other workflows**: commits/tags/PRs created by the default `GITHUB_TOKEN` don't fire `on: push`/`on: pull_request` events, to prevent recursive workflow runs. Concretely, without this secret: the release PR never gets CI checks run on it, and the tag Release Please pushes on merge never triggers `cd.yml` — so no versioned Docker images ever get built.

Create either:
- A fine-grained **Personal Access Token** scoped to this repo with `contents: write` and `pull-requests: write`, stored as the `RELEASE_PLEASE_TOKEN` secret, or
- A **GitHub App** installation token (Google's recommended approach — rotates automatically, no user account tied to it), minted via `actions/create-github-app-token` and passed through as `RELEASE_PLEASE_TOKEN`.

This must be done by a repo admin before the pipeline works end to end; it can't be expressed as a file in this repo.

## Versioning Scheme

GitStore uses **one unified semantic version** for the whole project, not independent per-service versions. That single version drives the GitHub Release and all 4 Docker image tags simultaneously.

- **Prerelease numbering is dot-separated**: `0.0.1-alpha.1`, `0.0.1-alpha.2`, ... — never `0.0.1-ALPHA1`. This matters: semver compares non-numeric prerelease identifiers as whole strings, so `ALPHA10` would sort *before* `ALPHA2`. The dot makes the trailing number its own identifier, which compares numerically at any scale.
- **Only `feat`, `fix`, and breaking (`!`) commits trigger a version bump by default** (Release Please's standard type-to-bump mapping). `chore`, `docs`, `refactor`, `test`, `ci`, `build`, and `style` commits are recorded in the changelog under hidden/miscellaneous sections but don't move the version on their own.
- **During a prerelease phase** (any version with an `-alpha.N`/`-beta.N` suffix), qualifying commits only increment the trailing prerelease counter — they never bump the semver core (major/minor/patch) while a prerelease is active. That's release-please's `versioning: prerelease` config for this repo's package.
- **Phase names (`alpha` → `beta` → stable) never change automatically.** Moving from one phase to the next is a deliberate human action — see Graduation below.

## Reviewing and Merging a Release PR

The release PR is titled something like `chore: release 0.0.1-alpha.4` and its diff only ever touches: `.release-please-manifest.json`, `CHANGELOG.md`, and the 4 version-marker files it keeps in sync (`gitstore-git-service/Cargo.toml`, `gitstore-admin/package.json`, `gitstore-api/internal/app/server.go`'s marker line, `gitstore-controller-manager/internal/version/version.go`'s marker line).

- **Don't hand-edit the release PR.** Release Please force-resyncs it on every subsequent push to `main` — any manual edit to its diff will be overwritten. If the proposed version or changelog content is wrong, fix it via `release-please-config.json` or a `Release-As` override (below), not by editing the PR directly.
- Merging the release PR is the only action that actually cuts a release. Nothing else in this pipeline does.

## Graduating: Alpha → Beta → Stable

This is a manual, operator-driven action — never automatic. To force the next release to a specific version regardless of what the accumulated commits would otherwise compute:

```bash
git commit --allow-empty -m "chore: release main" -m "Release-As: 0.0.1-beta.1"
git push origin main
```

(Verify the exact `Release-As:` footer syntax against the pinned `googleapis/release-please-action` version's current docs before relying on it — this convention has been stable across recent majors but confirm before your first graduation.)

Use the same mechanism for:
- **Alpha → beta**: `Release-As: 0.0.1-beta.1`
- **Beta → stable**: `Release-As: 0.0.1` (no prerelease suffix — this is what permanently flips Docker's `latest` tag behavior, see below)
- **Any one-off correction**: e.g. a bad automatic bump, or a hotfix that needs a specific version out of band.

## Docker `latest` Tag Behavior

`docker/metadata-action`'s built-in `flavor: latest=auto` does **not** do what this repo needs: by design, it never applies `latest` to a prerelease tag at all, regardless of whether a stable release exists yet (confirmed against its own docs — prerelease tags "will only extend `{{version}}` as tag"). So `cd.yml` computes this explicitly instead, in a `compute-latest-eligibility` job that runs before the 4 image builds and feeds each one's `flavor: | latest=${{ ... }}` with an explicit `true`/`false`:

- **A stable tag** (no `-alpha`/`-beta`/`-rc` suffix) is always latest-eligible.
- **A prerelease tag** is latest-eligible only if `gh release list` shows no stable (non-prerelease) release has ever been published to this repo.
- **Before any stable release exists**: `latest` tracks whichever alpha/beta build was published most recently.
- **After the first stable release** (e.g. `0.0.1`): `latest` permanently stops tracking prereleases. Any alpha/beta/rc published *after* that point will never become `latest` again — only a newer stable release can move it.
- **Practical implication for consumers**: once a stable release exists, anyone who wants "whatever's newest, prereleases included" must pin to the exact version tag (e.g. `:0.0.2-beta.1`), not `:latest`.

## Troubleshooting

**No release PR appears after merging PRs to `main`.**
Check that at least one merged PR title since the last release was a `feat:` or `fix:` (or `!`-breaking) Conventional Commit — `chore`/`docs`/etc.-only merges don't trigger a version bump, so Release Please has nothing to propose.

**The release PR has a merge conflict.**
Usually means one of the 4 `extra-files`-tracked files (`Cargo.toml`, `package.json`, or either version-marker line) was hand-edited in a separate, competing PR on `main`. Resolution: close the release PR and let Release Please recreate it on its next run, or manually rebase it.

**A PR won't merge because of the "PR Title Lint" check.**
The title isn't a valid Conventional Commit. Retitle the PR to `type(scope): description` (see `.github/workflows/pr-title-lint.yml` for the allowed types) and the check re-runs automatically.

**I need this check to actually block merges, not just report a status.**
Adding the `PR Title Lint` workflow only makes the check run and report — it does **not** block merging by itself. A repo admin must separately add it as a **required status check** in `main`'s branch protection settings (Settings → Branches → Branch protection rules). This is a manual GitHub settings change; no file in this repo can do it.

## Related

- `.github/workflows/release-please.yml` — the release-PR/tag/release-creation workflow.
- `.github/workflows/cd.yml` — the Docker build/push pipeline, triggered independently by the tag Release Please pushes.
- `.github/workflows/pr-title-lint.yml` — Conventional Commits enforcement on PR titles.
- `release-please-config.json` / `.release-please-manifest.json` — repo root, the source of truth for the current version and per-file sync targets.
- [Docker Deployment Troubleshooting](docker-troubleshooting.md)
