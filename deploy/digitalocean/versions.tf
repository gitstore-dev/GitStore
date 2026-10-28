# SPDX-License-Identifier: AGPL-3.0-or-later
# Copyright (c) 2026 GitStore contributors

terraform {
  required_version = ">= 1.13.0, < 2.0.0"

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "2.106.0"
    }
  }

  # Provision state storage separately; never put access keys in this block.
  backend "s3" {}
}

# Credentials resolve from DIGITALOCEAN_TOKEN and, for optional backup storage,
# SPACES_ACCESS_KEY_ID / SPACES_SECRET_ACCESS_KEY.
provider "digitalocean" {}
