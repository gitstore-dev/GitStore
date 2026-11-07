# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

output "inventory" {
  description = "Private deployment inventory for configuration management; contains addresses, never credentials. Keep local."
  value = {
    domains = local.domains
    ingress = { address = digitalocean_loadbalancer.public.ip, backend_port = 8080, readiness_path = "/ingress-ready" }
    hosts = { for name, host in digitalocean_droplet.host : name => {
      role         = local.hosts[name].role, private_ip = host.ipv4_address_private, ssh_ip = host.ipv4_address
      volume_mount = try(local.volumes[name].mount, null)
    } }
    api_upstreams     = [for name in sort(tolist(local.apps)) : "${digitalocean_droplet.host[name].ipv4_address_private}:4000"]
    catalog_upstreams = [for name in sort(tolist(local.apps)) : "${digitalocean_droplet.host[name].ipv4_address_private}:6000"]
    git_rpc           = "${digitalocean_droplet.host["git"].ipv4_address_private}:50051"
    scylla            = { contact_points = [for name in sort(tolist(local.scylla)) : "${digitalocean_droplet.host[name].ipv4_address_private}:9042"], replication_factor = 3 }
    postgres = {
      cluster_id = digitalocean_database_cluster.oidc.id
      host       = digitalocean_database_cluster.oidc.private_host
      port       = digitalocean_database_cluster.oidc.port
      databases  = { for name, db in digitalocean_database_db.oidc : name => db.name }
      users      = { for name, user in digitalocean_database_user.oidc : name => user.name }
      sslmode    = "verify-full"
    }
    backup_destination = var.backup_bucket == null ? null : { bucket = digitalocean_spaces_bucket.backups[0].name, region = var.backup_bucket.region }
  }
}
