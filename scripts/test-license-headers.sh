#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
test_dir="${repo_root}/.gitstore/license-headers-test.$$"
mkdir -p "${repo_root}/.gitstore"
mkdir -m 700 "$test_dir"
trap 'rm -rf "$test_dir"' EXIT
year="$(date +%Y)"
previous=$((year - 1))
next=$((year + 1))
assertions=0

new_repo() {
  mkdir -p "$test_dir/$1/scripts"
  cd "$test_dir/$1"
  cp "$repo_root/scripts/check-license-headers.sh" scripts/
  git init -q
  git config user.name 'License checker test'
  git config user.email 'license-checker@example.invalid'
  git config commit.gpgsign false
  git config core.hooksPath /dev/null
  git -c init.defaultBranch=main commit -q --allow-empty -m baseline
  git tag baseline
}

header() {
  mkdir -p "$(dirname "$1")"
  printf '// SPDX-License-Identifier: AGPL-3.0-or-later\n// Copyright (c) %s GitStore contributors\n' \
    "$2" > "$1"
}

expect() {
  local expected="$1" needle="$2" output status=0
  shift 2
  output="$(bash scripts/check-license-headers.sh "$@" 2>&1)" || status=$?
  if [[ "$status" != "$expected" || "$output" != *"$needle"* ]]; then
    printf 'FAIL (%s): expected status=%s, message=%s\n%s\n' "$PWD" "$expected" "$needle" "$output" >&2
    exit 1
  fi
  assertions=$((assertions + 1))
}

new_repo index
for extension in go rs ts tsx js jsx mjs cjs; do
  file="source file.$extension"
  header "$file" "$year"
  git add -- "$file"
  printf 'invalid working tree\n' > "$file"
  expect 0 'passed' --staged
  git add -- "$file"
  header "$file" "$year"
  expect 1 'missing SPDX' --staged
  git add -- "$file"
done
expect 0 '8 file(s) checked' --staged
expect 0 '8 file(s) checked' --all
cp "$repo_root/.githooks/pre-commit" .git/hooks/pre-commit
chmod +x scripts/check-license-headers.sh .git/hooks/pre-commit
git commit -qm 'valid indexed sources'
header 'source file.go' "$previous"
git add 'source file.go'
if output="$(.git/hooks/pre-commit 2>&1)"; then
  echo 'pre-commit accepted an outdated staged header' >&2
  exit 1
fi
[[ "$output" == *"current year ($year)"* ]]
assertions=$((assertions + 1))

new_repo years
for copyright in "$previous" "$previous-$previous" "$next" "$previous-$next"; do
  header source.go "$copyright"
  git add source.go
  expect 0 'passed' --all
  expect 1 "current year ($year)" --staged
done
for copyright in "$year" "$previous-$year" "$year-$year"; do
  header source.go "$copyright"
  git add source.go
  expect 0 'passed' --staged
done
printf '// SPDX-License-Identifier: AGPL-3.0-or-later\n' > source.go
git add source.go
expect 1 'missing copyright line' --staged
header source.go 'not-a-year'
git add source.go
expect 1 'invalid copyright format' --staged
header source.go "$previous"
git add source.go
git commit -qm 'old source'
expect 1 "current year ($year)" --diff-base baseline
header source.go "$year"
expect 0 'passed' --diff-base baseline
expect 2 'could not collect tracked sources' --diff-base no-such-ref
expect 2 'requires a git ref' --diff-base
expect 2 'unknown argument' --unknown

new_repo renames
old=$'old\tname.go'
renamed=$'renamed\nsource.go'
header "$old" "$previous"
git add -- "$old"
git commit -qm 'old name'
git tag before-rename
git mv -- "$old" "$renamed"
expect 1 "current year ($year)" --staged
git commit -qm 'rename'
expect 1 "current year ($year)" --diff-base before-rename
header "$renamed" "$year"
git add -- "$renamed"
expect 0 '1 file(s) checked' --staged
git commit -qm 'update year'
expect 0 '1 file(s) checked' --diff-base before-rename
git rm -q -- "$renamed"
expect 0 '0 file(s) checked' --staged

