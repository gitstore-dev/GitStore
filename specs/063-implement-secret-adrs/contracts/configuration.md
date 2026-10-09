# Contract: Typed, Nested Controller Configuration

**Design revision**: 2026-10-03, inspected against checkout `d065d08`.
This contract supersedes the original plan's instruction to retain flat
controller config names. The loader and command/config producers now implement
this canonical tree; deployed replacement evidence is tracked separately.

## Loading convention

Use the API's existing convention, also matched by the Rust Git service:

- Viper prefix `GITSTORE` produces the environment prefix `GITSTORE_`.
- Dots in a configuration path become `__`; underscores within a leaf name
  remain underscores.
- Register every supported leaf with a default, including empty optional
  fields, so environment-only values participate in `Unmarshal`.
- Decode once into nested, tagged config structs and validate those structs.
  Consumers receive typed fields, not Viper instances or string lookups.
- Preserve precedence: process environment > selected configuration file >
  defaults. Optional `.env` loading must not override existing process values.
- Keep optional discovery for `Load()` and required-file semantics for
  `LoadFrom(path)`. Do not change file selection or existing API defaults.

Both Go services already configure the prefix and replacer. The controller
divergence is `bindServiceAccountEnvironment` translating nested-looking
environment names into flat TOML fields, followed by `readServiceAccountConfig`
manually overwriting the unmarshalled struct. Remove both functions when the
canonical nested schema is wired. No parallel `GetString` hydration path,
per-leaf environment alias table, or redundant duplicate assignments.

API's pre-environment inspection of `auth.serviceaccount.signing_key` is
different: it preserves source provenance to reject keys in shared files even
when an environment override exists. Preserve that safety check; "decode once"
does not prohibit deliberate source validation.

## Canonical tree

```text
Config
  Controller
    Port
    ApiURI
    ServiceAccount
      Namespace
      Name
      UID
      KeyID
      KeyRef { Kind, Name, Key }
      AssertionAudience
      AccessTokenAudience
    SecretProviders
      Bootstrap { Type, Format, BasePath, EnvPrefix }
    Checkpoint { Dir, FlushIntervalEvents }
    Reconcile { MaxAttempts, StallThreshold }
    Watch { MaxBackoff, ResyncInterval }
  Log { Level, Format }
```

Map each node to its lowercase `mapstructure` name below. The root alone owns
`controller`; child structs do not repeat it. `controller.controller.*` is an
error, not an alternative path. Config structs belong to the service; convert
provider config to shared resolver options at the construction boundary rather
than making a Viper dependency part of `secretmaterial`.

`controller.secret_providers.runtime` is reserved for a future production
consumer, not an accepted unused configuration block in this release. Runtime
contract tests construct provider bindings directly. Do not advertise an
unwired runtime provider in sample config.

## Complete relocation map

Paths below are relative to `controller`.

| Former TOML path                                      | Canonical TOML path                                    |
|-------------------------------------------------------|--------------------------------------------------------|
| `serviceaccount_namespace`                            | `serviceaccount.namespace`                             |
| `serviceaccount_name`                                 | `serviceaccount.name`                                  |
| `serviceaccount_uid`                                  | `serviceaccount.uid`                                   |
| `serviceaccount_key_id`                               | `serviceaccount.key_id`                                |
| `serviceaccount_key_ref.kind/name/key`                | `serviceaccount.key_ref.kind/name/key`                 |
| `serviceaccount_assertion_audience`                   | `serviceaccount.assertion_audience`                    |
| `serviceaccount_access_token_audience`                | `serviceaccount.access_token_audience`                 |
| `secret_provider_bootstrap.type/base_path/env_prefix` | `secret_providers.bootstrap.type/base_path/env_prefix` |
| New provider format                                   | `secret_providers.bootstrap.format`                    |
| `checkpoint_dir`                                      | `checkpoint.dir`                                       |
| `checkpoint_flush_interval_events`                    | `checkpoint.flush_interval_events`                     |
| `default_max_attempts`                                | `reconcile.max_attempts`                               |
| `default_stall_threshold`                             | `reconcile.stall_threshold`                            |
| `max_watch_backoff`                                   | `watch.max_backoff`                                    |
| `resync_interval`                                     | `watch.resync_interval`                                |

`controller.port`, `controller.api_uri` and `log.*` remain unchanged. Defaults
and units remain unchanged by relocation: port 5001, max attempts 5, stall
threshold 5m, checkpoint directory `/var/lib/gitstore/checkpoints`, flush count
100 events, watch backoff 30s and resync 10m. `0s` still disables resync.
Use `time.Duration` fields decoded once with the supported duration hook, with
canonical field-specific validation; do not retain both string and duration
copies solely for manual hydration.

The `/graphql` default is the current Admin endpoint, not a permanent promise
about shared-listener routing. ADR-0012's future N/N+1/N+2 endpoint migration is
independent of these key renames; no early move to an unsupported path, and no
removal of its deprecated endpoint alias under the no-config-alias policy.
Dedicated Admin listeners continue to use `/graphql`.

Examples of mechanically derived environment names:

```text
controller.serviceaccount.namespace
  -> GITSTORE_CONTROLLER__SERVICEACCOUNT__NAMESPACE
controller.serviceaccount.key_ref.name
  -> GITSTORE_CONTROLLER__SERVICEACCOUNT__KEY_REF__NAME
controller.secret_providers.bootstrap.base_path
  -> GITSTORE_CONTROLLER__SECRET_PROVIDERS__BOOTSTRAP__BASE_PATH
controller.checkpoint.flush_interval_events
  -> GITSTORE_CONTROLLER__CHECKPOINT__FLUSH_INTERVAL_EVENTS
controller.watch.max_backoff
  -> GITSTORE_CONTROLLER__WATCH__MAX_BACKOFF
controller.reconcile.stall_threshold
  -> GITSTORE_CONTROLLER__RECONCILE__STALL_THRESHOLD
```

