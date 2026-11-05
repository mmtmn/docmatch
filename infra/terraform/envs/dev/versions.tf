terraform {
  required_version = ">= 1.17.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.71"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5.28"
    }
  }

  # Partial configuration: bucket, key, region and use_lockfile come from backend.hcl, written by
  # infra/scripts/bootstrap-state.sh (it carries the account id, so it is never committed).
  backend "s3" {}
}
