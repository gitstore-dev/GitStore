# GitStore Scripts

Utility scripts for GitStore development and demonstration.

For normal repository checks, run the licence scripts through the root Make target:

```bash
make license-check
```

Use the scripts directly only when you need a narrower mode such as `--staged`.

## check-go-license-headers.sh

Validates that Go files include the required AGPL header and that changed files include the current year in their copyright line.

### Usage

```bash
# Check all tracked Go files
./scripts/check-go-license-headers.sh --all

# Check only staged added/modified Go files
./scripts/check-go-license-headers.sh --staged

# Check added/modified Go files between a base ref and HEAD
./scripts/check-go-license-headers.sh --diff-base origin/main
```

The required file header is:

```
// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (c) 2026 GitStore contributors
```

## install-git-hooks.sh

Installs repository-local git hooks into the standard `.git/hooks` directory, enabling automatic staged-file licence checks and Conventional Commits validation.

### Usage

```bash
./scripts/install-git-hooks.sh
```

This installs the `pre-commit` and `commit-msg` hooks into `.git/hooks`.

In CI, `.github/workflows/go-license-headers.yml` runs:
- `--all` checks on pushes to `main`
- `--diff-base` checks on pull requests

## check-rust-license-headers.sh

Validates that Rust files include the required AGPL header and that changed files include the current year in their copyright line.

### Usage

```bash
# Check all tracked Rust files
./scripts/check-rust-license-headers.sh --all

# Check only staged added/modified Rust files
./scripts/check-rust-license-headers.sh --staged

# Check added/modified Rust files between a base ref and HEAD
./scripts/check-rust-license-headers.sh --diff-base origin/main
```

In CI, `.github/workflows/rust-license-headers.yml` runs:
- `--all` checks on pushes to `main`
- `--diff-base` checks on pull requests

## check-js-license-headers.sh

Validates that JavaScript/TypeScript files include the required AGPL header and that changed files include the current year in their copyright line.

### Usage

```bash
# Check all tracked JS/TS files
./scripts/check-js-license-headers.sh --all

# Check only staged added/modified JS/TS files
./scripts/check-js-license-headers.sh --staged

# Check added/modified JS/TS files between a base ref and HEAD
./scripts/check-js-license-headers.sh --diff-base origin/main
```

In CI, `.github/workflows/js-license-headers.yml` runs:
- `--all` checks on pushes to `main`
- `--diff-base` checks on pull requests
