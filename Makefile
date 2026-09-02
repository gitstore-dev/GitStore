# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

.DEFAULT_GOAL := help

ROOT := $(CURDIR)
API_DIR := $(ROOT)/gitstore-api
CONTROLLER_MANAGER_DIR := $(ROOT)/gitstore-controller-manager
GIT_SERVICE_DIR := $(ROOT)/gitstore-git-service
GO_MODULE_DIRS := $(API_DIR) $(CONTROLLER_MANAGER_DIR)

API_ENV_FILE ?= $(API_DIR)/.env
CONFIG_FILE ?= ./config/config.toml
AUTH_CONFIG_DIR ?= $(ROOT)/config
POLICY_FILE ?= $(AUTH_CONFIG_DIR)/policy.yaml
USERS_FILE ?= $(AUTH_CONFIG_DIR)/users.yaml
LOCAL_COMPOSE = CONFIG_FILE="$(abspath $(CONFIG_FILE))" COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose --profile local -f compose.yml -f compose.local.yml
LIFECYCLE_COMPOSE = $(LOCAL_COMPOSE) -f compose.scylla.yml -f compose.scylla.cluster.yml -f compose.admin.yml
GIT_DATA_DIR ?= $(ROOT)/.gitstore/repos
DIFF_BASE ?= origin/main

COMPOSE_BAKE ?= true
DATASTORE ?= memdb
PROFILE ?= single
SCYLLA_CLUSTER_SMP ?= 1
SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS ?= 2048
SCYLLA_COMPOSE_FILE = $(if $(filter cluster,$(PROFILE)),compose.scylla.cluster.yml,compose.scylla.yml)
DATASTORE_COMPOSE_FILE = $(if $(filter scylla,$(DATASTORE)),-f $(SCYLLA_COMPOSE_FILE),)
SCYLLA_SERVICES = $(if $(filter cluster,$(PROFILE)),scylla-1 scylla-2 scylla-3 scylla-init,scylla scylla-init)
SCYLLA_LIFECYCLE_SERVICES = scylla scylla-1 scylla-2 scylla-3 scylla-init
COMPOSE_SERVICE = $(if $(filter scylla,$(SERVICE)),$(SCYLLA_LIFECYCLE_SERVICES),$(SERVICE))
DETACH_FLAG := $(if $(filter 1 true yes,$(DETACH)),-d,)
SERVICE ?=

API_URL ?= http://localhost:4000/graphql
ADMIN_USERNAME ?= admin
ADMIN_PASSWORD ?=
BOOTSTRAP_TOKEN ?=
BOOTSTRAP_TOKEN_CACHE ?= $(ROOT)/.gitstore/bootstrap-token
NAMESPACE ?= gitstore-test
NAMESPACE_DISPLAY_NAME ?= GitStore Test
NAMESPACE_TIER ?= USER
REPOSITORY ?= catalog
DEFAULT_BRANCH ?= main
SCYLLA_TEST_ADDR ?= 127.0.0.1:9042
SCYLLA_CAPACITY_PRODUCTS ?= 5000000
SCYLLA_CAPACITY_CONCURRENCY ?= 32
SCYLLA_CAPACITY_DURATION ?= 10m
NAMESPACE_CAPACITY_DURATION ?= 30m
NAMESPACE_WATCH_CAPACITY_DURATION ?= 60m
NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS ?= 1000
NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS ?= 10000
NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES ?= 20
NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL ?= 1m
NAMESPACE_WATCH_CAPACITY_BURST_SIZE ?= 100
NAMESPACE_WATCH_CAPACITY_MUTATION_WORKERS ?= 20
NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY ?=
NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS ?= 0
NAMESPACE_WATCH_API_A ?= http://localhost:4000
NAMESPACE_WATCH_API_B ?= http://localhost:4001
NAMESPACE_WATCH_API_REPLACEMENT ?=
NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE ?=
NAMESPACE_WATCH_TOKEN ?=
NAMESPACE_WATCH_TOKEN_FILE ?=
NAMESPACE_WATCH_OVERFLOW_TRANSITIONS ?=
CAPACITY_PROFILE ?= api-readiness
CAPACITY_EVIDENCE_DIR ?= $(ROOT)/.gitstore/capacity
CAPACITY_ENV_FILE ?=
CAPACITY_TOKEN_FILE ?=
CAPACITY_CONFIG_MANIFEST ?=
CAPACITY_ENVIRONMENT_MANIFEST ?=
K6_IMAGE ?= grafana/k6:2.1.0
CHAOS_PROFILE ?= api-restart
CHAOS_TARGET ?=
CHAOS_CONFIRM ?= 0
CHAOS_EVIDENCE_DIR ?= $(ROOT)/.gitstore/chaos
PUMBA_IMAGE ?= ghcr.io/alexei-led/pumba:1.1.7
CAPACITY_CHAOS_PROFILE ?=
CAPACITY_CHAOS_TARGET ?=
CAPACITY_CHAOS_DELAY ?=30s
CAPACITY_CHAOS_CONFIRM ?=0

