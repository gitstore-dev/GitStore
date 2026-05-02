# Feature Specification: OSS Alignment — Service Naming, Docs, CI, and Compose Separation

**Feature Branch**: `003-oss-alignment`  
**Created**: 2026-05-01  
**Status**: Draft  
**Input**: User description: "Better OSS alignment. The services folders should be named distinctly from other backing folders. api → gitstore-api, admin-ui → gitstore-admin, git-server → gitstore-git-service. The core stack is gitstore-api and gitstore-git-service. Gitstore-admin is an add-on service, the core documentation should only focus on the core stack. Gitstore-admin documentation can be moved to dedicated documentation files. CI for gitstore-admin should only run when files in gitstore-admin changes. Integration tests in CI should only focus on gitstore-api and gitstore-git-service and should be implemented (currently TODO). Remove gitstore-admin from core architecture diagrams, add only to architecture diagrams in dedicated documentation pages. Current compose file should only focus on core stack. An override compose file e.g. compose.admin.yml should be used instead and dedicated documentation should reference it."

## User Scenarios & Testing *(mandatory)*

### User Story 1 — Repository Structure Is Immediately Legible (Priority: P1)

A new open-source contributor clones the repository and, within seconds of listing the top-level directories, can distinguish service source folders from all other folders. The prefixed names (`gitstore-api`, `gitstore-git-service`, `gitstore-admin`) make the purpose and scope of each folder unambiguous, and the split between "core" and "add-on" services is apparent without reading any documentation.

**Why this priority**: This is the foundational change — every other story depends on the folder structure being correct. It also directly addresses the OSS first-impressions problem where ambiguous folder names slow down contribution and adoption.

**Independent Test**: Can be fully tested by cloning the repository from a clean state and listing directories; the output alone must communicate which folders are services and which are core vs add-on.

**Acceptance Scenarios**:

1. **Given** a fresh clone of the repository, **When** a contributor lists the root directory, **Then** they see `gitstore-api`, `gitstore-git-service`, and `gitstore-admin` as distinct, prefixed service folders alongside non-service folders with different naming conventions.
2. **Given** the renamed service folders exist, **When** a contributor reads only the folder names, **Then** they can correctly identify `gitstore-api` and `gitstore-git-service` as the core stack and `gitstore-admin` as an optional add-on.
3. **Given** the renaming is complete, **When** any internal cross-references (scripts, configs, CI, compose files) are inspected, **Then** all references use the new folder names with no references to the old names (`api`, `git-server`, `admin-ui`).

---

### User Story 2 — Core Stack Runs Without the Admin Add-On (Priority: P1)

An operator who wants to run only the essential gitstore services (the API and git service) uses the default compose file and gets exactly those two services — no admin UI, no admin-related dependencies. This makes the minimal deployment footprint clear and keeps the operational surface area small for users who do not need the admin feature.

**Why this priority**: Separating the compose files is the runtime counterpart to folder renaming. It ensures gitstore-admin is truly optional, not accidentally bundled into every deployment.

**Independent Test**: Can be fully tested by running the default compose command and verifying that only `gitstore-api` and `gitstore-git-service` containers start.

**Acceptance Scenarios**:

1. **Given** the default `compose.yml` file, **When** an operator starts the stack with a single compose command, **Then** only `gitstore-api` and `gitstore-git-service` containers are started, and no admin-related containers are present.
2. **Given** the `compose.admin.yml` override file, **When** an operator starts the stack using both `compose.yml` and `compose.admin.yml`, **Then** the admin service starts alongside the core services without errors.
3. **Given** the `compose.admin.yml` override, **When** it is inspected, **Then** it contains only the `gitstore-admin` service definition and any admin-specific dependencies that are not part of the core stack.

---

### User Story 3 — Integration Tests Validate the Core Stack in CI (Priority: P1)

A contributor opens a pull request touching `gitstore-api` or `gitstore-git-service`. The CI pipeline runs integration tests that exercise real interactions between those two services. The tests are not marked TODO; they exist, pass in CI, and provide meaningful signal about regressions before merge.

**Why this priority**: The integration tests were previously marked TODO, meaning the core services had no end-to-end validation in CI. This is a critical gap for any OSS project that wants to accept external contributions confidently.

