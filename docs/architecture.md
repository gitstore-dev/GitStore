# Architecture

This document presents two end-to-end architecture options for the AI-native commerce engine. Both options treat Git as the authoritative system of record, then project customer-facing state into a fast read layer.

## System Goals

- Keep catalog history auditable and reversible.
- Allow AI agents to perform safe, structured mutations.
- Serve storefront reads from low-latency indexed data.
- Decouple write acceptance from heavy validation and indexing work.

## Shared Building Blocks

Both proposals use the same core building blocks, arranged with different control points:

- **Actors**: AI agents, human engineers, and the storefront.
- **Control plane**: Go API gateway and/or Rust Git server.
- **Storage plane**: Bare Git repositories on disk as source of truth.
- **Distribution plane**: Event queue plus KV store for published read state.

## Implementation Baseline (Current Repository)

- `gitstore-api/`: Go API gateway, GraphQL surface, and gRPC client/server boundaries.
- `gitstore-git-service/`: Rust Git engine, receive hooks, and repository access logic.
- `shared/schemas/`: GraphQL schema contracts consumed by the API layer.

> **Admin add-on**: For the optional web interface, see [`docs/admin/architecture.md`](admin/architecture.md).

These folders map directly to the control, storage, and distribution planes described below.

---

## Proposal 1 - API-Led Mutations with Asynchronous Indexing

Proposal 1 starts at the API boundary, commits to Git immediately, and then indexes validated state asynchronously.

### Top-Down Flow

1. **Entry**: AI agents call GraphQL mutations through the API gateway.
2. **Commit**: API gateway sends gRPC mutation requests to the Rust Git server.
3. **Persist**: Git server writes commits to disk repositories.
4. **Validate and index**: Post-receive events flow through queue and parser worker.
5. **Serve**: Storefront GraphQL queries read from the KV store.

### Implementation Focus

- **Request contract**: GraphQL mutations should include idempotency keys so retries do not create duplicate commits.
- **Commit metadata**: Persist actor identity, request ID, and schema version in commit message/footer for traceability.
- **Queue payload**: Emit repository path, commit SHA, changed blob paths, and correlation ID on each post-receive event.
- **Validation contract**: Parser worker validates changed blobs against the catalog content schema (product/category/collection frontmatter) and returns structured errors.
- **KV write model**: Use deterministic keys (`catalog:{env}:{entity}:{id}`) and version stamps (`etag` or commit SHA).

### Architecture Diagram

```mermaid
%%{init: {'theme': 'base', 'themeVariables': { 'primaryColor': '#E5E7EB', 'edgeLabelBackground':'#ffffff', 'tertiaryColor': '#fff'}}}%%
graph LR
    %% Definitions of External Actors %%
    Agent[("🤖 AI Agents")]:::external
    Storefront[("🌐 Headless Storefront\n(Next.js / ISR)")]:::external

    %% Infrastructure & Data Storage Nodes %%
    Disk[("💾 Disk Storage\n(Bare .git Repos)")]:::infra
    KV[("⚡ Edge-KV Store\n(Redis / Valkey)\n[Fast State]")]:::infra
    Queue[("📨 Event Queue\n(Redis Streams / NATS)")]:::infra

    %% Core Services Subgraph (Your System) %%
    subgraph "AI-Native Commerce Engine Infrastructure"
        direction TB
        API[("🐹 Go API Gateway\n(GraphQL / gRPC)")]:::go
        Git[("🦀 Rust Git Server\n(gRPC / gitoxide)\n[Slow Source of Truth]")]:::rust
        Parser[("⚙️ Standalone Parser Worker\n(Go/Rust / gRPC)")]:::worker
    end

    %% --- Connections: The Write Path --- %%
    Agent == "1. GraphQL Mutation (Change Price)" ==> API
    API == "2. gRPC: CommitChange" ==> Git
    Git <== "3. Native Fast I/O" ==> Disk

    %% --- Connections: The Asynchronous Sync Path --- %%
    Git -. "4. post-receive Webhook" .-> Queue
    Queue -. "5. Consume Event" .-> Parser
    Parser <== "6. gRPC: GetChangedBlobs" ==> Git

    %% Internal worker logic symbolized %%
    Parser -- "7. Schema Validation (Pre-commit Guardrail)" --> Parser
    Parser == "8. Upsert Validated JSON" ==> KV

    %% --- Connections: The Read Path (Customer Facing) --- %%
    Storefront -. "A. ISR Revalidate Webhook" .-> Storefront
    Storefront -- "B. GraphQL Query" --> API
    API -- "C. Fetch Indexed Data" --> KV

    %% --- Styling & Legends --- %%
    %% Path styles %%
    %% Critical Write/Read paths
    linkStyle 0,1,2,5,7,9,10 stroke-width:3px,stroke:black;
    %% Async paths
    linkStyle 3,4,6,8 stroke-width:2px,stroke-dasharray: 5 5,stroke:dimgray;

    %% Component Styles %%
    classDef rust fill:#FCA5A5,stroke:#DC2626,stroke-width:2px,color:#000;
    classDef go fill:#BAE6FD,stroke:#0284C7,stroke-width:2px,color:#000;
    classDef worker fill:#C7D2FE,stroke:#4F46E5,stroke-width:2px,color:#000;
    classDef infra fill:#E5E7EB,stroke:#4B5563,stroke-width:2px,color:#000;
    classDef external fill:#fff,stroke:#111,stroke-width:1px,stroke-dasharray: 5 5,color:#000;
```

