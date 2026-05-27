# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

.DEFAULT_GOAL := help

ROOT := $(CURDIR)
API_DIR := $(ROOT)/gitstore-api
GIT_SERVICE_DIR := $(ROOT)/gitstore-git-service

API_ENV_FILE ?= $(API_DIR)/.env
GIT_DATA_DIR ?= $(ROOT)/.gitstore/repos
DIFF_BASE ?= origin/main

COMPOSE_BAKE ?= true
DETACH_FLAG := $(if $(filter 1 true yes,$(DETACH)),-d,)
SERVICE ?=

API_URL ?= http://localhost:4000/graphql
ADMIN_USERNAME ?= admin
ADMIN_PASSWORD ?=
BOOTSTRAP_TOKEN ?=
BOOTSTRAP_TOKEN_CACHE ?= $(ROOT)/.gitstore/bootstrap-token
NAMESPACE ?= gitstore
NAMESPACE_DISPLAY_NAME ?= GitStore
NAMESPACE_TIER ?= USER
REPOSITORY ?= catalog
DEFAULT_BRANCH ?= main

export API_URL ADMIN_USERNAME ADMIN_PASSWORD BOOTSTRAP_TOKEN BOOTSTRAP_TOKEN_CACHE
export NAMESPACE NAMESPACE_DISPLAY_NAME NAMESPACE_TIER REPOSITORY DEFAULT_BRANCH

.PHONY: help git api dev compose scylla compose-scylla ps logs stop down
.PHONY: build test lint license-check pr-ready
.PHONY: bootstrap bootstrap-token bootstrap-namespace bootstrap-repository git-clean-data
.PHONY: admin-compose admin-down admin-stop admin-logs bootstrap-tools

help: ## Show available targets and common variables.
	@awk 'BEGIN {FS = ":.*##"; printf "GitStore make targets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nCommon variables:\n"
	@printf "  DETACH=1                  Run compose start targets in the background\n"
	@printf "  COMPOSE_BAKE=true         Compose build bake setting for Docker Compose\n"
	@printf "  SERVICE=<name>            Limit logs/stop to one compose service\n"
	@printf "  GIT_DATA_DIR=%s\n" "$(GIT_DATA_DIR)"
	@printf "  API_URL=%s\n" "$(API_URL)"
	@printf "  ADMIN_USERNAME=%s\n" "$(ADMIN_USERNAME)"
	@printf "  ADMIN_PASSWORD=<password> Required for login unless BOOTSTRAP_TOKEN or cached token is available\n"
	@printf "  BOOTSTRAP_TOKEN=<token>   Use an existing bearer token for bootstrap\n"
	@printf "  NAMESPACE=%s REPOSITORY=%s DEFAULT_BRANCH=%s\n" "$(NAMESPACE)" "$(REPOSITORY)" "$(DEFAULT_BRANCH)"

git: ## Run gitstore-git-service locally in the foreground.
	@mkdir -p "$(GIT_DATA_DIR)"
	@cd "$(GIT_SERVICE_DIR)" && GITSTORE_GIT__DATA_DIR="$(GIT_DATA_DIR)" cargo run --bin git-service

api: ## Run gitstore-api locally in the foreground.
	@if [ ! -f "$(API_ENV_FILE)" ] && { [ -z "$${GITSTORE_AUTH__ADMIN__USERNAME:-}" ] || [ -z "$${GITSTORE_AUTH__ADMIN__PASSWORD_HASH:-}" ] || [ -z "$${GITSTORE_AUTH__JWT__SECRET:-}" ]; }; then \
		echo "make api requires $(API_ENV_FILE) or shell env for GITSTORE_AUTH__ADMIN__USERNAME, GITSTORE_AUTH__ADMIN__PASSWORD_HASH, and GITSTORE_AUTH__JWT__SECRET"; \
		exit 2; \
	fi
	@cd "$(API_DIR)" && go run ./cmd/server

