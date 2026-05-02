# GitStore Admin — Architecture

`gitstore-admin` is the backoffice layer of the core GitStore stack.

## Architecture Diagram

```mermaid
%%{init: {'theme': 'base', 'themeVariables': { 'primaryColor': '#E5E7EB', 'edgeLabelBackground':'#ffffff', 'tertiaryColor': '#fff'}}}%%
graph TD
    Merchandiser[("🧑‍💼 Merchandiser\n(Browser)")]:::external
    GitClient[("👩‍💻 Git Client\n(CLI / AI Agent)")]:::external
    Storefront[("🌐 Storefront")]:::external

    Admin[("🖥️ gitstore-admin\n(Astro/React)")]:::addon
    API[("🐹 gitstore-api\n(Go GraphQL)")]:::go
    Git[("🦀 gitstore-git-service\n(Rust Git Engine)")]:::rust
    Disk[("💾 Storage\n(Bare .git Repos)")]:::infra

    Merchandiser -->|"Browser UI"| Admin
    Admin -->|"GraphQL mutations\n(products, categories, tags)"| API
    GitClient -->|"git push / pull"| Git
    API -->|"gRPC / git protocol"| Git
    Git <-->|"Fast I/O"| Disk
    Storefront -->|"GraphQL queries"| API

    classDef rust fill:#FCA5A5,stroke:#DC2626,stroke-width:2px,color:#000;
    classDef go fill:#BAE6FD,stroke:#0284C7,stroke-width:2px,color:#000;
    classDef addon fill:#D1FAE5,stroke:#059669,stroke-width:2px,color:#000;
    classDef infra fill:#E5E7EB,stroke:#4B5563,stroke-width:2px,color:#000;
    classDef external fill:#fff,stroke:#111,stroke-width:1px,stroke-dasharray: 5 5,color:#000;
```

## How the Admin Fits In

The core stack (`gitstore-api` + `gitstore-git-service`) operates independently. `gitstore-admin`:

1. Connects only to `gitstore-api` via GraphQL — it never talks directly to `gitstore-git-service`.
2. Uses the same GraphQL mutations available to any other client (AI agents, storefronts, CI scripts).
3. Calls the `publishCatalog` mutation which triggers `gitstore-api` to push a tagged commit to `gitstore-git-service`.

## Relationship to Core Proposals

Both architecture proposals in [`docs/architecture.md`](../architecture.md) support the admin add-on at the same attachment point: the GraphQL API layer. The admin add-on is proposal-agnostic.

## Network Topology (Compose)

When started with the `compose.admin.yml` override, the three containers share `gitstore-network`:

```
gitstore-network
├── gitstore-git-service  (ports 9418, 8080)
├── gitstore-api          (port 4000)
└── gitstore-admin        (port 3000)  ← added by compose.admin.yml
```

The admin container communicates with the API container using the internal DNS name `api` (resolved by Docker within `gitstore-network`).