**Independent Test**: Can be fully tested by triggering CI on a branch that only modifies `gitstore-api` or `gitstore-git-service`; integration tests must run and produce a pass/fail result.

**Acceptance Scenarios**:

1. **Given** a commit that modifies files in `gitstore-api` or `gitstore-git-service`, **When** CI runs, **Then** integration tests that exercise communication between `gitstore-api` and `gitstore-git-service` are executed and a pass/fail result is reported.
2. **Given** the integration tests exist, **When** they run against a correctly configured core stack, **Then** all tests pass in a clean CI environment without requiring `gitstore-admin`.
3. **Given** a test failure in integration tests, **When** CI reports the result, **Then** the failure output clearly identifies which interaction between the two services failed.

---

### User Story 4 — Admin CI Is Path-Filtered; Core CI Always Runs (Priority: P2)

A contributor submits a pull request that only touches `gitstore-api`. The `gitstore-admin` CI jobs do not run. Conversely, a PR that only modifies files inside `gitstore-admin` triggers the admin CI jobs, and the core CI jobs still run — because core CI is required for branch protection and must pass on every PR regardless of which files changed.

**Why this priority**: Core CI jobs gatekeeping merge ensure the main branch is always in a releasable state. Path-filtering is scoped exclusively to the add-on (`gitstore-admin`) so contributors get fast, focused feedback without weakening branch protection guarantees.

**Independent Test**: Can be fully tested by opening a PR that modifies only `gitstore-admin` files and confirming that both the core CI jobs and the admin CI jobs appear in the run summary — and that only the admin CI jobs are absent on a core-only PR.

**Acceptance Scenarios**:

1. **Given** a PR that modifies only files in `gitstore-api` or `gitstore-git-service`, **When** CI runs, **Then** core CI jobs execute and no `gitstore-admin` CI jobs are triggered.
2. **Given** a PR that modifies only files in `gitstore-admin`, **When** CI runs, **Then** both core CI jobs and `gitstore-admin` CI jobs run; core CI must pass before merge is allowed.
3. **Given** a PR that modifies files in both core services and `gitstore-admin`, **When** CI runs, **Then** all CI jobs run; merge is blocked until all pass.
4. **Given** branch protection rules are configured, **When** any PR is opened against the main branch, **Then** core CI jobs for `gitstore-api` and `gitstore-git-service` are listed as required status checks.

---

### User Story 5 — Documentation Reflects the Core/Add-On Separation (Priority: P2)

A developer reading the core documentation (quickstart, architecture overview, user guide) encounters only `gitstore-api` and `gitstore-git-service`. If they want to learn about the admin interface, they follow a link to a dedicated admin documentation page that covers setup, architecture (including the admin in diagrams), and the `compose.admin.yml` usage. Core architecture diagrams show only the two core services.

**Why this priority**: Documentation accuracy is a force multiplier for OSS adoption. Core docs cluttered with add-on details confuse users who only want the minimal stack.

**Independent Test**: Can be fully tested by reading all core documentation files and confirming no mention of `gitstore-admin`; separately, admin documentation must exist and be self-contained.

**Acceptance Scenarios**:

1. **Given** all core documentation files, **When** they are searched for references to `gitstore-admin` or the admin UI, **Then** no such references exist (except as a "see also" link pointing to dedicated admin docs).
2. **Given** the core architecture diagrams, **When** they are reviewed, **Then** they depict only `gitstore-api` and `gitstore-git-service` with their interactions and backing infrastructure.
3. **Given** the dedicated admin documentation pages, **When** they are reviewed, **Then** they include an architecture diagram showing `gitstore-admin` in context with the core services, and all setup instructions reference `compose.admin.yml`.
4. **Given** an operator following the dedicated admin docs, **When** they run the compose command shown in those docs, **Then** the full stack including `gitstore-admin` starts successfully.

---

### Edge Cases