export API_URL ADMIN_USERNAME ADMIN_PASSWORD BOOTSTRAP_TOKEN BOOTSTRAP_TOKEN_CACHE
export NAMESPACE NAMESPACE_DISPLAY_NAME NAMESPACE_TIER REPOSITORY DEFAULT_BRANCH

.PHONY: help git api controller dev compose scylla ps logs stop down validate-local-config compose-config-check
.PHONY: build test lint license-check credential-output-check credential-leakage-check pr-ready capacity chaos test-scylla-hardening test-scylla-integration test-scylla-capacity test-namespace-admission-capacity test-namespace-watch-capacity test-namespace-watch-recovery
.PHONY: bootstrap bootstrap-token bootstrap-namespace bootstrap-repository git-clean-data
.PHONY: admin-compose admin-down admin-stop admin-logs bootstrap-tools add-user add-role assign-role hash-user-password gen-jwt-secret gen-hmac-secret

help: ## Show available targets and common variables.
	@awk 'BEGIN {FS = ":.*##"; printf "GitStore make targets:\n"} /^[a-zA-Z0-9_.-]+:.*##/ {printf "  %-22s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
	@printf "\nCommon variables:\n"
	@printf "  DETACH=1                  Run compose start targets in the background\n"
	@printf "  DATASTORE=%s              Compose datastore: memdb or scylla\n" "$(DATASTORE)"
	@printf "  COMPOSE_BAKE=true         Compose build bake setting for Docker Compose\n"
	@printf "  PROFILE=%s                Scylla profile: single or cluster\n" "$(PROFILE)"
	@printf "  SERVICE=<name>            Limit logs/stop to one compose service; SERVICE=scylla includes all Scylla variants\n"
	@printf "  SCYLLA_COMPOSE_FILE=%s  Derived Scylla overlay used by scylla and compose DATASTORE=scylla\n" "$(SCYLLA_COMPOSE_FILE)"
	@printf "  SCYLLA_CLUSTER_SMP=%s     CPU shards per Scylla node for PROFILE=cluster\n" "$(SCYLLA_CLUSTER_SMP)"
	@printf "  SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS=%s  Networking AIO blocks per cluster node\n" "$(SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS)"
	@printf "  GIT_DATA_DIR=%s\n" "$(GIT_DATA_DIR)"
	@printf "  CONFIG_FILE=%s        Shared local-profile configuration\n" "$(CONFIG_FILE)"
	@printf "  API_URL=%s\n" "$(API_URL)"
	@printf "  ADMIN_USERNAME=%s\n" "$(ADMIN_USERNAME)"
	@printf "  ADMIN_PASSWORD=<password> Required for login unless BOOTSTRAP_TOKEN or cached token is available\n"
	@printf "  SCYLLA_TEST_ADDR=%s\n" "$(SCYLLA_TEST_ADDR)"
	@printf "  SCYLLA_CAPACITY_PRODUCTS=%s\n" "$(SCYLLA_CAPACITY_PRODUCTS)"
	@printf "  SCYLLA_CAPACITY_CONCURRENCY=%s\n" "$(SCYLLA_CAPACITY_CONCURRENCY)"
	@printf "  SCYLLA_CAPACITY_DURATION=%s\n" "$(SCYLLA_CAPACITY_DURATION)"
	@printf "  NAMESPACE_CAPACITY_DURATION=%s\n" "$(NAMESPACE_CAPACITY_DURATION)"
	@printf "  NAMESPACE_WATCH_CAPACITY_DURATION=%s\n" "$(NAMESPACE_WATCH_CAPACITY_DURATION)"
	@printf "  NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS=%s\n" "$(NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS)"
	@printf "  NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS=%s NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES=%s\n" "$(NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS)" "$(NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES)"
	@printf "  CAPACITY_PROFILE=%s        Reusable k6 capacity profile\n" "$(CAPACITY_PROFILE)"
	@printf "  CHAOS_PROFILE=%s           Reusable Pumba fault profile\n" "$(CHAOS_PROFILE)"
	@printf "  CHAOS_TARGET=<name>        Explicit GitStore container targeted by chaos\n"
	@printf "  NAMESPACE_WATCH_API_A=%s NAMESPACE_WATCH_API_B=%s\n" "$(NAMESPACE_WATCH_API_A)" "$(NAMESPACE_WATCH_API_B)"
	@printf "  NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE=<path> Required coordination signal for an actual replica restart\n"
	@printf "  NAMESPACE_WATCH_TOKEN=<token> Required for the cross-replica Namespace watch probe\n"
	@printf "  BOOTSTRAP_TOKEN=<token>   Use an existing bearer token for bootstrap\n"
	@printf "  NAMESPACE=%s REPOSITORY=%s DEFAULT_BRANCH=%s\n" "$(NAMESPACE)" "$(REPOSITORY)" "$(DEFAULT_BRANCH)"

