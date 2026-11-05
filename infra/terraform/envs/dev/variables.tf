variable "account_id" {
  description = "AWS account id this environment lives in."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id must be the 12-digit AWS account id."
  }
}

variable "region" {
  description = "AWS region. CloudFront, ACM and DNSSEC require us-east-1."
  type        = string
  default     = "us-east-1"
}

variable "domain" {
  description = "Public domain of this environment (a Route 53 zone is created for it)."
  type        = string
}

variable "mrr_usd" {
  description = "Monthly recurring revenue in USD; the budget is max(20, 10% of it)."
  type        = number
  default     = 0
}

variable "alert_email" {
  description = "Mailbox for alarms and budget notifications."
  type        = string
}

variable "email_from" {
  description = "Sender of transactional e-mail (DM_EMAIL_FROM), e.g. 'docmatch <no-reply@example.com>'."
  type        = string
}

variable "security_contact_email" {
  description = "Security contact published by the application (DM_SECURITY_CONTACT)."
  type        = string
}

variable "dmarc_rua_email" {
  description = "Mailbox for DMARC aggregate reports. Empty publishes DMARC without rua."
  type        = string
  default     = ""
}

variable "cloudflare_account_id" {
  description = "Cloudflare account id for the R2 media bucket. Empty skips the r2 module (token not available yet)."
  type        = string
  default     = ""
}

variable "ami_id" {
  description = "Hardened AMI from infra/packer. Empty until the first Packer build."
  type        = string
  default     = ""
}

variable "lambda_image_tag" {
  description = "Image tag pushed to every sandbox ECR repository. Empty until the images exist."
  type        = string
  default     = ""
}

variable "worker_desired_capacity" {
  description = "Desired number of spot workers (0 at the tiny stage)."
  type        = number
  default     = 0
}

variable "wait_for_certificate_validation" {
  description = "Wait for ACM validation and serve the domain from CloudFront (needs the zone delegated at the registrar). false on the very first apply: the distribution then runs on the default *.cloudfront.net certificate without the domain aliases."
  type        = bool
  default     = true
}

variable "dnssec_enabled" {
  description = "Sign the zone (KMS key, US$ 1/month). The DS record must be published at the registrar."
  type        = bool
  default     = true
}

variable "enable_flow_logs" {
  description = "VPC flow logs for rejected traffic into the logs bucket."
  type        = bool
  default     = false
}

variable "extra_dns_records" {
  description = "Additional DNS records (DKIM, verifications, delegations)."
  type = list(object({
    name    = string
    type    = string
    ttl     = number
    records = list(string)
  }))
  default = []
}

variable "egress_allowlist" {
  description = "Hosts the egress proxy on the instance may reach (DM_EGRESS_ALLOWLIST, space separated on the box)."
  type        = list(string)
  default = [
    "openrouter.ai",
    "api.stripe.com",
    "api.resend.com",
    "accounts.google.com",
    "oauth2.googleapis.com",
    "www.googleapis.com",
    "appleid.apple.com",
    "acme-v02.api.letsencrypt.org",
    "archive.ubuntu.com",
    "security.ubuntu.com",
    "ports.ubuntu.com",
    "database.clamav.net",
    "api.crowdsec.net",
    "hub-cdn.crowdsec.net",
    "api.snapcraft.io",
    "snapcraftcontent.com",
    "esm.ubuntu.com",
    "dl.cloudsmith.io",
    "apt.postgresql.org",
    "packagecloud.io",
    "download.falco.org",
  ]
}

variable "llm_mode" {
  description = "DM_LLM_MODE for the instance: openrouter, local or off."
  type        = string
  default     = "openrouter"

  validation {
    condition     = contains(["openrouter", "local", "off"], var.llm_mode)
    error_message = "llm_mode must be openrouter, local or off."
  }
}
