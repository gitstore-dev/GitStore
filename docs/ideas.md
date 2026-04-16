# Roadmap

This document outlines the roadmap for the development of our project. It includes the planned features, milestones, and timelines for the upcoming releases.

## Platform Shape (Core vs Optional)

- **Core GitStore runtime (required):** `git-server` and `api` only.
- **Optional capability modules:** recommendations, OIDC, user management, and other integrations are deployable add-ons.
- **Local-first adoption principle:** a single bootstrap script should run GitStore locally with zero heavy infrastructure requirements.
- **In-memory first defaults:** use in-memory storage/cache for local development by default.
- **Production-ready upgrades (optional):** external infrastructure like ScyllaDB/Cassandra, Redis/Valkey, vector databases, and external identity services can be enabled incrementally.

### Optional Modules and Dependencies

- **Recommendation module:** requires an optional vector database (for example Qdrant or Typesense vector capabilities).
- **OIDC module:** requires an optional OIDC service deployment.
- **User management module:** requires an optional identity/user-management service deployment.
- **Caching module:** optional Redis or Valkey, with in-memory fallback.
- **Order management persistence acceleration:** optional distributed stores for high-scale deployments, with in-memory fallback.

## Planned Features

- **Improvement to Catalog Frontmatter**: Kubernetes-style frontmatter for better configuration management and flexibility. `apiVersion`, `kind`, `metadata`, `spec` fields will be added to product, category, and collection files to enhance organization and maintainability.
- **Basket Management**: Implement a robust basket management system that allows users to easily add, remove, and update items in their shopping cart.
- **Checkout Process**: Develop a seamless checkout process that integrates with various payment gateways and provides a smooth user experience.
- **Order Tracking**: Enable users to track their orders in real-time, providing updates on the status of their shipments and estimated delivery times.
- **User Profiles**: Create user profiles that allow customers to manage their personal information, view order history, and save their preferences.
- **Product Recommendations**: Implement a recommendation engine that suggests products based on user behavior and preferences.
- **Inventory Management**: Develop an inventory management system that allows merchants to easily track and manage their stock levels.
- **Expressions in Product Files**: Allow merchants to use expressions in product files for dynamic pricing, inventory management, and other use cases.
- **References in catalog files**: Enable references in catalog files to allow for more flexible and maintainable product, category, and collection definitions.
- **App Marketplace**: Create an app marketplace where third-party developers can create and sell extensions and integrations for our platform.
  - **ERP Connectors**: Develop connectors for popular ERP systems to allow merchants to easily integrate their existing systems with our platform. Inventory management, order processing, and customer data synchronization will be key features of these connectors.
  - **CMS Connectors**: Create connectors for popular CMS platforms to enable seamless content management and integration with our ecommerce platform.
- **Agent Marketplace**: Develop an agent marketplace where users can create and share AI agents that automate various tasks within the ecommerce platform.
  - **a2a protocol**: Define and implement an agent-to-agent communication protocol that allows agents to interact and collaborate effectively. AgentCard skills and capabilities will be designed to be easily discoverable and usable by other agents in the marketplace.
- **Extension Marketplace**: Create WASI-based extensions that can be easily integrated into the platform to override or enhance existing functionality. These extensions will be [text](about:blank#blocked)designed to override or enhance critical parts of the platform, such as the checkout process, recommendation engine, asset/image management, and more, allowing for a high degree of customization and flexibility for merchants.
  - Compare using WASM over OCI for extensions - pros and cons of each approach, potential use cases, and implementation considerations.
- **Agents for the Buyer Journey**: Create AI agents that assist customers throughout their shopping experience, from product discovery to post-purchase support. These agents will provide personalized recommendations, answer customer inquiries, and help with order management.
  - **MCP Apps**: Model Context Protocol (MCP) apps will be developed to enable agents to access and utilize contextual information about products, user preferences, and shopping behavior to provide more relevant and personalized assistance to customers.
  - **ACP and UCP**: Agentic Commerce Protocol (ACP) and Universal Commerce Protocol (UCP) will be designed to enable entire shopping journeys and checkout flows to be executed by agents, providing a seamless and automated shopping experience for customers.
- **Query Language**: Develop a powerful and flexible query language that allows users to easily retrieve and manipulate data within the platform. This will enable merchants and developers to create custom reports, dashboards, and integrations with ease.
- **CI/CD**: Implement _GitStore Actions_, a CI/CD pipeline with a worklow canvas for designing the build, test, and deployment processes of product catalogs.
- **Namespaces**: Introduce namespaces to allow for better organisation by userspace, organisation or enterprise. This will enable multiple teams or departments to manage their own catalogs and configurations within the same platform without conflicts.

## Technology Exploration

- **Xet**: Alternative to LFS?
- **Parquet**: What are the use cases?
- **mmap and io_uring**: Efficient storage and retrieval?
- **RocksDB or DuckDB**: Embedded databases for catalog management?
- **Redis or Valkey**: KV store for caching and fast access?
- **Qdrant or Typesense**: Vector search for product recommendations and search?
- **ScyllaDB or Cassandra**: Distributed databases for scalability and high availability?

### OIDC and User Management (OSS Options)

- **Ory Hydra (OIDC/OAuth2 engine):** standards-focused OIDC/OAuth2 server that intentionally decouples protocol from user management.
- **Dex (OIDC federation engine):** OIDC provider with connector-based federation to LDAP, SAML, OIDC, GitHub, and others.
- **Keycloak (integrated IAM option):** full IAM suite with OIDC, SAML, user federation, admin UI, and built-in user management.

### Headless User Management (OSS Options)

- **Ory Kratos:** headless identity and user-management APIs for registration, login, account recovery, MFA, and profile lifecycle.
- **ZITADEL (OSS self-hostable):** IAM platform with user and organization management, OIDC/OAuth2/SAML support.
- **SuperTokens (OSS core):** modular auth stack for sign-in, sessions, and user account management with self-hosting support.

### Suggested Pairings for GitStore

- **Hydra + Kratos:** strict separation of OIDC protocol and user lifecycle, aligned with decoupled architecture.
- **Dex + existing enterprise IdP/LDAP:** best when customers already have identity providers and need federation quickly.
- **Keycloak only:** fastest single-service path when operational simplicity is preferred over service separation.

## Sandbox Environment

- Deploy to https://sandbox.gitstore.dev
- Disable authentication for easy access and testing
- Disable mutations to prevent data loss and ensure a stable testing environment
  - If mutation is not disabled, implement a reset mechanism to restore the sandbox to a known state after testing.
  - Add a notification banner to inform users that they are in a sandbox environment and that data may be reset periodically.

## Coverage

- Setup codecov
- Add badges to README and documentation