Existing explicitly bound `SERVICEACCOUNT__...` environment names become the
natural mapping and remain valid. Former mechanically derived flat names
(`SERVICEACCOUNT_NAMESPACE`, `SERVICEACCOUNT_KEY_REF__NAME`, etc.) are rejected.
Former `SECRET_PROVIDER_BOOTSTRAP__...`, `CHECKPOINT_DIR`,
`CHECKPOINT_FLUSH_INTERVAL_EVENTS`, `DEFAULT_MAX_ATTEMPTS`,
`DEFAULT_STALL_THRESHOLD`, `MAX_WATCH_BACKOFF` and `RESYNC_INTERVAL` names are
rejected and mapped to actionable replacement names.

Example target TOML for a whole-record identity reference:

```toml
[controller]
port = 5001
api_uri = "http://localhost:4000/graphql"

[controller.serviceaccount]
namespace = "controllers"
name = "gitstore-controller-manager"
uid = "<enrolled-service-account-uid>"
assertion_audience = "gitstore-api/serviceaccount-token"
access_token_audience = "gitstore-api"

[controller.serviceaccount.key_ref]
kind = "SecretRef"
name = "controller-signing-key"

[controller.secret_providers.bootstrap]
type = "file"
format = "json-record"
base_path = "/run/secrets"

[controller.checkpoint]
dir = "/var/lib/gitstore/checkpoints"
flush_interval_events = 100

[controller.reconcile]
max_attempts = 5
stall_threshold = "5m"

[controller.watch]
max_backoff = "30s"
resync_interval = "10m"
```

The UID placeholder must be replaced. Neither raw key bytes nor a key-file
path appears in the identity reference. Provider format and static-ID versus
record-ID validation follow [bootstrap-identity.md](bootstrap-identity.md).

## Mandatory migration, no legacy aliases

The user selected migration before deployment. Reject obsolete controller
keys at startup with a safe old-path -> new-path diagnostic. Reject legacy
keys even if a canonical value overrides them or both values agree; otherwise
stale configuration is silently retained. No automatic aliases, normalization
of legacy values, or fallback to defaults for renamed keys.

Inspect source keys before defaults/decoding erase presence information:

- File checks cover the service-owned `controller`/`log` subtrees. Do not
  reject valid sibling sections (`api`, `auth`, `git`, etc.) in shared TOML.
- Environment checks cover `GITSTORE_CONTROLLER__...` and service-owned log
  settings, including values loaded from `.env`. Ignore other service prefixes
  and provider material variables such as `GITSTORE_SECRET__...`.
- Reject unknown controller keys, malformed nesting and removed spellings.
  Known removed keys get an explicit replacement path. Never echo values.
- Missing/invalid new credentials still fail closed; no provider access or
  authenticated work happens before configuration validation succeeds.

File changes directly under the alpha contract without migration tooling. Raw provider
material compatibility does not mean legacy configuration names are accepted.
Deploy new instances with their own migrated config/environment snapshot;
leave old instances on their compatible snapshot during rolling replacement.
Do not rewrite one shared config mount that a restarting old instance still
needs. Rollback restores both the prior binary and its matching config
snapshot. UID, enrolled key, logical reference, audiences and checkpoint
directory must retain the same effective values across the rename.

Checkpoint roots are assigned to the configured controller group/deployment,
never shared with an unrelated group or different watch scope (ADR-0018).
Preserve current per-kind snapshots, cursors and related replay keys; secondary
watches belong to their consuming group. A checkpoint is a recovery hint, not
a lease/fence. Test separate group roots and safe replay after replacement.
Preserve #435 runner tracking and bounded final flush when updating startup
consumers; the config rename must not lose shutdown checkpoint persistence.

## Wiring and acceptance

Implementation must update the entire chain in one coherent change:

- Controller nested structs, defaults, decoding and validation; all startup,
  checkpoint, watch, reconciliation, logging and test consumers.
- `config/config.toml`, `.env` examples, `compose.local.yml`,
  `compose.capacity.yml` and other deployment config consumers.
- `gitctl enroll-serviceaccount` emitted environment/config instructions,
  bootstrap scripts and Makefile checkpoint cleanup/config targets.
- `scripts/check-local-compose-config.sh`, config/Compose tests, capacity
  effective-config manifests, environment fixtures and documentation.
- API config tests prove the established convention/source-provenance checks
  are preserved; Rust tests retain prefix/separator parity. No unrelated API
  or Rust config redesign is required.

Test env-only, TOML-only and mixed sources for every relocated field; verify
typed values, unchanged defaults/units, zero/false/empty behavior, duration
errors, missing required files, and canonical error paths. Explicitly test
the environment-only leaves Viper could otherwise miss during `Unmarshal`.
Prove every obsolete file/env spelling fails, including overridden legacy
values and `controller.controller.*`; valid other-service config must load.
Config diagnostics must contain paths only, never material.

Use a common convention, not a generic shared configuration framework solely
to satisfy duplicate-code inspection. Remove the redundant controller
hydration/alias code. Extract a helper only if actual repeated mechanics
justify it; keep service-specific defaults, validation and provenance local.
