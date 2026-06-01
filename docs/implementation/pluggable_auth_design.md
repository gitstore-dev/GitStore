# Pluggable Identity and Access Design

This document defines the target design for making authentication and authorisation pluggable in GitStore, while keeping user management independent.

## Status

- Proposed
- No backwards compatibility required (alpha)

## Goals

- Allow swapping authentication providers without changing resolver or service business logic.
- Allow swapping authorisation engines (for example OpenFGA, OPA) without changing resolver logic.
- Keep user management independent of authentication and authorisation.
- Support both simple local development and production-scale deployments.
- Preserve consistent audit identity fields across providers.

## Non-Goals

- Preserving current demo auth contracts.
- Defining UI flows for admin portal login screens.
- Choosing a single external identity vendor.

## Core Separation

GitStore identity and access is split into three independent planes:

1. Authentication (AuthN): who are you?
2. Authorization (AuthZ): what can you do?
3. User Management (UserDir): where user/profile/group lifecycle is managed?

These planes are configured and deployed independently.

## Canonical Principal Contract

All AuthN providers must produce a provider-agnostic principal used internally everywhere:

```text
Principal {
  subject: string      # stable user/service ID
  issuer: string       # identity authority (IdP)
  tenant: string?      # optional tenant scope
  namespace: string?   # optional namespace scope
  groups: []string
  roles: []string
  scopes: []string
  claims: map[string]any
  auth_method: string  # oidc_jwt, mtls, api_key, etc.
}
```

Resolvers and domain services only consume `Principal`. They must not parse raw provider token formats directly.

## Provider Abstractions

## AuthN Provider Interface

```text
Authenticate(request) -> (Principal, Decision)
Capabilities() -> { authenticate, issue_session, introspect, group_resolution, user_lookup }
```

Candidate providers:

- `none`
- `static-admin`
- `oidc-jwt`
- `oauth2-introspection`
- `mtls`
- `api-key`
- `webhook`

### AuthN Chain

AuthN providers run as an ordered chain (first successful decision wins):

1. mTLS
2. OIDC JWT
3. OAuth2 introspection
4. API key
5. anonymous/no-op (only if explicitly enabled)

## AuthZ Provider Interface

```text
Authorize(principal, action, resource, context) -> Allow|Deny + reason
```

Candidate providers:

- `allow-all` (dev only)
- `rbac-local`
- `openfga`
- `opa`
- `webhook`

## UserDir Provider Interface

```text
GetBySubject(issuer, subject) -> UserProfile?
ListGroups(issuer, subject) -> []string
SearchUsers(query) -> []UserProfile
UpsertProfile(profile) -> optional
Deactivate(issuer, subject) -> optional
```

Candidate providers:

- `none`
- `oidc-readonly`
- `kratos`
- `scim`
- `custom-http`

## Shared Headless User Management

A single headless user management system can be shared independently by:

- the external identity provider (for account/login lifecycle), and
- GitStore (for profile/group lookup),

without coupling GitStore to IdP internals.

Identity mapping key is `(issuer, subject)`.

## Modes and Safety

## `authn=none` Mode

`authn=none` is supported as an explicit no-op mode for local development.

Guardrails:

- must be explicitly configured,
- must emit startup warning,
- should be blocked in production profile unless a force override is set.

## `static-admin` Role

`static-admin` remains useful for:

- bootstrap local setup,
- deterministic integration tests,
- optional break-glass fallback.

It is not intended as the long-term production model.

## Authorization Model Direction

Authorization is always evaluated through the AuthZ abstraction. Direct `isAdmin` claim checks in resolver code should be replaced by policy checks such as:

- `namespace.enterprise.create`
- `namespace.delete.any`
- `repository.write`

Ownership rules are expressed as policy input context, not hardcoded claim logic.

## Suggested Deployment Profiles

## Local Fast Path

- `authn=none`
- `authz=allow-all`
- `userdir=none`

## Local Secure

- `authn=oidc-jwt`
- `authz=rbac-local`
- `userdir=oidc-readonly`

## Production

- `authn=oidc-jwt|oauth2-introspection|mtls`
- `authz=openfga|opa`
- `userdir=kratos|scim|custom-http` (optional)

## Rollout Plan

1. Define internal interfaces and canonical principal.
2. Add provider registry and config-driven provider selection.
3. Introduce AuthZ abstraction and migrate resolver checks to policy calls.
4. Add chain execution and startup capability validation.
5. Add initial providers (`none`, `static-admin`, `oidc-jwt`, `rbac-local`).
6. Add advanced providers (`openfga`, `opa`, `oauth2-introspection`, `mtls`).
7. Add optional UserDir adapters (`kratos`, `custom-http`, `scim`).

## Open Decisions

- Whether extension runtime should be in-process only first, or include external gRPC/webhook from phase 1.
- Whether policy decision logs should be persisted in datastore or log stream only.
- Preferred default production profile (`openfga` vs `opa`) for namespace + repository authorisation semantics.