git: ## Run gitstore-git-service locally in the foreground.
	@mkdir -p "$(GIT_DATA_DIR)"
	@cd "$(GIT_SERVICE_DIR)" && GITSTORE_GIT__DATA_DIR="$(GIT_DATA_DIR)" cargo run --bin git-service

controller: ## Run gitstore-controller-manager locally in the foreground.
	@cd "$(CONTROLLER_MANAGER_DIR)" && go run ./cmd/controller

api: ## Run gitstore-api locally in the foreground.
	@cd "$(API_DIR)" && go run ./cmd/server

dev: ## Run local git service and API together in the foreground.
	@set -u; \
	mkdir -p "$(GIT_DATA_DIR)"; \
	tmp=$$(mktemp -d); \
	fifo="$$tmp/done"; \
	mkfifo "$$fifo"; \
	cleanup() { \
		trap - INT TERM EXIT; \
		[ -n "$${git_pid:-}" ] && kill "$$git_pid" 2>/dev/null || true; \
		[ -n "$${api_pid:-}" ] && kill "$$api_pid" 2>/dev/null || true; \
		[ -n "$${controller_pid:-}" ] && kill "$$controller_pid" 2>/dev/null || true; \
		[ -n "$${git_pid:-}" ] && wait "$$git_pid" 2>/dev/null || true; \
		[ -n "$${api_pid:-}" ] && wait "$$api_pid" 2>/dev/null || true; \
		[ -n "$${controller_pid:-}" ] && wait "$$controller_pid" 2>/dev/null || true; \
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
	( set +e; \
		cd "$(CONTROLLER_MANAGER_DIR)" || { printf 'controller 1\n' > "$$fifo"; exit 0; }; \
		go run ./cmd/controller & child=$$!; \
		trap 'kill "$$child" 2>/dev/null; wait "$$child" 2>/dev/null; exit 143' INT TERM; \
		wait "$$child"; status=$$?; \
		printf 'controller %s\n' "$$status" > "$$fifo"; \
	) & controller_pid=$$!; \
	read service status < "$$fifo"; \
	echo "$$service exited with status $$status"; \
	cleanup; \
	trap - EXIT; \
	exit "$$status"

validate-local-config:
	@test -f "$(CONFIG_FILE)" || { echo "CONFIG_FILE does not exist: $(CONFIG_FILE)"; exit 2; }
	@test -r "$(CONFIG_FILE)" || { echo "CONFIG_FILE is not readable: $(CONFIG_FILE)"; exit 2; }
	@test -f "$(POLICY_FILE)" || { echo "Local RBAC policy does not exist: $(POLICY_FILE)"; exit 2; }
	@test -r "$(POLICY_FILE)" || { echo "Local RBAC policy is not readable: $(POLICY_FILE)"; exit 2; }

compose-config-check: validate-local-config ## Validate local profile arguments and read-only mounts.
	@CONFIG_FILE="$(abspath $(CONFIG_FILE))" ./scripts/check-local-compose-config.sh

compose: validate-local-config ## Run all core services; pass DATASTORE=scylla and optional PROFILE=cluster for Scylla.
	@case "$(DATASTORE)" in memdb|scylla) ;; *) echo "DATASTORE must be 'memdb' or 'scylla'"; exit 2;; esac
	@case "$(PROFILE)" in single|cluster) ;; *) echo "PROFILE must be 'single' or 'cluster'"; exit 2;; esac
	@SCYLLA_CLUSTER_SMP="$(SCYLLA_CLUSTER_SMP)" SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS="$(SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS)" $(LOCAL_COMPOSE) $(DATASTORE_COMPOSE_FILE) up --build $(DETACH_FLAG)

