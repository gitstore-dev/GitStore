# Infrastructure command instructions

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
