# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

data "digitalocean_certificate" "ingress" {
  name = var.certificate_name
}

resource "digitalocean_loadbalancer" "public" {
  name                             = "${var.name}-public"
  region                           = var.region
  type                             = "REGIONAL"
  network                          = "EXTERNAL"
  size_unit                        = 1
  vpc_uuid                         = digitalocean_vpc.gitstore.id
  droplet_tag                      = digitalocean_tag.role["app"].name
  redirect_http_to_https           = true
  enable_backend_keepalive         = true
  http_idle_timeout_seconds        = 600
  disable_lets_encrypt_dns_records = true

  forwarding_rule {
    entry_port      = 80
    entry_protocol  = "http"
    target_port     = 8080
    target_protocol = "http"
  }
  forwarding_rule {
    entry_port       = 443
    entry_protocol   = "https"
    target_port      = 8080
    target_protocol  = "http"
    certificate_name = data.digitalocean_certificate.ingress.name
  }
  # This must be an aggregate ingress readiness endpoint, not just process liveness.
  healthcheck {
    port                     = 8080
    protocol                 = "http"
    path                     = "/ingress-ready"
    check_interval_seconds   = 10
    response_timeout_seconds = 5
    healthy_threshold        = 3
    unhealthy_threshold      = 3
  }
}

# Allow only the role's required cross-host listeners. Docker must publish these
# on the VPC IP, never 0.0.0.0. Tag selection follows replacement hosts.
resource "digitalocean_firewall" "host" {
  for_each = toset(["app", "git", "scylla"])
  name     = "${var.name}-${each.key}"
  tags     = [digitalocean_tag.role[each.key].name]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.operator_cidrs
  }
  dynamic "inbound_rule" {
    for_each = each.key == "app" ? [1] : []
    content {
      protocol                  = "tcp"
      port_range                = "8080"
      source_load_balancer_uids = [digitalocean_loadbalancer.public.id]
    }
  }
  dynamic "inbound_rule" {
    # API GraphQL, CatalogService callbacks, Hydra and Kratos private listeners.
    for_each = each.key == "app" ? toset(["4000", "4433-4434", "4444-4445", "6000"]) : toset([])
    content {
      protocol    = "tcp"
      port_range  = inbound_rule.value
      source_tags = inbound_rule.value == "6000" ? [digitalocean_tag.role["git"].name] : [digitalocean_tag.role["app"].name]
    }
  }
  dynamic "inbound_rule" {
    for_each = each.key == "git" ? [1] : []
    content {
      protocol    = "tcp"
      port_range  = "50051"
      source_tags = [digitalocean_tag.role["app"].name]
    }
  }
  dynamic "inbound_rule" {
    for_each = each.key == "scylla" ? [1] : []
    content {
      protocol    = "tcp"
      port_range  = "9042"
      source_tags = [digitalocean_tag.role["app"].name, digitalocean_tag.role["scylla"].name]
    }
  }
  dynamic "inbound_rule" {
    for_each = each.key == "scylla" ? toset(["19042", "19142"]) : toset([])
    content {
      protocol    = "tcp"
      port_range  = inbound_rule.value
      source_tags = [digitalocean_tag.role["app"].name]
    }
  }
  dynamic "inbound_rule" {
    for_each = each.key == "scylla" ? toset(["7000-7001"]) : toset([])
    content {
      protocol    = "tcp"
      port_range  = inbound_rule.value
      source_tags = [digitalocean_tag.role["scylla"].name]
    }
  }
  inbound_rule {
    protocol         = "icmp"
    source_addresses = [var.vpc_cidr]
  }
  # Egress supports OS updates, image pulls, email and telemetry. Application
  # identities and TLS remain required; VPC membership does not grant AuthZ.
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "53"
    destination_addresses = ["0.0.0.0/0"]
  }
  outbound_rule {
    protocol              = "udp"
    port_range            = "123"
    destination_addresses = ["0.0.0.0/0"]
  }
  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0"]
  }
}

resource "digitalocean_record" "public" {
  for_each = var.dns_zone == null ? {} : local.domains
  domain   = var.dns_zone
  type     = "A"
  name     = each.value == var.dns_zone ? "@" : trimsuffix(each.value, ".${var.dns_zone}")
  value    = digitalocean_loadbalancer.public.ip
  ttl      = 300
}