new_repo renamed-outside-scope
header source.go "$previous"
git add source.go
git commit -qm source
git mv source.go notes.txt
expect 0 '0 file(s) checked' --staged
git commit -qm 'rename outside checked extensions'
expect 0 '0 file(s) checked' --diff-base baseline

new_repo generated
marker() {
  local line
  : > "$1"
  for ((line = 1; line < $2; line++)); do
    printf '// padding\n' >> "$1"
  done
  printf '// %s\n' "$3" >> "$1"
  git add -- "$1"
}
for extension in go rs js; do
  if [[ "$extension" == js ]]; then boundary=12; else boundary=8; fi
  for text in 'Code generated' 'DO NOT EDIT'; do
    marker "generated.$extension" "$boundary" "$text"
    expect 0 '0 file(s) checked' --staged
    marker "generated.$extension" "$((boundary + 1))" "$text"
    expect 1 'missing SPDX' --staged
  done
  git rm -q -f -- "generated.$extension"
done
marker generated.rs 8 '@generated'
expect 0 '0 file(s) checked' --staged
marker generated.go 8 '@generated'
expect 1 'generated.go: missing SPDX' --staged
git rm -q -f generated.go
marker generated.rs 8 'code generated'
expect 1 'generated.rs: missing SPDX' --staged
git rm -q -f generated.rs
marker generated.ts 12 'auto-generated'
expect 0 '0 file(s) checked' --staged
marker generated.go 8 'AUTO-GENERATED'
expect 1 'generated.go: missing SPDX' --staged
git rm -q -f generated.go generated.ts
for extension in go rs ts; do
  if [[ "$extension" == ts ]]; then boundary=14; else boundary=12; fi
  header "header.$extension" "$year"
  content="$(cat "header.$extension")"
  for ((line = 2; line < boundary; line++)); do
    printf '// padding\n'
  done > "header.$extension"
  printf '%s\n' "$content" >> "header.$extension"
  git add -- "header.$extension"
  expect 0 'passed' --staged
  printf '// padding\n%s\n' "$(cat "header.$extension")" > "header.$extension"
  git add -- "header.$extension"
  expect 1 'missing copyright line' --staged
  git rm -q -f -- "header.$extension"
done

new_repo upstream
mkdir -p shared/scylla-cdc-go
printf '// Original upstream attribution\n' > shared/scylla-cdc-go/reader.go
printf 'Apache License\nVersion 2.0\n' > shared/scylla-cdc-go/LICENSE.txt
git add shared
expect 0 '0 file(s) checked' --all
expect 0 '0 file(s) checked' --staged
printf 'invalid working tree\n' > shared/scylla-cdc-go/LICENSE.txt
expect 0 'passed' --staged
expect 1 'missing upstream Apache license' --all
git add shared/scylla-cdc-go/LICENSE.txt
printf 'Apache License\n' > shared/scylla-cdc-go/LICENSE.txt
expect 1 'missing upstream Apache license' --staged
git add shared/scylla-cdc-go/LICENSE.txt
git commit -qm upstream
printf 'invalid license-only change\n' > shared/scylla-cdc-go/LICENSE.txt
git add shared/scylla-cdc-go/LICENSE.txt
expect 1 'missing upstream Apache license' --staged
git rm -q -f shared/scylla-cdc-go/LICENSE.txt
expect 1 'missing upstream Apache license' --staged
git commit -qm 'delete license'
expect 1 'missing upstream Apache license' --diff-base baseline
header shared/scylla-cdc-go/application.rs "$previous"
git add shared/scylla-cdc-go/application.rs
expect 1 "current year ($year)" --staged

printf 'License checker regressions passed (%s assertions).\n' "$assertions"