scylla: ## Run Scylla services; pass PROFILE=cluster for the local three-node cluster.
	@case "$(PROFILE)" in single|cluster) ;; *) echo "PROFILE must be 'single' or 'cluster'"; exit 2;; esac
	@SCYLLA_CLUSTER_SMP="$(SCYLLA_CLUSTER_SMP)" SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS="$(SCYLLA_CLUSTER_MAX_NETWORKING_IO_CONTROL_BLOCKS)" COMPOSE_BAKE="$(COMPOSE_BAKE)" docker compose -f compose.yml -f $(SCYLLA_COMPOSE_FILE) up $(DETACH_FLAG) $(SCYLLA_SERVICES)

ps: ## Show compose service status.
	@$(LIFECYCLE_COMPOSE) ps

logs: ## Follow compose logs; optionally pass SERVICE=<name>.
	@$(LIFECYCLE_COMPOSE) logs -f $(COMPOSE_SERVICE)

stop: ## Stop compose services; optionally pass SERVICE=<name>.
	@$(LIFECYCLE_COMPOSE) stop $(COMPOSE_SERVICE)

down: ## Stop and remove compose services and networks.
	@$(LIFECYCLE_COMPOSE) down --remove-orphans

build: ## Build Rust and Go services.
	@cd "$(GIT_SERVICE_DIR)" && cargo build --verbose
	@for dir in $(GO_MODULE_DIRS); do \
		cd "$$dir" && go build -v ./...; \
	done

test: ## Run Rust and Go test suites.
	@cd "$(GIT_SERVICE_DIR)" && cargo test --verbose
	@cd "$(API_DIR)" && go test -count=1 -v -race -coverprofile=coverage.txt -covermode=atomic ./...
	@cd "$(CONTROLLER_MANAGER_DIR)" && go test -count=1 -v -race -coverprofile=coverage.txt -covermode=atomic ./...

capacity: ## Run a reusable k6 capacity profile and retain structured evidence.
	@CAPACITY_PROFILE="$(CAPACITY_PROFILE)" \
		CAPACITY_EVIDENCE_DIR="$(CAPACITY_EVIDENCE_DIR)" \
		CAPACITY_ENV_FILE="$(CAPACITY_ENV_FILE)" \
		CAPACITY_TOKEN_FILE="$(CAPACITY_TOKEN_FILE)" \
		CAPACITY_CONFIG_MANIFEST="$(CAPACITY_CONFIG_MANIFEST)" \
		CAPACITY_ENVIRONMENT_MANIFEST="$(CAPACITY_ENVIRONMENT_MANIFEST)" \
		CAPACITY_CHAOS_PROFILE="$(CAPACITY_CHAOS_PROFILE)" \
		CAPACITY_CHAOS_TARGET="$(CAPACITY_CHAOS_TARGET)" \
		CAPACITY_CHAOS_DELAY="$(CAPACITY_CHAOS_DELAY)" \
		CAPACITY_CHAOS_CONFIRM="$(CAPACITY_CHAOS_CONFIRM)" \
		K6_IMAGE="$(K6_IMAGE)" \
		./scripts/run-capacity.sh "$(CAPACITY_PROFILE)"

chaos: ## Inject an opt-in container fault and retain structured evidence.
	@CHAOS_PROFILE="$(CHAOS_PROFILE)" \
		CHAOS_TARGET="$(CHAOS_TARGET)" \
		CHAOS_CONFIRM="$(CHAOS_CONFIRM)" \
		CHAOS_EVIDENCE_DIR="$(CHAOS_EVIDENCE_DIR)" \
		PUMBA_IMAGE="$(PUMBA_IMAGE)" \
		./scripts/run-chaos.sh "$(CHAOS_PROFILE)"

test-scylla-hardening: ## Run focused datastore hardening tests without an external Scylla instance.
	@cd "$(API_DIR)" && go test -count=1 ./internal/datastore/... ./tests/contract/datastore/...

