# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

variable "name" {
  description = "Unique resource prefix. Keep stable after creating persistent resources."
  type        = string
  default     = "gitstore-production"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{2,35}$", var.name))
    error_message = "name must be 3–36 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "region" {
  description = "One region for all compute, the VPC, volumes and PostgreSQL."
  type        = string
  default     = "ams3"
}

variable "vpc_cidr" {
  description = "Unused RFC1918 IPv4 /16–/24 subnet; avoid overlaps with operator networks."
  type        = string
  default     = "10.120.0.0/20"
  validation {
    condition = can(cidrnetmask(var.vpc_cidr)) && can(regex("/(1[6-9]|2[0-4])$", var.vpc_cidr)) && (
      can(regex("^10\\.", var.vpc_cidr)) || can(regex("^192\\.168\\.", var.vpc_cidr)) ||
      can(regex("^172\\.(1[6-9]|2[0-9]|3[01])\\.", var.vpc_cidr))
    )
    error_message = "vpc_cidr must be an RFC1918 IPv4 subnet with a /16 through /24 prefix."
  }
}

variable "ssh_key_ids" {
  description = "Existing DigitalOcean SSH key IDs or fingerprints. No private keys."
  type        = set(string)
  validation {
    condition     = length(var.ssh_key_ids) > 0
    error_message = "Supply at least one existing SSH key."
  }
}

variable "operator_cidrs" {
  description = "Explicit operator IPv4 CIDRs permitted to SSH; no public-wide access."
  type        = set(string)
  validation {
    condition = length(var.operator_cidrs) > 0 && alltrue([
      for cidr in var.operator_cidrs : can(cidrnetmask(cidr)) && try(tonumber(split("/", cidr)[1]) >= 8, false)
    ])
    error_message = "Supply operator IPv4 CIDRs with /8 or narrower masks; /0 is forbidden."
  }
}

variable "base_domain" {
  description = "Deployment base domain. API and identity use api.<base> and id.<base>."
  type        = string
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9.-]*[a-z0-9])?\\.[a-z]{2,}$", var.base_domain)) && !strcontains(var.base_domain, "..")
    error_message = "Supply a lowercase domain name without a scheme, path, wildcard or trailing dot."
  }
}

variable "certificate_name" {
  description = "Existing DigitalOcean certificate covering base_domain, api.base_domain and id.base_domain."
  type        = string
  validation {
    condition     = length(trimspace(var.certificate_name)) > 0
    error_message = "Supply an existing certificate name."
  }
}

variable "dns_zone" {
  description = "Existing DigitalOcean DNS zone. Null leaves DNS entirely unmanaged; never creates or replaces a zone."
  type        = string
  default     = null
  validation {
    condition     = var.dns_zone == null ? true : (var.base_domain == var.dns_zone || endswith(var.base_domain, ".${var.dns_zone}"))
    error_message = "base_domain must be within dns_zone."
  }
}

variable "host_sizes" {
  description = "Application pair, singleton Git host and three dedicated Scylla hosts. Confirm region availability and measure capacity."
  type = object({
    app    = optional(string, "s-4vcpu-8gb")
    git    = optional(string, "s-2vcpu-4gb")
    scylla = optional(string, "g-4vcpu-16gb")
  })
  default = {}
}

variable "host_image" {
  description = "Ubuntu 24.04 image slug or immutable prepared image ID. Changing it requires planned host replacement."
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "git_volume_gib" {
  type        = number
  description = "Retained singleton Git storage, including operation receipts and UID tombstones."
  default     = 100
  validation {
    condition     = var.git_volume_gib >= 10 && floor(var.git_volume_gib) == var.git_volume_gib
    error_message = "git_volume_gib must be an integer of at least 10."
  }
}

variable "checkpoint_volume_gib" {
  type        = number
  description = "Separate retained controller checkpoint volume per application host."
  default     = 10
  validation {
    condition     = var.checkpoint_volume_gib >= 1 && floor(var.checkpoint_volume_gib) == var.checkpoint_volume_gib
    error_message = "checkpoint_volume_gib must be a positive integer."
  }
}

variable "postgres" {
  description = "OIDC PostgreSQL primary plus standby. Confirm engine/size/version eligibility for the account and region."
  type = object({
    engine  = optional(string, "advanced_pg")
    size    = optional(string, "gd-2vcpu-8gb")
    version = optional(string, "16")
  })
  default = {}
  validation {
    condition     = contains(["pg", "advanced_pg"], var.postgres.engine)
    error_message = "postgres.engine must be pg or advanced_pg."
  }
}

variable "backup_bucket" {
  description = "Optional private versioned Spaces destination in a different region. Does not schedule backups or create state storage."
  type = object({
    name   = string
    region = string
  })
  default = null
  validation {
    condition     = var.backup_bucket == null ? true : var.backup_bucket.region != var.region
    error_message = "Place the backup destination in a region different from compute."
  }
}
