# GitStore Admin Architecture

`gitstore-admin` is an optional UI layer in front of `gitstore-api`.

## Topology

```mermaid
graph TD
    Browser["Browser"]
    GitClient["Git client\n(CLI / agent)"]
    Storefront["Storefront"]

    Admin["gitstore-admin\nAstro / React\nport 3000"]
    API["gitstore-api\nGraphQL: 4000\nGit Smart HTTP: 5000\nCatalogService gRPC: 6000"]
    GitService["gitstore-git-service\ngRPC: 50051"]
    Controller["gitstore-controller-manager\nhealth/metrics/API: 5001"]
    Datastore["memdb / ScyllaDB"]
    Repos["Bare Git repositories"]

    Browser -->|"HTTP"| Admin
    Admin -->|"GraphQL"| API
    Storefront -->|"GraphQL"| API
    GitClient -->|"Git Smart HTTP"| API
    Controller -->|"GraphQL reconcile traffic"| API

    API -->|"GitService gRPC"| GitService
    GitService -->|"CatalogService gRPC\nvalidate/admit"| API
    API --> Datastore
    GitService --> Repos
```

## Boundaries

- Admin is a client of `gitstore-api`.
- Admin uses `GITSTORE_GRAPHQL_URL` to reach the GraphQL endpoint.
- Admin never talks directly to `gitstore-git-service`.
- Git clone, fetch, and push traffic enters through `gitstore-api` on Git Smart HTTP port `5000`.
- `gitstore-git-service` remains gRPC-only from the perspective of the compose network.
- Catalogue admission is owned by the API and Git service hook callout flow, not by the admin process.

## Compose Network

When started with `compose.admin.yml`, the services share `gitstore-network`:

```text
gitstore-network
├── gitstore-api                 ports 4000, 5000, 6000
├── gitstore-git-service         port 50051
├── gitstore-controller-manager  port 5001
└── gitstore-admin               port 3000
```

Inside the Docker network, admin reaches the API at `http://api:4000/graphql`.

## Current Write Model

Catalogue writes are Git-driven today. Users author catalogue resource files, commit them, and push to the API-fronted Git Smart HTTP endpoint. Push validation and admission are performed by the core services.

The admin UI remains the browser-facing attachment point for future Git-backed editing workflows. Direct catalogue GraphQL CRUD and publish mutations are not the documented integration path while that design is still being finalized.