dev: ## Run local git service and API together in the foreground.
	@if [ ! -f "$(API_ENV_FILE)" ] && { [ -z "$${GITSTORE_AUTH__ADMIN__USERNAME:-}" ] || [ -z "$${GITSTORE_AUTH__ADMIN__PASSWORD_HASH:-}" ] || [ -z "$${GITSTORE_AUTH__JWT__SECRET:-}" ]; }; then \
		echo "make dev requires $(API_ENV_FILE) or shell env for GITSTORE_AUTH__ADMIN__USERNAME, GITSTORE_AUTH__ADMIN__PASSWORD_HASH, and GITSTORE_AUTH__JWT__SECRET"; \
		exit 2; \
	fi
	@set -u; \
	mkdir -p "$(GIT_DATA_DIR)"; \
	tmp=$$(mktemp -d); \
	fifo="$$tmp/done"; \
	mkfifo "$$fifo"; \
	cleanup() { \
		trap - INT TERM EXIT; \
		[ -n "$${git_pid:-}" ] && kill "$$git_pid" 2>/dev/null || true; \
		[ -n "$${api_pid:-}" ] && kill "$$api_pid" 2>/dev/null || true; \
		[ -n "$${git_pid:-}" ] && wait "$$git_pid" 2>/dev/null || true; \
		[ -n "$${api_pid:-}" ] && wait "$$api_pid" 2>/dev/null || true; \
		rm -rf "$$tmp"; \
	}; \
	trap 'cleanup; exit 130' INT; \
	trap 'cleanup; exit 143' TERM; \
	trap 'cleanup' EXIT; \
	( set +e; \
		cd "$(GIT_SERVICE_DIR)" || { printf 'git-service 1\n' > "$$fifo"; exit 0; }; \
		GITSTORE_GIT__DATA_DIR="$(GIT_DATA_DIR)" cargo run --bin git-service & child=$$!; \
		trap 'kill "$$child" 2>/dev/null; wait "$$child" 2>/dev/null; exit 143' INT TERM; \
		wait "$$child"; status=$$?; \
		printf 'git-service %s\n' "$$status" > "$$fifo"; \
	) & git_pid=$$!; \
	( set +e; \
		cd "$(API_DIR)" || { printf 'api 1\n' > "$$fifo"; exit 0; }; \
		go run ./cmd/server & child=$$!; \
		trap 'kill "$$child" 2>/dev/null; wait "$$child" 2>/dev/null; exit 143' INT TERM; \
		wait "$$child"; status=$$?; \
		printf 'api %s\n' "$$status" > "$$fifo"; \
	) & api_pid=$$!; \
	read service status < "$$fifo"; \
	echo "$$service exited with status $$status"; \
	cleanup; \
	trap - EXIT; \
	exit "$$status"

compose: ## Run API and git service with Docker Compose.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml up --build $(DETACH_FLAG)

scylla: ## Run only local Scylla services with Docker Compose.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml up $(DETACH_FLAG) scylla scylla-init

compose-scylla: ## Run API, git service, and Scylla with Docker Compose.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml up --build $(DETACH_FLAG)

ps: ## Show compose service status.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml -f compose.admin.yml ps

logs: ## Follow compose logs; optionally pass SERVICE=<name>.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml -f compose.admin.yml logs -f $(SERVICE)

stop: ## Stop compose services; optionally pass SERVICE=<name>.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml -f compose.admin.yml stop $(SERVICE)

down: ## Stop and remove compose services and networks.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.scylla.yml -f compose.admin.yml down

build: ## Build Rust and Go services.
	@cd "$(GIT_SERVICE_DIR)" && cargo build --verbose
	@cd "$(API_DIR)" && go build -v ./...

test: ## Run Rust and Go test suites.
	@cd "$(GIT_SERVICE_DIR)" && cargo test --verbose
	@cd "$(API_DIR)" && go test -count=1 -v -race -coverprofile=coverage.txt -covermode=atomic ./...