test-scylla-integration: ## Run tagged datastore hardening tests against Scylla.
	@cd "$(API_DIR)" && GITSTORE_TEST_SCYLLA_ADDR="$(SCYLLA_TEST_ADDR)" \
		go test -tags scylla -count=1 -timeout 10m ./internal/datastore/scylla/... ./tests/contract/datastore/...

test-scylla-capacity: ## Run the opt-in Scylla capacity and soak test.
	@cd "$(API_DIR)" && GITSTORE_TEST_SCYLLA_ADDR="$(SCYLLA_TEST_ADDR)" \
		GITSTORE_SCYLLA_CAPACITY_PRODUCTS="$(SCYLLA_CAPACITY_PRODUCTS)" \
		GITSTORE_SCYLLA_CAPACITY_CONCURRENCY="$(SCYLLA_CAPACITY_CONCURRENCY)" \
		GITSTORE_SCYLLA_CAPACITY_SOAK_DURATION="$(SCYLLA_CAPACITY_DURATION)" \
		GITSTORE_SCYLLA_CAPACITY_RUN=1 \
		go test -tags scylla -count=1 -timeout 0 -run TestScyllaCapacity ./internal/datastore/scylla/...

test-namespace-admission-capacity: ## Run the opt-in two-replica Namespace admission soak.
	@cd "$(API_DIR)" && \
		GITSTORE_NAMESPACE_CAPACITY_DURATION="$(NAMESPACE_CAPACITY_DURATION)" \
		GITSTORE_NAMESPACE_CAPACITY_RUN=1 \
		go test -count=1 -timeout 0 -run '^TestNamespaceValidationCapacity$$' ./internal/cataloggrpc

test-namespace-watch-capacity: ## Run the deployed two-replica 60-minute Namespace watch capacity gate.
	@cd "$(ROOT)/tests/integration" && \
		NAMESPACE_WATCH_API_A="$(NAMESPACE_WATCH_API_A)" \
		NAMESPACE_WATCH_API_B="$(NAMESPACE_WATCH_API_B)" \
		NAMESPACE_WATCH_API_REPLACEMENT="$(NAMESPACE_WATCH_API_REPLACEMENT)" \
		NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE="$(NAMESPACE_WATCH_REPLACEMENT_TRIGGER_FILE)" \
		NAMESPACE_WATCH_TOKEN="$(NAMESPACE_WATCH_TOKEN)" \
		NAMESPACE_WATCH_TOKEN_FILE="$(NAMESPACE_WATCH_TOKEN_FILE)" \
		NAMESPACE_WATCH_CAPACITY_DURATION="$(NAMESPACE_WATCH_CAPACITY_DURATION)" \
		NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS="$(NAMESPACE_WATCH_CAPACITY_SUBSCRIBERS)" \
		NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS="$(NAMESPACE_WATCH_CAPACITY_REPLAY_EVENTS)" \
		NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES="$(NAMESPACE_WATCH_CAPACITY_REPLAY_SAMPLES)" \
		NAMESPACE_WATCH_CAPACITY_REPLAY_CATCHUP_TIMEOUT="$(NAMESPACE_WATCH_CAPACITY_REPLAY_CATCHUP_TIMEOUT)" \
		NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL="$(NAMESPACE_WATCH_CAPACITY_BURST_INTERVAL)" \
		NAMESPACE_WATCH_CAPACITY_BURST_SIZE="$(NAMESPACE_WATCH_CAPACITY_BURST_SIZE)" \
		NAMESPACE_WATCH_CAPACITY_MUTATION_WORKERS="$(NAMESPACE_WATCH_CAPACITY_MUTATION_WORKERS)" \
		NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY="$(NAMESPACE_WATCH_CAPACITY_REPLACEMENT_DELAY)" \
		NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS="$(NAMESPACE_WATCH_CAPACITY_ALLOW_MISSING_METRICS)" \
		NAMESPACE_WATCH_CAPACITY_RUN=1 \
		go test -v -count=1 -timeout 0 -run '^TestNamespaceWatchDeploymentCapacity$$' .

