# Roadmap

This document outlines GitStore's product strategy, architectural decisions, and technology roadmap.

**Contents:**
- Strategic Decisions
- Product Roadmap (Phases 0-3)
- Technology Exploration
- Operational Requirements

---

## Strategic Decisions

### Platform Shape (Core vs Optional)

**Core GitStore Runtime** (required):
- `git-server` and `api`

**Local-First Principles:**
- Single bootstrap script runs GitStore locally with zero heavy infrastructure requirements
- In-memory storage/cache used for local development by default

**Production-Ready Upgrades** (optional):
- External infrastructure deployable incrementally
- Examples: ScyllaDB, Valkey, Qdrant, external identity services

#### Optional Modules

| Module                | Dependencies                        | Description                                         |
|-----------------------|-------------------------------------|-----------------------------------------------------|
| **Recommendation**    | Qdrant or Typesense                 | Vector database for product recommendations         |
| **OIDC**              | Ory Hydra, Dex, or Keycloak         | Authentication and federation                       |
| **User Management**   | Ory Kratos, ZITADEL, or SuperTokens | Identity and user lifecycle                         |
| **Caching**           | Redis or Valkey                     | Distributed cache (in-memory fallback)              |
| **Order Persistence** | ScyllaDB or Cassandra               | High-scale distributed storage (in-memory fallback) |

### Identity & Authentication Architecture

#### OIDC and OAuth2 Engines

- **Ory Hydra** — Standards-focused OIDC/OAuth2 server with intentional protocol/user-mgmt decoupling
- **Dex** — OIDC provider with connector-based federation (LDAP, SAML, OIDC, GitHub, etc.)
- **Keycloak** — Full integrated IAM suite (OIDC, SAML, user federation, admin UI)

#### Headless User Management

- **Ory Kratos** — APIs for registration, login, recovery, MFA, and profile lifecycle
- **ZITADEL** — IAM platform with user/org management, OIDC/OAuth2/SAML support (self-hostable)
- **SuperTokens** — Modular auth stack for sign-in, sessions, account management (self-hosting support)

#### Recommended Pairings for GitStore

| Pairing                | Use Case                                          |
|------------------------|---------------------------------------------------|
| **Hydra + Kratos**     | Strict service separation; protocol decoupling    |
| **Dex + existing IdP** | Enterprise federation; customer LDAP/SAML reuse   |
| **Keycloak alone**     | Operational simplicity; single-service deployment |

## Product Roadmap

### Phase 0: Core Git-Backed Catalogue

Git-backed product catalogue with flexible configuration and data interchange formats.

**Catalogue Frontmatter:**
- Adopt Kubernetes-style frontmatter (`apiVersion`, `kind`, `metadata`, `spec`)
- Create control loops per object type (Product, Category, Collection)
- Implement reconciliation pattern for desired vs. actual state

**Catalogue Features:**
- References in catalogue files for flexible definitions
- Expressions in product files (dynamic pricing, inventory, etc.)
- Operators and CRDs for extensibility without core runtime changes

---

### Phase 1: Adoption Friction & Local Development

Minimise barriers to local startup and experimentation.

- **Local Bootstrap Script** — Single command with zero infrastructure dependencies
- **In-Memory Defaults** — External service flags for production deployments
- **Inventory Management** — Track and manage stock levels

---

### Phase 2: Commerce Core

Essential e-commerce operations: shopping carts, transactions, order lifecycle, customer profiles.

- **Basket Management** — Add, remove, and update cart items
- **Checkout Process** — Multiple payment gateway integration
- **Order Tracking** — Real-time shipment status and delivery estimates
- **User Profiles** — Personal info, order history, preferences (OIDC/user-service integrated)

---

### Phase 3: Advanced & Extensibility

Platform ecosystem, AI-driven features, and deep customisation.

#### Core Features

- **Settings Management** — Kubernetes-style ConfigMaps/Secrets; Git-driven configuration
- **Enterprise SSO** — OIDC/SAML federation with role and group claim mapping
- **SCIM Provisioning** — User/group automation from external IdPs
- **Fine-Grained Authorisation** — OpenFGA/SpiceDB for relationship-based access control
  - Markdown-based policies similar to Kubernetes RBAC

#### Intelligence & Recommendations

- **Product Recommendations** — Vector search (Qdrant/Typesense) for behaviour-based suggestions
- **Query Language** — Custom reports, dashboards, and integrations
- **Agents for Buyer Journey** — AI assistance from discovery to post-purchase support
  - MCP (Model Context Protocol) apps for contextual product/user data
  - ACP/UCP (Agentic Commerce Protocol) for agent-executed shopping journeys

#### Extensibility

- **App Marketplace** — Third-party extensions and integrations
  - ERP Connectors (inventory, orders, customer data)
  - CMS Connectors (content management integration)
  
- **Agent Marketplace** — AI agents for platform automation
  - Agent-to-Agent (a2a) Protocol for agent collaboration
  - AgentCard skills and capability discovery

- **Extension Marketplace** — WASI-based extensions
  - Override/enhance checkout, recommendations, asset management, etc.
  - Compare WASM vs OCI approaches

#### Automation & Organization

- **CI/CD: GitStore Actions** — Workflow canvas for catalogue build/test/deploy
- **Namespaces** — Multi-tenant org support (Kubernetes-style, Git-declared)
- **Custom Workflows** — Event-driven automation with custom product lifecycles

---

## Technology Exploration

Open questions and technologies under evaluation for future phases.

### Infrastructure & Storage

| Technology               | Purpose             | Question                                       |
|--------------------------|---------------------|------------------------------------------------|
| **Xet**                  | Git LFS alternative | Should we use Xet for large file storage?      |
| **Parquet**              | Columnar format     | Explore use cases and benefits                 |
| **RocksDB / DuckDB**     | Local storage       | Do they add value beyond alternatives?         |
| **Redis / Valkey**       | Distributed cache   | Best fit for GitStore with in-memory fallback? |
| **Qdrant / Typesense**   | Vector search       | Recommendations and semantic search?           |
| **ScyllaDB / Cassandra** | Distributed DB      | Best fit for GitStore with in-memory fallback? |
| **mmap & io_uring**      | Performance         | High-performance file access patterns          |

### Declarative Architecture Pattern

GitStore can adopt Kubernetes-like declarative patterns:

**Desired State in Git**
- The desired state of the product catalogue is defined in Git
- GitStore runtime converges actual state to desired state
- Enables better version control, collaboration, and rollback

**Catalogue Resource Types**
- Product
- ProductVariant
- Category
- Collection
- Inventory
- File (type: `gitstore.dev/media`)
- [AccessControl](https://kubernetes.io/docs/reference/access-authn-authz/)
- Role / ServiceAccount
- Storage (PersistentVolume / CSI)
- Namespace (type: `gitstore.dev/storefront`)

### CLI-First Philosophy

**Guiding Principles:**
- CLI is the primary interface for developers and AI agents
- Admin UI is optional, built on GraphQL API for non-technical users
- Vendors can build their own Admin UIs (similar to Rancher for Kubernetes)
- Enables automation, scripting, and seamless tool integration

---

## Operational

### Sandbox Environment

**Deployment:** https://sandbox.gitstore.dev

**Configuration:**
- Authentication disabled (easy access and testing)
- Mutations disabled (prevent data loss, ensure stability)
- Reset mechanism to restore known state after testing (if mutations enabled)
- Notification banner (users informed data may reset periodically)

### Code Coverage

- Enable and configure codecov (CI script already in place)
- Add codecov badges to README and documentation