lint: ## Run Rust formatting/clippy and Go formatting/vet/staticcheck.
	@cd "$(GIT_SERVICE_DIR)" && cargo fmt --all -- --check
	@cd "$(GIT_SERVICE_DIR)" && cargo clippy --all-targets --all-features -- -D warnings
	@if [ "$$(cd "$(API_DIR)" && gofmt -s -l . | wc -l | tr -d ' ')" != "0" ]; then \
		echo "The following files need formatting:"; \
		cd "$(API_DIR)" && gofmt -s -l .; \
		exit 1; \
	fi
	@cd "$(API_DIR)" && go vet ./...
	@cd "$(API_DIR)" && go install honnef.co/go/tools/cmd/staticcheck@latest
	@cd "$(API_DIR)" && "$$(go env GOPATH)"/bin/staticcheck ./...

license-check: ## Run Go, Rust, and JS/TS license header checks.
	@./scripts/check-go-license-headers.sh --all
	@./scripts/check-go-license-headers.sh --diff-base "$(DIFF_BASE)"
	@./scripts/check-rust-license-headers.sh --all
	@./scripts/check-rust-license-headers.sh --diff-base "$(DIFF_BASE)"
	@./scripts/check-js-license-headers.sh --all
	@./scripts/check-js-license-headers.sh --diff-base "$(DIFF_BASE)"

pr-ready: lint build test license-check ## Run the full PR readiness workflow.

bootstrap: bootstrap-namespace bootstrap-repository ## Create the default namespace and repository through the API.

bootstrap-tools:
	@command -v curl >/dev/null 2>&1 || { echo "curl is required for bootstrap targets"; exit 127; }
	@command -v jq >/dev/null 2>&1 || { echo "jq is required for bootstrap targets"; exit 127; }

