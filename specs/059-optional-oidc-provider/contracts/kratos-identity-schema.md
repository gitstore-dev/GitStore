# Contract: Kratos Identity Schema for GitStore Users

## Schema file

New file: `deploy/oidc/kratos/identity.schema.json`, referenced from `deploy/oidc/kratos/kratos.yml`'s `identity.schemas` list as the (initially sole) GitStore user identity schema.

```json
{
  "$id": "https://gitstore.dev/schemas/identity.json",
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "GitStore User",
  "type": "object",
  "properties": {
    "traits": {
      "type": "object",
      "properties": {
        "email": {
          "type": "string",
          "format": "email",
          "title": "E-Mail",
          "minLength": 3,
          "ory.sh/kratos": {
            "credentials": {
              "password": { "identifier": true },
              "webauthn": { "identifier": true }
            },
            "recovery": { "via": "email" },
            "verification": { "via": "email" }
          }
        },
        "username": {
          "type": "string",
          "title": "Username",
          "minLength": 3
        }
      },
      "required": ["email", "username"],
      "additionalProperties": false
    }
  }
}
```

This is a standard Kratos identity schema shape (the reference experiment's `dex-oathkeeper-kratos/deploy/kratos/identity.schema.json` follows the same convention for `email`); GitStore's variant adds the required `username` trait so `Principal.Claims["preferred_username"]` (see `data-model.md`) has a source.

## Contract with `gitstore-api`'s `Principal`

This schema is the authoritative source for exactly two of `Principal`'s fields when the active AuthN chain includes `oidc-jwt` pointed at this reference stack:

| `Principal` field | Source | Stability |
|---|---|---|
| `Subject` | Kratos identity `id` (not a trait — server-generated) | Stable for the identity's lifetime |
| `Claims["email"]` | `traits.email` | Mutable — a user can change their email via Kratos's self-service settings flow |
| `Claims["preferred_username"]` | `traits.username` | Mutable — same self-service flow |

Any consumer relying on a GitStore identity's stable identifier (audit logs, ownership fields, authorization policy keyed by user) MUST use `Principal.Subject`, never `Claims["email"]` — this is a direct consequence of Decision 5 in `research.md` and is the same stability guarantee `Principal.Subject` already carries for the `static-admin` provider's JWT `sub` claim.

## Non-goals of this schema

- No `traits.roles` or `traits.groups` field — `Principal.Roles`/`Principal.Groups` are not populated from Kratos by this spec (see `data-model.md`'s explicit exclusion and `spec.md`'s Assumptions).
- No `metadata_admin`/`metadata_public` GitStore-specific extension in this initial schema — reserved as a documented extension point for a future spec if role/tenant metadata sourced from Kratos becomes a requirement, but not defined here to avoid speculative schema design ahead of an actual need (Simplicity/YAGNI).
- No multi-schema support (e.g., a separate schema for a service-account-like identity kind) — this spec defines exactly one identity schema for exactly one kind of GitStore user.