- What happens if an existing script or automation tool hard-codes the old folder names (`api`, `git-server`, `admin-ui`)? All such references must be updated; a CI step or grep check should catch any remaining old names post-rename.
- What if `compose.admin.yml` defines a service name that conflicts with a service in `compose.yml`? The override file must be reviewed to ensure service names are unique or intentionally extend core services.
- What if a contributor runs only `compose.admin.yml` without the base `compose.yml`? Documentation should clearly state that `compose.admin.yml` is an override and requires the base file.
- What if a PR touches files both inside and outside service folders (e.g., root-level CI config)? CI path filters should be designed so that changes to shared CI config do not inadvertently skip important jobs.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: The repository MUST contain service directories named `gitstore-api`, `gitstore-git-service`, and `gitstore-admin`, replacing the current `api`, `git-server`, and `admin-ui` directories respectively.
- **FR-002**: All internal references to the old folder names (`api`, `git-server`, `admin-ui`) MUST be updated to use the new names across all configuration files, scripts, CI definitions, and compose files.
- **FR-003**: The default `compose.yml` MUST define only the core stack (`gitstore-api` and `gitstore-git-service`) and their required backing infrastructure (databases, message queues, etc.).
- **FR-004**: A `compose.admin.yml` override file MUST exist and define the `gitstore-admin` service and any admin-specific dependencies not present in `compose.yml`.
- **FR-005**: Core documentation files MUST NOT contain references to `gitstore-admin` other than a "see also" pointer to dedicated admin documentation.
- **FR-006**: Core architecture diagrams MUST depict only `gitstore-api` and `gitstore-git-service` and their interactions.
- **FR-007**: Dedicated documentation pages for `gitstore-admin` MUST exist and include: service overview, architecture diagram showing `gitstore-admin` with the core services, setup instructions referencing `compose.admin.yml`.
- **FR-008**: CI pipeline MUST implement path filtering so that `gitstore-admin` build and test jobs run only when files under `gitstore-admin/` change.
- **FR-009**: Integration tests for the core stack MUST be implemented in CI (replacing current TODO stubs) and MUST exercise real interactions between `gitstore-api` and `gitstore-git-service`.
- **FR-010**: Core CI integration tests MUST run without requiring `gitstore-admin` to be present or running.
- **FR-011**: Core CI jobs for `gitstore-api` and `gitstore-git-service` MUST run on every pull request regardless of which files changed, and MUST be configured as required status checks for branch protection.
- **FR-012**: CI for `gitstore-api` and `gitstore-git-service` MUST NOT be gated on or delayed by `gitstore-admin` CI jobs.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A contributor can correctly identify the core services and the add-on service by reading only the top-level directory listing — validated by a brief user review with at least one external contributor unfamiliar with the codebase.
- **SC-002**: Running the default compose command starts exactly two application services (`gitstore-api` and `gitstore-git-service`); zero admin-related containers are created.
- **SC-003**: Core CI jobs run on every PR; a CI run triggered by a commit that modifies only files in `gitstore-admin` shows core CI jobs in the run summary and no `gitstore-admin` CI jobs are present on a core-only PR.
- **SC-004**: Integration tests for the core stack exist, execute in CI, and achieve a passing state on the main branch — replacing 100% of the previously TODO integration test stubs.
- **SC-005**: A full-text search of all core documentation files returns zero matches for "gitstore-admin" (excluding any "see also" navigation links).
- **SC-006**: All core architecture diagrams contain exactly the core services and their backing infrastructure; `gitstore-admin` appears in zero core diagrams.
- **SC-007**: The dedicated `gitstore-admin` documentation is self-contained — a reader can set up the admin add-on by following only those pages, with no need to consult core documentation for admin-specific steps.

## Assumptions

- The renaming of folders is purely a structural change; the runtime behaviour and external-facing interfaces of each service remain unchanged.
- "Backing folders" (e.g., `docs`, `specs`, `.github`, `scripts`) retain their existing naming convention; only the three service directories are renamed.
- The integration tests to be implemented will use the project's existing testing infrastructure and tooling; no new test frameworks need to be evaluated as part of this feature.
- `compose.admin.yml` is an override/extension of `compose.yml`, following the standard Docker Compose override pattern, rather than a standalone file.
- Core documentation includes: README, quickstart/getting-started guide, user guide, API reference, and any architecture overview pages.
- Dedicated admin documentation will be co-located in the `docs/` directory under a clearly named subdirectory or file (e.g., `docs/admin/` or `docs/gitstore-admin.md`).