bootstrap-token: bootstrap-tools ## Login and print/cache a bootstrap bearer token.
	@if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
		echo "ADMIN_PASSWORD is required for bootstrap-token"; \
		exit 2; \
	fi
	@mkdir -p "$$(dirname "$${BOOTSTRAP_TOKEN_CACHE}")"
	@query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { session { token } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	token=$$(echo "$$response" | jq -er '.data.login.session.token // empty') || { \
		echo "Login response did not contain a token. Check ADMIN_USERNAME, ADMIN_PASSWORD, and API_URL."; \
		exit 1; \
	}; \
	printf '%s\n' "$$token"; \
	printf '%s\n' "$$token" > "$${BOOTSTRAP_TOKEN_CACHE}"; \
	echo "Token cached at $${BOOTSTRAP_TOKEN_CACHE}" >&2

bootstrap-namespace: bootstrap-tools ## Create only the bootstrap namespace.
	@set -u; \
	token="$${BOOTSTRAP_TOKEN:-}"; \
	if [ -z "$$token" ] && [ -f "$${BOOTSTRAP_TOKEN_CACHE}" ]; then token=$$(cat "$${BOOTSTRAP_TOKEN_CACHE}"); fi; \
	if [ -z "$$token" ]; then \
		if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
			echo "ADMIN_PASSWORD is required unless BOOTSTRAP_TOKEN is provided or $${BOOTSTRAP_TOKEN_CACHE} exists"; \
			exit 2; \
		fi; \
		query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { session { token } } }'; \
		payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
		response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
			echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
			exit 1; \
		}; \
		if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
			echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
			exit 1; \
		fi; \
		token=$$(echo "$$response" | jq -er '.data.login.session.token // empty') || { echo "Login response did not contain a token."; exit 1; }; \
	fi; \
	query='mutation CreateNamespace($$identifier: String!, $$displayName: String, $$tier: NamespaceTier!) { createNamespace(input: { identifier: $$identifier, displayName: $$displayName, tier: $$tier }) { namespace { id identifier tier } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg identifier "$${NAMESPACE}" --arg displayName "$${NAMESPACE_DISPLAY_NAME}" --arg tier "$${NAMESPACE_TIER}" '{query: $$query, variables: {identifier: $$identifier, displayName: $$displayName, tier: $$tier}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' -H "Authorization: Bearer $$token" --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	echo "$$response" | jq -r '.data.createNamespace.namespace | "Created namespace \(.identifier) (\(.id))"'

bootstrap-repository: bootstrap-tools ## Create only the bootstrap repository; namespace must already exist.
	@set -u; \
	token="$${BOOTSTRAP_TOKEN:-}"; \
	if [ -z "$$token" ] && [ -f "$${BOOTSTRAP_TOKEN_CACHE}" ]; then token=$$(cat "$${BOOTSTRAP_TOKEN_CACHE}"); fi; \
	if [ -z "$$token" ]; then \
		if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
			echo "ADMIN_PASSWORD is required unless BOOTSTRAP_TOKEN is provided or $${BOOTSTRAP_TOKEN_CACHE} exists"; \
			exit 2; \
		fi; \
		query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { session { token } } }'; \
		payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
		response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
			echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
			exit 1; \
		}; \
		if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
			echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
			exit 1; \
		fi; \
		token=$$(echo "$$response" | jq -er '.data.login.session.token // empty') || { echo "Login response did not contain a token."; exit 1; }; \
	fi; \
	query='query Namespace($$identifier: String!) { namespace(by: { identifier: $$identifier }) { id identifier } }'; \
	payload=$$(jq -n --arg query "$$query" --arg identifier "$${NAMESPACE}" '{query: $$query, variables: {identifier: $$identifier}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' -H "Authorization: Bearer $$token" --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	namespace_id=$$(echo "$$response" | jq -er '.data.namespace.id // empty') || { \
		echo "Namespace \"$${NAMESPACE}\" was not found. Run make bootstrap-namespace first."; \
		exit 1; \
	}; \
	query='mutation CreateRepository($$namespaceId: ID!, $$name: String!, $$defaultBranch: String!) { createRepository(input: { namespaceId: $$namespaceId, name: $$name, defaultBranch: $$defaultBranch }) { repository { id name defaultBranch storagePath namespace { identifier } } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg namespaceId "$$namespace_id" --arg name "$${REPOSITORY}" --arg defaultBranch "$${DEFAULT_BRANCH}" '{query: $$query, variables: {namespaceId: $$namespaceId, name: $$name, defaultBranch: $$defaultBranch}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' -H "Authorization: Bearer $$token" --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	echo "$$response" | jq -r '.data.createRepository.repository | .storagePath as $$path | ($$path | split("/")[-1] | sub("\\.git$$"; "")) as $$repoId | "Created repository \(.namespace.identifier)/\(.name) (\(.id))\nClone URL: http://localhost:9418/\($$repoId)"'

git-clean-data: ## Remove native local git-service repository data; requires CONFIRM=1.
	@if [ "$(CONFIRM)" != "1" ]; then \
		echo "Refusing to remove $(GIT_DATA_DIR). Re-run with CONFIRM=1."; \
		exit 2; \
	fi
	@if [ -z "$(GIT_DATA_DIR)" ] || [ "$(GIT_DATA_DIR)" = "/" ]; then \
		echo "Refusing to remove unsafe GIT_DATA_DIR=$(GIT_DATA_DIR)"; \
		exit 2; \
	fi
	@rm -rf "$(GIT_DATA_DIR)"

admin-compose: ## Run the optional admin compose stack.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.admin.yml up --build $(DETACH_FLAG) admin

admin-down: ## Stop and remove the admin compose stack.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.admin.yml down

admin-stop: ## Stop only the admin compose service.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.admin.yml stop admin

admin-logs: ## Follow admin compose logs.
	@COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f compose.admin.yml logs -f admin
