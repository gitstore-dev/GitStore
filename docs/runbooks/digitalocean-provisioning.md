# DigitalOcean infrastructure provisioning

The OpenTofu configuration in [`deploy/digitalocean`](../../deploy/digitalocean/)
provisions a fixed, single-region deployment without Kubernetes. It installs
Docker Engine and Compose and prepares persistent storage. It does **not** start
GitStore, Scylla or OIDC containers, install secrets, enroll controllers, perform
migrations, or certify production capacity. Public ingress stays unhealthy until
the application and reverse proxy are configured.

## Provisioned topology

| Resource | Purpose |
| --- | --- |
| Two application Droplets | One API, one controller and one copy of the OIDC serving components per host |
| One Git Droplet | Exactly one active Git service |
| Three dedicated-CPU Scylla Droplets | One Scylla node per host, with local data storage |
| One VPC and three role firewalls | Private communication with restricted service ingress |
| One regional load balancer | HTTPS termination, forwarding to application hosts on port 8080 |
| One Git volume | Retained repositories, operation receipts and UID tombstones |
| Two checkpoint volumes | Separate retained storage for the controller replicas |
| One managed PostgreSQL cluster | Primary and standby, separate Hydra/Kratos databases and users |
| Optional DNS records | Three names in an existing operator-selected zone |
| Optional private versioned Spaces bucket | Backup destination in another region; no backup jobs |

Host sizes are initial defaults, not validated workload capacity. Confirm image,
size, PostgreSQL engine/version/layout and quota availability for the account and
region. PostgreSQL defaults to Advanced Edition with one standby; account
eligibility for Standard Edition standby nodes varies.

## Prerequisites and private configuration

- OpenTofu 1.13 or later and access to download the pinned DigitalOcean provider.
- An existing DigitalOcean SSH key and an explicit operator IPv4 CIDR allowlist.
- An existing DigitalOcean certificate covering the base domain and its `api`
  and `id` names. A wildcard on the parent of a nested base domain is not
  sufficient. Manage issuance and renewal separately.
- An existing private S3-compatible state bucket with versioning, encryption,
  restricted access and verified locking. Provision it separately from this
  stack. The supplied backend example uses Spaces and enables lockfiles.

Copy `terraform.tfvars.example` and `backend.hcl.example` to private locations
and replace their generic values. Do not commit actual deployment names,
domains, CIDRs, inventory, credentials, backend settings or saved plans.
The repository ignores `.tfvars`, state, provider caches and saved plan files.

Provide credentials through the environment or an approved credential helper:

- `DIGITALOCEAN_TOKEN` for resource provisioning.
- `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` for the S3-compatible backend.
- `SPACES_ACCESS_KEY_ID` and `SPACES_SECRET_ACCESS_KEY` only when creating the
  optional backup bucket. These are separate from backend credentials.

OpenTofu state contains provider-generated database credentials even though the
inventory omits them. Sensitive flags do not encrypt state. Protect the remote
state, local `.terraform` metadata and saved plans. Configure OpenTofu state/plan
encryption or a supported encrypted backend as required; do not assume an
AWS-specific server-side encryption option works with every S3-compatible API.
Verify lock acquisition/contention on the chosen backend and tool version before
allowing concurrent operators. Never disable locking to bypass contention.

## Validate, plan and provision

Run from the repository root:

```bash
make check TARGET=infrastructure PROFILE=digitalocean
```

This checks formatting, initializes providers without a backend, validates the
configuration and runs mocked plans. It creates no cloud resources and requires
no DigitalOcean credentials. `TARGET=all` does not include this optional toolchain
check. Set `TOFU_BIN` to override the OpenTofu executable.

Set absolute private paths, then initialize and inspect a real plan:

```bash
export INFRA_BACKEND_CONFIG=/absolute/private/backend.hcl
export INFRA_VAR_FILE=/absolute/private/deployment.tfvars
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=init
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=plan
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=show
```

The saved plan is under `.gitstore/infrastructure/digitalocean/deployment.tfplan`.
Review every change before executing it:

```bash
make bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=apply
```

**Applying a saved plan does not prompt.** Run that action only after reviewing
the saved plan. There is no automatic apply or destroy path. After changing
inputs, create and review a fresh plan. The default action is `plan`.

Export the configuration-management inventory to a protected file:

```bash
umask 077
make --silent bootstrap TARGET=infrastructure PROFILE=digitalocean ACTION=output \
  > /absolute/private/inventory.json
```

Inventory includes private/SSH addresses, API and CatalogService upstreams, the
Git endpoint, Scylla contact points, PostgreSQL connection metadata and storage
paths. It contains no passwords. Treat it as private deployment metadata.
Check `cloud-init status --wait` on every host before installing services.

## Application configuration after provisioning

Use configuration management or a controlled per-host deployment procedure:

1. Pin container images to reviewed release digests. Reuse GitStore service
   configuration and quickstart Caddy routing patterns. The local development
   overlays are not a multi-host production deployment.
2. Run one Scylla node per designated host. Tune I/O, filesystem, memory and CPU
   settings; use private listen/advertised addresses and seeds; configure
   authentication and transport security. Create the keyspace using
   `NetworkTopologyStrategy`, the actual datacenter name and RF=3. Inventory
   metadata alone does not set replication. Run existing `gitctl migrate` before
   starting APIs in validation-only migration mode.
3. Install API signing material and matching API/Git RPC secrets privately.
   Give controllers enrolled signing records and their separate
   `/srv/gitstore/checkpoints` volumes. Configure pluggable AuthN/AuthZ explicitly.