### Responsibilities by Layer

- **Actors**
  - AI agents submit mutations and receive fast acknowledgments.
  - Storefront 
    - reads indexed catalog state from KV for low-latency queries.
    - triggers ISR revalidation on webhook event from API.
- **Core services**
  - Go API gateway handles GraphQL writes and reads.
  - Rust Git server executes durable commit operations.
  - Parser worker validates changed blobs and writes read-optimized JSON.
- **Infrastructure**
  - Disk stores canonical Git history.
  - Queue carries asynchronous indexing work.
  - KV serves low-latency storefront reads.

### Operational Notes

- Write acknowledgments are fast because indexing is asynchronous.
- Validation failures are surfaced operationally without rewriting accepted Git commits.
- Multiple parser workers can scale out independently as event volume grows.

### Implementation Sequence

1. Wire GraphQL mutation handlers in `gitstore-api/` to a single gRPC `CommitChange` boundary.
2. Implement post-receive event publication in `gitstore-git-service/` with correlation IDs.
3. Add parser worker consumers that validate and upsert KV documents.
4. Add observability: mutation latency, queue lag, validation failure rate, and KV upsert latency.
5. Gate rollout with shadow indexing before switching storefront reads fully to KV.

---

## Proposal 2 - Git-Native Ingress with Tag-Gated Publishing

Proposal 2 starts at Git transport boundaries, validates during receive, and only publishes customer-visible state on explicit release tags.

### Top-Down Flow

1. **Entry**: Engineers and AI agents push via SSH or Smart HTTP/API.
2. **Control**: Rust Git server runs pre-receive validation checks.
3. **Persist draft**: Accepted changes are written to disk as draft state.
4. **Publish release**: Tag events trigger queue-to-parser publish workflow.
5. **Serve**: Storefront reads published state from KV and revalidates pages.

### Implementation Focus

- **Ingress policy**: Enforce branch/tag naming rules and signer checks in pre-receive hooks.
- **Quarantine validation**: Parse staged objects before accepting refs; reject pushes that violate schema.
  ```bash
  $ git push origin main
  Enumerating objects: 5, done.
  Counting objects: 100% (5/5), done.
  Writing objects: 100% (3/3), 342 bytes | 342.00 KiB/s, done.
  Total 3 (delta 2), reused 0 (delta 0)
  remote: -------------------------------------------------
  remote: ❌ COMMERCE ENGINE VALIDATION FAILED
  remote: -------------------------------------------------
  remote: File: catalog/shoes/sneaker-v2.md
  remote: Error: 'price' field must be a Float. Received String ("forty").
  remote:
  remote: Please fix the YAML front-matter and push again.
  remote: -------------------------------------------------
  To ssh://git.yourstore.com/brand/catalog.git
  ! [remote rejected] main -> main (pre-receive hook declined)
  error: failed to push some refs to 'ssh://git.yourstore.com/brand/catalog.git'
  ```
- **Release contract**: Only tags matching a release pattern (for example `release/*` or `v*`) emit publish events.
- **Publication payload**: Include tag name, target commit SHA, repository, and release timestamp.
- **KV projection**: Parser materializes only tagged state, keeping draft commits invisible to storefront queries.

