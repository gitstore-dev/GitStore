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

## Signing and enrollment commands

- `make secret TARGET=signing-key` generates the native controller's matching
  PEM and JSON record. With `DESTINATION_PATH`, the default record is
  `<DESTINATION_PATH>.json`; `RECORD_OUTPUT_PATH` overrides it, and an explicitly
  empty value requests PEM only. Relative output paths resolve from the root.
  Preserve existing keys and identical records; reject differing records.
- `make enroll-controller-serviceaccount` explicitly enrolls the existing
  public key and saves the API-assigned UID. It does not generate private
  material or JSON. Development fixture credentials are for local APIs only.
- `make controller` consumes the existing record and enrolled UID; it must
  not regenerate credentials. Keep `CONTROLLER_*` overrides consistent across
  generation, enrollment and startup.
- `make dev` automates local generation/enrollment and starts all three native
  core services with shutdown trapping. See `../docs/developer-guide.md`.
- Extend the existing Makefile selector families; do not add feature-specific
  public targets.
