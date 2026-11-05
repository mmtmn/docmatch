provider "aws" {
  region = var.region

  # Refuses to run against any other account, whatever credentials are in the environment.
  allowed_account_ids = [var.account_id]

  default_tags {
    tags = {
      Project     = "docmatch"
      Environment = local.env
      ManagedBy   = "terraform"
      Repository  = "docmatch/infra"
    }
  }
}

# Token from CLOUDFLARE_API_TOKEN. Only needed when cloudflare_account_id is set; with it empty the
# r2 module is not instantiated and the provider is never configured.
provider "cloudflare" {}