### Architecture Diagram

```mermaid
%%{init: {'theme': 'base', 'themeVariables': { 'primaryColor': '#E5E7EB', 'edgeLabelBackground':'#ffffff', 'tertiaryColor': '#fff'}}}%%
graph TD
    %% Definitions of External Actors %%
    Eng[("👩‍💻 Human Engineers\n(Standard Git CLI)")]:::external
    Agent[("🤖 AI Agents\n(API or Git CLI)")]:::external
    Storefront[("🌐 Headless Storefront\n(Next.js / ISR)")]:::external

    %% Ingress Protocols %%
    SSH[("🔑 SSH Protocol\n(Port 22 / russh)")]:::rust
    HTTPS[("🔒 Smart HTTP / API\n(Go API Gateway)")]:::go

    %% Infrastructure & Data Storage Nodes %%
    Disk[("💾 Disk Storage\n(Bare .git Repos)")]:::infra
    KV[("⚡ Edge-KV Store\n(Production State)")]:::infra
    Queue[("📨 Event Queue\n(Releases/Tags Only)")]:::infra

    %% Core Services Subgraph %%
    subgraph "Git-Native Commerce Engine"
        Git[("🦀 Rust Git Server\n(Core Engine)")]:::rust
        Parser[("⚙️ Parser Worker\n(Validator & Indexer)")]:::worker
    end

    %% --- Connections: The Synchronous Write & Validate Path --- %%
    Eng -->|git push| SSH
    Agent -->|git push / API| HTTPS
    SSH ==> Git
    HTTPS ==> Git

    Git -. "1. pre-receive hook (Quarantine Area)" .-> Parser
    Parser -. "2. Schema Check (Return OK or Reject)" .-> Git
    Git == "3. If OK: Save to Disk (Draft State)" ==> Disk

    %% --- Connections: The Explicit Publish Path (Tags) --- %%
    Git == "4. post-receive hook (Fires on tags only)" ==> Queue
    Queue == "5. Consume release event" ==> Parser
    Parser == "6. Extract and upsert release state" ==> KV

    %% --- Connections: The Read Path (Customer Facing) --- %%
    Storefront -.->|A. Revalidate URL| Storefront
    Storefront -->|B. GraphQL Query| HTTPS
    HTTPS -->|C. Fetch Published Data| KV

    %% --- Styling & Legends --- %%
    classDef rust fill:#FCA5A5,stroke:#DC2626,stroke-width:2px,color:#000;
    classDef go fill:#BAE6FD,stroke:#0284C7,stroke-width:2px,color:#000;
    classDef worker fill:#C7D2FE,stroke:#4F46E5,stroke-width:2px,color:#000;
    classDef infra fill:#E5E7EB,stroke:#4B5563,stroke-width:2px,color:#000;
    classDef external fill:#fff,stroke:#111,stroke-width:1px,stroke-dasharray: 5 5,color:#000;
```

### Responsibilities by Layer

- **Actors**
  - Engineers and AI agents can both submit changes through Git-native channels.
  - Storefront reads only published release state.
- **Core services**
  - Rust Git server enforces receive-time policy and repository integrity.
  - Parser worker validates and projects tagged releases into KV.
  - Go API endpoint remains available for GraphQL reads and controlled write APIs.
- **Infrastructure**
  - Disk stores draft and released Git history.
  - Queue carries release publication events.
  - KV contains only customer-visible published catalog data.

### Operational Notes

- Release tags become the explicit publish contract.
- Draft branch activity is isolated from customer-facing reads.
- Rollbacks can be executed by moving release tags and replaying publish events.

### Implementation Sequence

1. Implement SSH/HTTP receive entrypoints and pre-receive checks in `gitstore-git-service/`.
2. Persist accepted draft refs to disk and log audit metadata for every ref update.
3. Trigger publish events only from tag updates and process them in parser workers.
4. Update `gitstore-api/` read resolvers to fetch only published keys from KV.
5. Add release runbooks for promote, rollback, and replay operations.

---

## Choosing Between Proposals

- Choose **Proposal 1** if mutation throughput and agent ergonomics at the API layer are the primary priority.
- Choose **Proposal 2** if strict release control and Git-native operational workflows are the primary priority.
- In both cases, Git remains authoritative and KV remains the read-optimized projection layer.

