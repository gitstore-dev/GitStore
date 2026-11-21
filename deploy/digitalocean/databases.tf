# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

resource "digitalocean_database_cluster" "oidc" {
  name                 = "${var.name}-oidc"
  engine               = var.postgres.engine
  version              = var.postgres.version
  size                 = var.postgres.size
  region               = var.region
  node_count           = 2
  private_network_uuid = digitalocean_vpc.gitstore.id

  lifecycle {
    prevent_destroy = true
  }
}

resource "digitalocean_database_db" "oidc" {
  for_each   = toset(["hydra", "kratos"])
  cluster_id = digitalocean_database_cluster.oidc.id
  name       = each.key

  lifecycle {
    prevent_destroy = true
  }
}

resource "digitalocean_database_user" "oidc" {
  for_each   = toset(["hydra", "kratos"])
  cluster_id = digitalocean_database_cluster.oidc.id
  name       = each.key
}

resource "digitalocean_database_firewall" "oidc" {
  cluster_id = digitalocean_database_cluster.oidc.id
  rule {
    type  = "tag"
    value = digitalocean_tag.role["app"].name
  }
}

resource "digitalocean_spaces_bucket" "backups" {
  count         = var.backup_bucket == null ? 0 : 1
  name          = var.backup_bucket.name
  region        = var.backup_bucket.region
  acl           = "private"
  force_destroy = false
  versioning {
    enabled = true
  }

  lifecycle {
    prevent_destroy = true
  }
}
