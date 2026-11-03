# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

locals {
  apps   = toset(["app-1", "app-2"])
  scylla = toset(["scylla-1", "scylla-2", "scylla-3"])
  hosts = merge(
    { for name in local.apps : name => { role = "app", size = var.host_sizes.app } },
    { git = { role = "git", size = var.host_sizes.git } },
    { for name in local.scylla : name => { role = "scylla", size = var.host_sizes.scylla } }
  )
  volumes = merge(
    { git = { name = "${var.name}-git", size = var.git_volume_gib, mount = "/srv/gitstore/git" } },
    { for name in local.apps : name => {
      name = "${var.name}-${name}-checkpoints", size = var.checkpoint_volume_gib, mount = "/srv/gitstore/checkpoints"
    } }
  )
  domains = { git = var.base_domain, api = "api.${var.base_domain}", identity = "id.${var.base_domain}" }
}

resource "digitalocean_vpc" "gitstore" {
  name     = var.name
  region   = var.region
  ip_range = var.vpc_cidr
}

resource "digitalocean_tag" "role" {
  for_each = toset(["app", "git", "scylla"])
  name     = "${var.name}-${each.key}"
}

resource "digitalocean_volume" "data" {
  for_each                = local.volumes
  name                    = each.value.name
  region                  = var.region
  size                    = each.value.size
  initial_filesystem_type = "ext4"
  description             = "GitStore ${each.key} retained state"

  lifecycle {
    prevent_destroy = true
  }
}

resource "digitalocean_droplet" "host" {
  for_each          = local.hosts
  name              = "${var.name}-${each.key}"
  region            = var.region
  image             = var.host_image
  size              = each.value.size
  vpc_uuid          = digitalocean_vpc.gitstore.id
  ssh_keys          = var.ssh_key_ids
  tags              = [digitalocean_tag.role[each.value.role].name]
  monitoring        = true
  ipv6              = false
  graceful_shutdown = true
  resize_disk       = false
  # Public interfaces allow package/image downloads without a NAT gateway.
  # All application traffic uses VPC addresses; firewalls restrict ingress.
  volume_ids = contains(keys(local.volumes), each.key) ? [digitalocean_volume.data[each.key].id] : []
  user_data = "#cloud-config\n${yamlencode({
    package_update = true
    packages       = ["docker.io", "docker-compose-v2", "curl", "jq", "xfsprogs"]
    ssh_pwauth     = false
    write_files = [
      {
        path        = "/usr/local/sbin/gitstore-storage"
        permissions = "0755"
        content = templatefile("${path.module}/storage.sh.tftpl", {
          volumes = contains(keys(local.volumes), each.key) ? [local.volumes[each.key]] : []
        })
      },
      {
        path        = "/etc/systemd/system/gitstore-storage.service"
        permissions = "0644"
        content     = file("${path.module}/gitstore-storage.service")
      },
      {
        path        = "/etc/systemd/system/docker.service.d/gitstore-storage.conf"
        permissions = "0644"
        content     = "[Unit]\nRequires=gitstore-storage.service\nAfter=gitstore-storage.service\n"
      },
      {
        path        = "/etc/gitstore/host.json"
        permissions = "0644"
        content = jsonencode({
          role        = each.value.role, replica = each.key, base_domain = var.base_domain
          git_storage = "/srv/gitstore/git", checkpoints = "/srv/gitstore/checkpoints"
        })
      }
    ]
    runcmd = [
      ["install", "-d", "-m", "0700", "/etc/gitstore/secrets"],
      ["install", "-d", "-m", "0755", "/opt/gitstore", "/srv/gitstore"],
      ["systemctl", "daemon-reload"],
      ["systemctl", "enable", "--now", "gitstore-storage.service"],
      ["systemctl", "enable", "docker"],
      ["systemctl", "restart", "docker"]
    ]
  })}"

  # Changing user_data, SSH keys or images must not silently replace a writer,
  # checkpoint owner or Scylla node. Remove only for a reviewed recovery plan.
  lifecycle {
    prevent_destroy = true
  }
}
