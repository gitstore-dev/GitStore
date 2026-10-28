# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

mock_provider "digitalocean" {}

variables {
  ssh_key_ids      = ["12345678"]
  operator_cidrs   = ["203.0.113.10/32"]
  base_domain      = "sandbox.example.com"
  certificate_name = "test-ingress"
}

run "private_topology" {
  command = plan

  assert {
    condition     = length(digitalocean_droplet.host) == 6 && length([for host in local.hosts : host if host.role == "git"]) == 1
    error_message = "Provision exactly two app hosts, three Scylla hosts and one singleton Git host."
  }
  assert {
    condition     = length(digitalocean_volume.data) == 3 && digitalocean_volume.data["app-1"].name != digitalocean_volume.data["app-2"].name
    error_message = "Git and each controller replica require separate persistent volumes."
  }
  assert {
    condition     = digitalocean_database_cluster.oidc.node_count == 2 && length(digitalocean_database_db.oidc) == 2
    error_message = "OIDC requires a primary/standby pair and separate Hydra/Kratos databases."
  }
  assert {
    condition     = length(digitalocean_record.public) == 0 && length(digitalocean_spaces_bucket.backups) == 0
    error_message = "DNS and backup bucket creation must be explicit opt-ins."
  }
  assert {
    condition = alltrue(flatten([
      for firewall in digitalocean_firewall.host : [
        for rule in firewall.inbound_rule : !contains(coalesce(rule.source_addresses, toset([])), "0.0.0.0/0")
      ]
    ]))
    error_message = "No host listener may permit public-wide inbound access."
  }
  assert {
    condition = alltrue([
      for name in ["git", "app-1", "app-2"] : strcontains(digitalocean_droplet.host[name].user_data, "Requires=gitstore-storage.service")
    ])
    error_message = "Docker must depend on successful persistent storage verification."
  }
  assert {
    condition = alltrue([
      for host in digitalocean_droplet.host : can(yamldecode(host.user_data))
    ])
    error_message = "Every host must receive valid cloud-config YAML."
  }
}

run "nested_dns" {
  command = plan
  variables {
    dns_zone = "example.com"
  }
  assert {
    condition     = toset([for record in digitalocean_record.public : record.name]) == toset(["sandbox", "api.sandbox", "id.sandbox"])
    error_message = "DNS names must preserve the configured nested deployment base domain."
  }
}

run "reject_public_ssh" {
  command = plan
  variables {
    operator_cidrs = ["0.0.0.0/0"]
  }
  expect_failures = [var.operator_cidrs]
}

run "root_dns" {
  command = plan
  variables {
    base_domain = "example.com"
    dns_zone    = "example.com"
  }
  assert {
    condition     = toset([for record in digitalocean_record.public : record.name]) == toset(["@", "api", "id"])
    error_message = "A base domain at the DNS zone root must use an apex record."
  }
}

run "reject_same_region_backups" {
  command = plan
  variables {
    backup_bucket = { name = "example-backups", region = "ams3" }
  }
  expect_failures = [var.backup_bucket]
}

run "reject_unrelated_dns_zone" {
  command = plan
  variables {
    dns_zone = "unrelated.example.net"
  }
  expect_failures = [var.dns_zone]
}