test-namespace-watch-recovery: ## Probe bootstrap/resume, replacement, expiry, and overflow across two API replicas.
	@cd "$(ROOT)/tests/integration" && \
		NAMESPACE_WATCH_API_A="$(NAMESPACE_WATCH_API_A)" \
		NAMESPACE_WATCH_API_B="$(NAMESPACE_WATCH_API_B)" \
		NAMESPACE_WATCH_API_REPLACEMENT="$(NAMESPACE_WATCH_API_REPLACEMENT)" \
		NAMESPACE_WATCH_TOKEN="$(NAMESPACE_WATCH_TOKEN)" \
		NAMESPACE_WATCH_TOKEN_FILE="$(NAMESPACE_WATCH_TOKEN_FILE)" \
		NAMESPACE_WATCH_OVERFLOW_TRANSITIONS="$(NAMESPACE_WATCH_OVERFLOW_TRANSITIONS)" \
		go test -count=1 -run '^TestNamespaceWatch(CrossReplicaBootstrapAndResume|RecoveryProbe|DocumentedConsumer)$$' .

lint: ## Run Rust formatting/clippy and Go formatting/vet/staticcheck.
	@cd "$(GIT_SERVICE_DIR)" && cargo fmt --all -- --check
	@cd "$(GIT_SERVICE_DIR)" && cargo clippy --all-targets --all-features -- -D warnings
	@for dir in $(GO_MODULE_DIRS); do \
		if [ "$$(cd "$$dir" && gofmt -s -l . | wc -l | tr -d ' ')" != "0" ]; then \
			echo "The following files need formatting in $$dir:"; \
			cd "$$dir" && gofmt -s -l .; \
			exit 1; \
		fi; \
	done
	@for dir in $(GO_MODULE_DIRS); do \
		cd "$$dir" && go vet ./...; \
	done
	@cd "$(API_DIR)" && go install honnef.co/go/tools/cmd/staticcheck@latest
	@for dir in $(GO_MODULE_DIRS); do \
		cd "$$dir" && "$$(go env GOPATH)"/bin/staticcheck ./...; \
	done

license-check: ## Run Go, Rust, and JS/TS license header checks.
	@./scripts/check-go-license-headers.sh --all
	@./scripts/check-go-license-headers.sh --diff-base "$(DIFF_BASE)"
	@./scripts/check-rust-license-headers.sh --all
	@./scripts/check-rust-license-headers.sh --diff-base "$(DIFF_BASE)"
	@./scripts/check-js-license-headers.sh --all
	@./scripts/check-js-license-headers.sh --diff-base "$(DIFF_BASE)"

credential-output-check: ## Verify enrollment CLI never writes credential material to output.
	@./scripts/check-enroll-serviceaccount-output.sh

credential-leakage-check: ## Check service-account code does not log raw credentials.
	@./scripts/check-credential-log-leakage.sh

pr-ready: lint build test license-check credential-output-check credential-leakage-check ## Run the full PR readiness workflow.

bootstrap: bootstrap-namespace bootstrap-repository ## Create the default namespace and repository through the API.

bootstrap-tools:
	@command -v curl >/dev/null 2>&1 || { echo "curl is required for bootstrap targets"; exit 127; }
	@command -v jq >/dev/null 2>&1 || { echo "jq is required for bootstrap targets"; exit 127; }

hash-user-password: ## Generate a bcrypt hash for PASSWORD for manual users.yaml maintenance.
	@if [ -z "$${PASSWORD:-}" ]; then \
		echo "Usage: make hash-user-password PASSWORD='<password>'"; \
		exit 2; \
	fi
	@hash=$$(printf '%s\n' "$$PASSWORD" | (cd "$(API_DIR)" && go run ./cmd/gitctl hash-password)) || { \
		echo "Failed to generate bcrypt hash. Make sure the gitstore-api module builds correctly."; \
		exit 1; \
	}; \
	echo "bcrypt hash (put this in users.yaml password_hash): $$hash"

add-user: ## Add a local user to USERS_FILE; requires USERNAME and PASSWORD.
	@if [ -z "$${USERNAME:-}" ] || [ -z "$${PASSWORD:-}" ]; then \
		echo "Usage: make add-user USERNAME=<user> PASSWORD='<password>' [EMAIL=<email>] [DISPLAY_NAME='<name>'] [USERS_FILE=<path>]"; \
		exit 2; \
	fi
	@printf '%s\n' "$$PASSWORD" | (cd "$(API_DIR)" && go run ./cmd/gitctl users add \
		--file "$(abspath $(USERS_FILE))" \
		--username "$$USERNAME" \
		--email "$${EMAIL:-}" \
		--display-name "$${DISPLAY_NAME:-}" \
		--password-stdin)

