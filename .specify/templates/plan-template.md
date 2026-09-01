# Implementation Plan: [FEATURE]

**Branch**: `[###-feature-name]` | **Date**: [DATE] | **Spec**: [link]
**Input**: Feature specification from `/specs/[###-feature-name]/spec.md`

**Note**: This template is filled in by the `/speckit.plan` command. See `.specify/templates/plan-template.md` for the execution workflow.

## Summary

[Extract from feature spec: primary requirement + technical approach from research]

## Technical Context

<!--
  ACTION REQUIRED: Replace the content in this section with the technical details
  for the project. The structure here is presented in advisory capacity to guide
  the iteration process.
-->

**Language/Version**: [e.g., Python 3.11, Swift 5.9, Rust 1.75 or NEEDS CLARIFICATION]  
**Primary Dependencies**: [e.g., FastAPI, UIKit, LLVM or NEEDS CLARIFICATION]  
**Storage**: [if applicable, e.g., PostgreSQL, CoreData, files or N/A]  
**Testing**: [e.g., pytest, XCTest, cargo test or NEEDS CLARIFICATION]  
**Target Platform**: [e.g., Linux server, iOS 15+, WASM or NEEDS CLARIFICATION]
**Project Type**: [e.g., library/cli/web-service/mobile-app/compiler/desktop-app or NEEDS CLARIFICATION]  
**Performance Goals**: [domain-specific, e.g., 1000 req/s, 10k lines/sec, 60 fps or NEEDS CLARIFICATION]  
**Constraints**: [domain-specific, e.g., <200ms p95, <100MB memory, offline-capable or NEEDS CLARIFICATION]  
**Scale/Scope**: [dataset, users, repositories, and sustained workload or NEEDS CLARIFICATION]
**Replica/Scaling Model**: [affected core services, state ownership, coordination, failover, and autoscaling behavior or N/A]
**Authentication/Authorization**: [user/service identities, policy enforcement points, and isolation impact or N/A]
**Load/Backpressure Model**: [peak and sustained workload, queue/concurrency bounds, timeouts, retries, and soak target or N/A]
**Capacity Profile**: [`tests/capacity/profiles/<profile>.js`, declared thresholds, correctness verifier, evidence location, or N/A with justification]
**Fault Profile**: [`tests/chaos/profiles/<profile>.json`, target boundary, steady-state/recovery assertions, or N/A with justification]

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

- **Test-First**: Contracts and failing tests are identified before implementation.
- **API/Contract-First**: GraphQL, gRPC, datastore, event, and error semantics are defined first.
- **Core-Service Boundary**: Impact on API, controller manager, and Git service is explicit.
- **Replica Safety**: Core-service changes remain correct with at least two replicas and rolling upgrades.
- **Multi-User Security**: AuthN/AuthZ enforcement and namespace/repository isolation are explicit.
- **Production Capacity**: Plans address 5,000,000-product scale and sustained Git push load where applicable.
- **Repeatable Evidence**: Load-bearing plans use `make capacity` for offered-load evidence and `make chaos` for declared failure/recovery scenarios; a tool exit code alone never substitutes for domain correctness verification.
- **Bounded Work**: Queries, queues, workers, retries, payloads, and partitions have explicit bounds.
- **Observability**: Logs, metrics, readiness, saturation, and recovery signals are designed.
- **Incremental Delivery**: Slices can deploy independently across mixed-version replicas.
- **Simplicity**: Added complexity is justified by measured or contractual production needs.

## Project Structure

### Documentation (this feature)

```text
specs/[###-feature]/
├── plan.md              # This file (/speckit.plan command output)
├── research.md          # Phase 0 output (/speckit.plan command)
├── data-model.md        # Phase 1 output (/speckit.plan command)
├── quickstart.md        # Phase 1 output (/speckit.plan command)
├── contracts/           # Phase 1 output (/speckit.plan command)
└── tasks.md             # Phase 2 output (/speckit.tasks command - NOT created by /speckit.plan)
```

### Source Code (repository root)
<!--
  ACTION REQUIRED: Replace the placeholder tree below with the concrete layout
  for this feature. Delete unused options and expand the chosen structure with
  real paths (e.g., apps/admin, packages/something). The delivered plan must
  not include Option labels.
-->

```text
# [REMOVE IF UNUSED] Option 1: Single project (DEFAULT)
src/
├── models/
├── services/
├── cli/
└── lib/

tests/
├── contract/
├── integration/
└── unit/

# [REMOVE IF UNUSED] Option 2: Web application (when "frontend" + "backend" detected)
backend/
├── src/
│   ├── models/
│   ├── services/
│   └── api/
└── tests/

frontend/
├── src/
│   ├── components/
│   ├── pages/
│   └── services/
└── tests/

# [REMOVE IF UNUSED] Option 3: Mobile + API (when "iOS/Android" detected)
api/
└── [same as backend above]

ios/ or android/
└── [platform-specific structure: feature modules, UI flows, platform tests]
```

**Structure Decision**: [Document the selected structure and reference the real
directories captured above]

## Complexity Tracking

> **Fill ONLY if Constitution Check has violations that must be justified**

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| [e.g., 4th project] | [current need] | [why 3 projects insufficient] |
| [e.g., Repository pattern] | [specific problem] | [why direct DB access insufficient] |
