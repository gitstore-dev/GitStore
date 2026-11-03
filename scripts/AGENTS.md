# Workspace commands, configuration and helper instructions

These instructions also apply to the root `Makefile`, Compose files,
container wiring and configuration.

- Use the root Makefile as the public interface and `make help` for current
  commands. Extend existing selector families (`TARGET`, `PROFILE`,
  `DATASTORE`) and helpers, not feature-specific public targets or overlays.
- Keep command documentation in the relevant README and scoped instructions
  synchronized with supported behavior. Do not describe a proposed command
  as implemented.
- `make git`, `make api` and `make controller` run native services in the
  foreground. `make dev` uses the existing native startup helper and shutdown
  trapping. Follow its current credential prerequisites rather than
  generating, replacing or enrolling keys implicitly.
- `make compose` uses the `local` profile and shared `CONFIG_FILE`
  (default `./config/config.toml`), with memdb by default. `DATASTORE=scylla`
  selects Scylla; `PROFILE=cluster` selects the existing three-node overlay.
  `make scylla` runs Scylla alone. Reuse these files and selectors.
- Use the existing `make ps`, `make logs`, `make stop` and `make down`
  lifecycle interface, with `SERVICE` where supported. Scope actions to
  explicitly owned deployments; never remove another deployment's storage.
- `make compose OBSERVABILITY=otel` uses the existing observability profile.
  Pass the same selector to lifecycle commands. Preserve native telemetry
  and the Git service's singleton storage semantics.
- `make compose IDENTITY=oidc` includes the optional reference provider;
  `make oidc` runs OIDC alone. Keep the same selector for lifecycle actions.
  See [../config/oidc/oidc.env.example](../config/oidc/oidc.env.example).
- Preserve compatibility with `gitstore-dev/quickstart`, which hand-copies
  Compose base/overlay wiring, ports, dependencies, health checks, networks,
  volumes, image names/tags, config schema/defaults, user/policy formats and
  OIDC reference configuration. Flag affected PRs for a quickstart follow-up.
- Use the existing identity/bootstrap helpers for user records, role
  bindings, authentication material and resources. Preserve private material
  and never put credentials in versioned files, logs or diagnostic evidence.
- `gitctl migrate` prepares schema independently. With
  `GITSTORE_API__DATASTORE__SCYLLA__AUTO_MIGRATE=false`, API startup validates
  migration history read-only; the compatible default is `true`. This
  command does not transfer data. Read
  [../.claude/rules/scylla-migrations.md](../.claude/rules/scylla-migrations.md).
- `make clean TARGET=<git-data|controller-checkpoints> CONFIRM=1` removes
  selected local runtime state, never Docker volumes.
- For capacity, profiling, chaos and owned-deployment helpers, read
  [../tests/capacity/AGENTS.md](../tests/capacity/AGENTS.md). Keep
  scenario-specific command details in the existing scenario documentation.

## Infrastructure command instructions

- Reuse the root Makefile selector families and existing helpers.
- `make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=<init|plan|show|apply|output>`
  operates `deploy/digitalocean`. The default action is `plan`; `apply` consumes
  the reviewed saved plan without prompting. Never invoke it from checks.
- `init` requires `INFRA_BACKEND_CONFIG`; `plan` requires `INFRA_VAR_FILE`.
  Use absolute paths to private files and environment-based credentials.
- `make check TARGET=infrastructure PROFILE=digitalocean` runs formatting,
  backend-disabled initialization, validation and mocked topology tests. It is
  opt-in because it needs OpenTofu and the pinned provider download; `TOFU_BIN`
  overrides the executable.
- Provisioning prepares hosts and storage, not running application containers.
  Follow `../docs/runbooks/digitalocean-provisioning.md` for runtime handoff,
  state protection, singleton Git fencing and retained-volume recovery.