add-role: ## Add a role to POLICY_FILE; requires ROLE and ALLOW and/or DENY.
	@if [ -z "$${ROLE:-}" ] || { [ -z "$${ALLOW:-}" ] && [ -z "$${DENY:-}" ]; }; then \
		echo "Usage: make add-role ROLE=<role> [ALLOW='action,action'] [DENY='action,action'] [POLICY_FILE=<path>]"; \
		exit 2; \
	fi
	@cd "$(API_DIR)" && go run ./cmd/gitctl rbac role add \
		--file "$(abspath $(POLICY_FILE))" \
		--name "$$ROLE" \
		$${ALLOW:+--allow "$$ALLOW"} \
		$${DENY:+--deny "$$DENY"}

assign-role: ## Assign an existing role to a subject in POLICY_FILE.
	@if [ -z "$${SUBJECT:-}" ] || [ -z "$${ROLE:-}" ]; then \
		echo "Usage: make assign-role SUBJECT=<subject> ROLE=<role> [POLICY_FILE=<path>]"; \
		exit 2; \
	fi
	@cd "$(API_DIR)" && go run ./cmd/gitctl rbac binding add \
		--file "$(abspath $(POLICY_FILE))" \
		--subject "$$SUBJECT" \
		--role "$$ROLE"

gen-jwt-secret: ## Generate a JWT secret and write GITSTORE_AUTH__JWT__SECRET to gitstore-api/.env.
	@secret=$$(cd "$(API_DIR)" && go run ./cmd/gitctl gen-jwt-secret | sed -n 's/^GITSTORE_AUTH__JWT__SECRET=//p') || { \
		echo "Failed to generate JWT secret. Make sure the gitstore-api module builds correctly."; \
		exit 1; \
	}; \
	./scripts/update-env-secret.sh GITSTORE_AUTH__JWT__SECRET "$$secret" "$(API_DIR)/.env"

gen-hmac-secret: ## Generate an HMAC secret and write GITSTORE_AUTH__GRPC__HMAC_SECRET to both service .env files.
	@secret=$$(cd "$(API_DIR)" && go run ./cmd/gitctl gen-hmac-secret | sed -n 's/^GITSTORE_AUTH__GRPC__HMAC_SECRET=//p') || { \
		echo "Failed to generate HMAC secret. Make sure the gitstore-api module builds correctly."; \
		exit 1; \
	}; \
	./scripts/update-env-secret.sh GITSTORE_AUTH__GRPC__HMAC_SECRET "$$secret" "$(API_DIR)/.env" "$(GIT_SERVICE_DIR)/.env"; \
	echo "HMAC secret updated in $(API_DIR)/.env and $(GIT_SERVICE_DIR)/.env"

