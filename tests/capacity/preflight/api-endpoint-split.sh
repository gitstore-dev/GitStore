#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
. "${root}/tests/capacity/lib/preflight.sh"
mode="$(capacity_preflight_mode)"
evidence="${1:?evidence directory required}"
[[ -n "${CAPACITY_ADMIN_URLS:-}" && -n "${CAPACITY_STOREFRONT_URLS:-}" ]] || { echo 'complete Admin and Storefront URLs are required' >&2; exit 2; }
if [[ "${mode}" == diagnostic ]]; then
  jq -n '{schemaVersion:1,passed:true,evidenceClass:"diagnostic",productionTopology:"unverified"}' >"${evidence}/preflight-environment.json"
  exit 0
fi
command -v python3 >/dev/null || { echo 'python3 is required for bounded owned capacity children' >&2;exit 2; }
"${root}/tests/capacity/preflight/api-readiness.sh" "${evidence}"
[[ -n "${CAPACITY_PROMETHEUS_URL:-}" ]] || { echo 'acceptance requires SDK/process resource history via CAPACITY_PROMETHEUS_URL' >&2;exit 2; }
IFS=',' read -r -a admin_urls <<<"${CAPACITY_ADMIN_URLS}"
IFS=',' read -r -a public_urls <<<"${CAPACITY_STOREFRONT_URLS}"
(( ${#admin_urls[@]}==2 && ${#public_urls[@]}==2 )) || { echo 'both endpoint URLs for both API replicas are required' >&2; exit 2; }
for url in "${admin_urls[@]}"; do
  [[ "${url}" == */admin/graphql ]] || exit 2
  jq -e --arg origin "${url%/admin/graphql}" 'any(.topology.liveReplicas[];.endpoint==$origin)' "${evidence}/preflight-environment.json" >/dev/null
  metrics="$(curl -fsS --max-time 5 "${url%/admin/graphql}/metrics")"
  [[ "${metrics}" == *gitstore_api_endpoint_limits* ]] || { echo 'enable shared OTLP/SDK telemetry before acceptance' >&2;exit 2; }
done
[[ "${admin_urls[0]}" != "${admin_urls[1]}" && "${public_urls[0]}" != "${public_urls[1]}" ]] || exit 2
[[ -n "${CAPACITY_OWNED_PROJECT:-}" && -n "${CAPACITY_CONTROLLER_CONTAINERS:-}" && -n "${CAPACITY_GIT_SERVICE_CONTAINER:-}" ]] || { echo 'owned project, two controllers and singleton Git are required' >&2; exit 2; }
IFS=',' read -r -a controllers <<<"${CAPACITY_CONTROLLER_CONTAINERS}"
(( ${#controllers[@]}==2 )) || exit 2
docker inspect "${controllers[@]}" "${CAPACITY_GIT_SERVICE_CONTAINER}" |
  jq '[.[] | {Id,Name,Image,Path,State:{Running:.State.Running},Config:{Image:.Config.Image,Labels:.Config.Labels}}]' >"${evidence}/connected-processes.json"
jq -e --arg project "${CAPACITY_OWNED_PROJECT}" --arg revision "$(git -C "${root}" rev-parse HEAD)" '
  length==3 and ([.[].Id]|unique|length)==3 and
  all(.[];.State.Running and .Config.Labels["com.docker.compose.project"]==$project) and
  all(.[0:2][];.Config.Labels["org.opencontainers.image.revision"]==$revision and
    ((.Config.Image | test("@sha256:[0-9a-f]{64}$")) or
      (.Config.Image==.Image and (.Image | test("^sha256:[0-9a-f]{64}$")))))
' "${evidence}/connected-processes.json" >/dev/null
# Git is unchanged by this API feature. An immutable release build may be reused
# only when its recorded revision has exactly the same build inputs. Cargo
# refreshes the root package's stale lockfile version during a build; normalize
# that metadata alone, preserving every dependency entry.
python3 - "${root}" "${evidence}/connected-processes.json" <<'PY'
import json,re,subprocess,sys
root, receipt = sys.argv[1:]
git = json.load(open(receipt))[-1]
revision = git["Config"]["Labels"].get("org.opencontainers.image.revision", "")
assert re.fullmatch(r"[0-9a-f]{40}", revision), "Git source revision is required"
assert re.search(r"@sha256:[0-9a-f]{64}$", git["Config"]["Image"]), "Git image must be digest-pinned"
def command(*args):
    return subprocess.check_output(["git","-C",root,*args])
command("diff","--exit-code",revision,"HEAD","--","gitstore-git-service", "docker/git-service.Dockerfile", "shared/proto", ":!gitstore-git-service/Cargo.lock")
normalize = lambda b: re.sub(rb'(\[\[package\]\]\nname = "git-service"\nversion = ")[^"]+(")', rb'\1<package-version>\2', b)
assert normalize(command("show",revision+":gitstore-git-service/Cargo.lock")) == normalize(command("show","HEAD:gitstore-git-service/Cargo.lock")), "Git dependency lock differs"
PY
git_count="$(docker ps --filter "label=com.docker.compose.project=${CAPACITY_OWNED_PROJECT}" --filter 'label=com.docker.compose.service=capacity-git-service' -q | wc -l | tr -d ' ')"
[[ "${git_count}" == 1 ]] || { echo 'owned project must have exactly one active Git service' >&2; exit 2; }
[[ -r "${CAPACITY_DATASET_RECEIPT:-}" ]] || { echo 'a verified retained dataset receipt is required' >&2; exit 2; }
cp "${CAPACITY_DATASET_RECEIPT}" "${evidence}/dataset.json"
jq -e '.verified==true and .products>=5000000 and .categories>=10000 and (.targets|unique|length)>=2 and (.digest|test("^[a-f0-9]{64}$"))' "${evidence}/dataset.json" >/dev/null