4. Configure Hydra and Kratos with separate PostgreSQL databases/users and
   certificate verification. Retrieve credentials through administrative tooling
   outside cloud-init and OpenTofu outputs. Restrict grants to each service's
   database before startup. Run Ory migrations as controlled one-shot release
   operations. Share required Ory secrets consistently between replicas.
5. Start the singleton Git service on its designated host, mounting the entire
   `/srv/gitstore/git` volume. Configure receiver-bound API-to-Git RPC and
   Git-to-API CatalogService authentication. Callbacks need health-aware
   selection between the two private API upstreams.
6. Start one API and controller per application host. Configure cross-host
   addresses explicitly: Compose DNS is local to each host. A local health-aware
   proxy can select private API/Ory replicas; an internal load balancer would be
   a separate resource. Scylla clients use direct node-aware contact points.
7. Install a reverse proxy on each application host's VPC address, port 8080.
   Route the base domain, `api.<base>` and `id.<base>`. Trust forwarded headers
   only from ingress. Keep Ory Admin, Git RPC, controller operational listeners
   and databases out of public routing.
8. Implement `/ingress-ready` as aggregate readiness for the intended local
   serving components. A constant 200 would send traffic to an unready host.
   Use service-specific upstream health checks as well.

Git smart HTTP goes through the API. On the identity host, exact bridge
login/consent and Kratos UI/public-API prefix routes precede Hydra fallback.
Current GitStore serves `/graphql`: two proxy aliases do not create separate
Admin/Storefront schemas. Enable split endpoints only when supported by the
application version. Use production email delivery and validate issuer,
redirect allowlists, secure cookies and CSRF.

HTTPS terminates at the load balancer; proxy traffic uses HTTP over the VPC.
This is not end-to-end TLS. For encrypted backend transport, change forwarding
and health-check protocols and configure backend certificates before serving
traffic. Database and RPC transport security are separate concerns.

## Private network policy

| Destination | Port | Permitted source |
| --- | --- | --- |
| All hosts, SSH | 22 | Explicit operator CIDRs |
| Application ingress | 8080 | Public managed load balancer |
| API GraphQL | 4000 | Application hosts |
| API CatalogService | 6000 | Git host |
| Kratos public/admin | 4433–4434 | Application hosts |
| Hydra public/admin | 4444–4445 | Application hosts |
| Git RPC | 50051 | Application hosts |
| Scylla CQL | 9042 | Application and Scylla hosts |
| Scylla shard-aware CQL | 19042, 19142 | Application hosts |
| Scylla internode | 7000–7001 | Scylla hosts |
| Managed PostgreSQL | Managed cluster port | Application-host tag |

Publish container listeners on private host IPs, not `0.0.0.0`. Avoid conflicting
host bindings for Hydra Admin and the bridge if their container ports coincide.
Monitoring/admin listeners remain local; use SSH tunnels or narrow private rules
for an explicitly deployed collector. Preserve time synchronization for tokens
and signed RPCs. Network access does not grant application authorization.

Droplets retain public interfaces for outbound updates, image pulls and external
services, with inbound application traffic restricted. Removing public
interfaces requires a separately designed egress and operator-access path.
The host firewall trusts application hosts as a group; application identities
still enforce service boundaries.

DNS is opt-in through `dns_zone`. Only three A records are managed in that
existing zone; no nameservers or zones are changed. Import preexisting matching
records before enabling management. Otherwise create the three names at your
DNS provider using the load balancer address from inventory.

## Storage, rollout and recovery

The storage boot service waits for expected volumes, verifies ext4, refuses to
hide nonempty unmounted directories, mounts without formatting and verifies
the mounted UUID. Docker requires successful verification, preventing startup
against an empty root-filesystem directory when a volume is absent.

Hosts, data volumes, PostgreSQL and the optional backup bucket have
`prevent_destroy`. Image, SSH key and cloud-init changes can require replacement
and will fail planning rather than silently replacing a writer or Scylla node.
Do not remove guards simply to make a plan succeed: review recovery/replacement
first. These guards do not stop manual provider-side deletion or deletion after
removing a resource block, and do not fence processes.

For Git recovery, stop and positively fence the former host before attaching
the retained volume to a replacement. Preserve the whole volume. Never run two
Git processes or use create-before-destroy. Git-dependent operations experience
an interruption. Each checkpoint volume also has one active owner. Scylla node
replacement requires its cluster replacement procedure, not cloning an old
node's identity or data directory.

Roll application hosts one at a time: drain ingress, retain checkpoints, replace
services, verify credentials/readiness/reconciliation, then restore traffic.
Confirm the remaining host carries the workload. Validate Git streaming,
GraphQL subscription reconnect/replay, identity flows, replica consistency and
sustained pushes/reconciliation before declaring production readiness.

The optional bucket is only a destination. Install encrypted,
application-consistent backup jobs and alerts, define retention/recovery
objectives, and test isolated restores across Git, Scylla, PostgreSQL and secret
material. RF=3 and standby nodes do not replace backups. This single-region
topology does not provide regional failover.

## References

- [DigitalOcean Terraform provider](https://registry.terraform.io/providers/digitalocean/digitalocean/latest/docs)
- [Spaces remote state configuration](https://docs.digitalocean.com/products/spaces/reference/terraform-backend/)
- [PostgreSQL engine and layout options](https://docs.digitalocean.com/products/databases/postgresql/reference/api/)