bootstrap-token: bootstrap-tools ## Login and print/cache a bootstrap bearer token.
	@if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
		echo "ADMIN_PASSWORD is required for bootstrap-token"; \
		exit 2; \
	fi
	@mkdir -p "$$(dirname "$${BOOTSTRAP_TOKEN_CACHE}")"
	@query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { token { accessToken } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		echo "Hint: verify ADMIN_USERNAME and ADMIN_PASSWORD match users.yaml (run 'make hash-user-password PASSWORD=<password>' to generate a hash)."; \
		exit 1; \
	fi; \
	token=$$(echo "$$response" | jq -er '.data.login.token.accessToken // empty') || { \
		echo "Login response did not contain a token. Check ADMIN_USERNAME, ADMIN_PASSWORD, and API_URL."; \
		echo "Hint: run 'make hash-user-password PASSWORD=<password>' to generate a users.yaml password hash."; \
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
		query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { token { accessToken } } }'; \
		payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
		response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
			echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
			exit 1; \
		}; \
		if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
			echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
			echo "Hint: verify ADMIN_USERNAME and ADMIN_PASSWORD match users.yaml (run 'make hash-user-password PASSWORD=<password>' to generate a hash)."; \
			exit 1; \
		fi; \
		token=$$(echo "$$response" | jq -er '.data.login.token.accessToken // empty') || { \
			echo "Login response did not contain a token."; \
			echo "Hint: run 'make hash-user-password PASSWORD=<password>' to generate a users.yaml password hash."; \
			exit 1; \
		}; \
	fi; \
	query='mutation CreateNamespace($$input: CreateNamespaceInput!) { createNamespace(input: $$input) { namespace { id metadata { name } spec { tier } } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg name "$${NAMESPACE}" --arg title "$${NAMESPACE_DISPLAY_NAME}" --arg tier "$${NAMESPACE_TIER}" '{query: $$query, variables: {input: {apiVersion: "gitstore.dev/v1beta1", kind: "Namespace", metadata: {name: $$name}, spec: {title: $$title, tier: $$tier}}}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' -H "Authorization: Bearer $$token" --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	echo "$$response" | jq -r '.data.createNamespace.namespace | "Created namespace \(.metadata.name) (\(.id))"'

bootstrap-repository: bootstrap-tools ## Create only the bootstrap repository; namespace must already exist.
	@set -u; \
	token="$${BOOTSTRAP_TOKEN:-}"; \
	if [ -z "$$token" ] && [ -f "$${BOOTSTRAP_TOKEN_CACHE}" ]; then token=$$(cat "$${BOOTSTRAP_TOKEN_CACHE}"); fi; \
	if [ -z "$$token" ]; then \
		if [ -z "$${ADMIN_PASSWORD:-}" ]; then \
			echo "ADMIN_PASSWORD is required unless BOOTSTRAP_TOKEN is provided or $${BOOTSTRAP_TOKEN_CACHE} exists"; \
			exit 2; \
		fi; \
		query='mutation Login($$username: String!, $$password: String!) { login(input: { username: $$username, password: $$password }) { token { accessToken } } }'; \
		payload=$$(jq -n --arg query "$$query" --arg username "$${ADMIN_USERNAME}" --arg password "$${ADMIN_PASSWORD}" '{query: $$query, variables: {username: $$username, password: $$password}}'); \
		response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' --data "$$payload" "$${API_URL}") || { \
			echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
			exit 1; \
		}; \
		if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
			echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
			echo "Hint: verify ADMIN_USERNAME and ADMIN_PASSWORD match users.yaml (run 'make hash-user-password PASSWORD=<password>' to generate a hash)."; \
			exit 1; \
		fi; \
		token=$$(echo "$$response" | jq -er '.data.login.token.accessToken // empty') || { \
			echo "Login response did not contain a token."; \
			echo "Hint: run 'make hash-user-password PASSWORD=<password>' to generate a users.yaml password hash."; \
			exit 1; \
		}; \
	fi; \
	query='query Namespace($$identifier: String!) { namespace(by: { identifier: $$identifier }) { id metadata { name } } }'; \
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
	query='mutation CreateRepository($$namespace: String!, $$name: String!, $$defaultBranch: String!) { createRepository(input: { namespace: $$namespace, name: $$name, defaultBranch: $$defaultBranch }) { repository { id name defaultBranch storagePath namespace { metadata { name } } } } }'; \
	payload=$$(jq -n --arg query "$$query" --arg namespace "$${NAMESPACE}" --arg name "$${REPOSITORY}" --arg defaultBranch "$${DEFAULT_BRANCH}" '{query: $$query, variables: {namespace: $$namespace, name: $$name, defaultBranch: $$defaultBranch}}'); \
	response=$$(curl --silent --show-error --connect-timeout 5 -H 'Content-Type: application/json' -H "Authorization: Bearer $$token" --data "$$payload" "$${API_URL}") || { \
		echo "Failed to reach GitStore API at $${API_URL}. Start it with make compose or make dev."; \
		exit 1; \
	}; \
	if echo "$$response" | jq -e '(.errors // []) | length > 0' >/dev/null; then \
		echo "$$response" | jq -r '.errors[]?.message' | sed 's/^/GraphQL error: /'; \
		exit 1; \
	fi; \
	echo "$$response" | jq -r '.data.createRepository.repository | "Created repository \(.namespace.metadata.name)/\(.name) (\(.id))\nClone URL: http://localhost:5000/\(.namespace.metadata.name)/\(.name).git"'

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

admin-compose: validate-local-config ## Run the optional admin compose stack.
	@$(LOCAL_COMPOSE) -f compose.admin.yml up --build $(DETACH_FLAG) admin

admin-down: ## Stop and remove the admin compose stack.
	@$(LOCAL_COMPOSE) -f compose.admin.yml down

admin-stop: ## Stop only the admin compose service.
	@$(LOCAL_COMPOSE) -f compose.admin.yml stop admin

admin-logs: ## Follow admin compose logs.
	@$(LOCAL_COMPOSE) -f compose.admin.yml logs -f admin
