variable "name" {
  description = "Prefix for bucket names (e.g. docmatch-prod)."
  type        = string
}

variable "cloudflare_account_id" {
  description = "Cloudflare account id that owns the R2 buckets."
  type        = string

  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_account_id))
    error_message = "cloudflare_account_id must be the 32-character hexadecimal account id."
  }
}

variable "domain" {
  description = "Public domain of the web app, used to build the CORS allowed origins for direct browser uploads."
  type        = string
}

variable "location" {
  description = "R2 location hint. There is no South America hint; enam is the closest to Brazil."
  type        = string
  default     = "enam"

  validation {
    condition     = contains(["apac", "eeur", "enam", "weur", "wnam", "oc"], var.location)
    error_message = "location must be one of apac, eeur, enam, weur, wnam, oc."
  }
}

variable "media_retention_days" {
  description = "Days after which media objects are deleted. The application deletes earlier per plan; this is the safety net."
  type        = number
  default     = 30
}

variable "tags" {
  description = "Unused by the Cloudflare provider; kept for interface symmetry with the AWS modules."
  type        = map(string)
  default     = {}
}
