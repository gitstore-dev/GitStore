# Release Process

This page covers how GitStore's automated release pipeline (Release Please) works, how to graduate between alpha/beta/stable, and how to troubleshoot a stuck release.

## Overview

[Release Please](https://github.com/googleapis/release-please) watches every squash-merge to `main`. It maintains a standing **release PR** that accumulates a version bump and changelog from Conventional Commit PR titles since the last release. Merging that release PR is what actually cuts a release:

1. Release Please pushes a tag (e.g. `v0.1.0-alpha.1`) and creates the GitHub Release with generated notes.
2. `.github/workflows/cd.yml`'s existing `on.push.tags: ['v*']` trigger fires independently and builds+pushes all 4 Docker images (`api`, `controller-manager`, `git-service`, `admin`) to `ghcr.io`, tagged with that exact version.

The two workflows (`release-please.yml` and `cd.yml`) are fully decoupled — `release-please.yml` never invokes or waits on `cd.yml`; it only pushes a tag, and `cd.yml`'s own trigger does the rest.

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
  - Breaking changes only just bump the prerelease counter if `minor === 0 && patch === 0`; otherwise a normal major bump + counter restart.
  - **Practical consequence**: because this repo will have ongoing `feat:` commits throughout the whole alpha phase, the version must stay minor-anchored (`0.1.0-alpha.N`, patch always `0`) for the "just bump the counter" behavior to hold indefinitely. Never manually edit the manifest to a non-zero patch while a prerelease is active — the next `feat:` commit will silently core-bump again.
- **Phase names (`alpha` → `beta` → stable) never change automatically.** Moving from one phase to the next is a deliberate human action — see Graduation below.

## Reviewing and Merging a Release PR

The release PR is titled something like `chore: release 0.1.0-alpha.4` and its diff only ever touches: `.release-please-manifest.json`, `CHANGELOG.md`, and the 4 version-marker files it keeps in sync (`gitstore-git-service/Cargo.toml`, `gitstore-admin/package.json`, `gitstore-api/internal/app/server.go`'s marker line, `gitstore-controller-manager/internal/version/version.go`'s marker line).

- **Don't hand-edit the release PR.** Release Please force-resyncs it on every subsequent push to `main` — any manual edit to its diff will be overwritten. If the proposed version or changelog content is wrong, fix it via `release-please-config.json` or a `Release-As` override (below), not by editing the PR directly.
- Merging the release PR is the only action that actually cuts a release. Nothing else in this pipeline does.

## Graduating: Alpha → Beta → Stable

This is a manual, operator-driven action — never automatic. To force the next release to a specific version regardless of what the accumulated commits would otherwise compute:

```bash
git commit --allow-empty -m "chore: release main" -m "Release-As: 0.1.0-beta.1"
git push origin main
```

(Verify the exact `Release-As:` footer syntax against the pinned `googleapis/release-please-action` version's current docs before relying on it — this convention has been stable across recent majors but confirm before your first graduation.)

Use the same mechanism for:
- **Alpha → beta**: `Release-As: 0.1.0-beta.1` (keep patch at `0` — the same minor-anchoring requirement applies to beta, since `feat:` commits will keep landing there too)
- **Beta → stable**: `Release-As: 0.1.0` (no prerelease suffix — this is what permanently flips Docker's `latest` tag behavior, see below)
- **Any one-off correction**: e.g. a bad automatic bump, or a hotfix that needs a specific version out of band.

## Docker `latest` Tag Behavior

`docker/metadata-action`'s built-in `flavor: latest=auto` does **not** do what this repo needs: by design, it never applies `latest` to a prerelease tag at all, regardless of whether a stable release exists yet (confirmed against its own docs — prerelease tags "will only extend `{{version}}` as tag"). So `cd.yml` computes this explicitly instead, in a `compute-latest-eligibility` job that runs before the 4 image builds and feeds each one's `flavor: | latest=${{ ... }}` with an explicit `true`/`false`:

- **A stable tag** (no `-alpha`/`-beta`/`-rc` suffix) is always latest-eligible.
- **A prerelease tag** is latest-eligible only if `gh release list` shows no stable (non-prerelease) release has ever been published to this repo.
- **Before any stable release exists**: `latest` tracks whichever alpha/beta build was published most recently.
- **After the first stable release** (e.g. `0.1.0`): `latest` permanently stops tracking prereleases. Any alpha/beta/rc published *after* that point will never become `latest` again — only a newer stable release can move it.
- **Practical implication for consumers**: once a stable release exists, anyone who wants "whatever's newest, prereleases included" must pin to the exact version tag (e.g. `:0.2.0-beta.1`), not `:latest`.

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
