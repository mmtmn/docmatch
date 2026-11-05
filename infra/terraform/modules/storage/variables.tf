variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch-."
  }
}

variable "account_id" {
  description = "AWS account id; appended to bucket names to make them globally unique."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id must be the 12-digit AWS account id."
  }
}

variable "region" {
  description = "Region of the buckets (used in the CloudTrail ARN condition of the logs bucket policy)."
  type        = string
}

variable "force_destroy" {
  description = "Allow terraform destroy to delete non-empty buckets. Only for dev."
  type        = bool
  default     = false
}

variable "uploads_expiration_days" {
  description = "Days after which objects under quarantine/, clean/ and derived/ in the uploads bucket expire. The application deletes earlier per plan; this is the safety net."
  type        = number
  default     = 30
}

variable "verdicts_expiration_days" {
  description = "Days after which unconsumed verdict objects under verdicts/ expire (the worker deletes consumed ones within minutes)."
  type        = number
  default     = 7
}

variable "signatures_expiration_days" {
  description = "Days after which stale signature bundles expire (the updater rewrites them every 6 hours)."
  type        = number
  default     = 7
}

variable "backups_noncurrent_days" {
  description = "Days to keep noncurrent (overwritten or deleted) versions of backup objects."
  type        = number
  default     = 30
}

variable "logs_expiration_days" {
  description = "Days to keep access logs, CloudTrail and CloudFront logs."
  type        = number
  default     = 90
}

variable "cloudtrail_name" {
  description = "Name of the CloudTrail trail allowed to write into the logs bucket."
  type        = string
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}

variable "releases_noncurrent_days" {
  description = "Days an overwritten object version of the releases bucket is kept."
  type        = number
  default     = 7
}

variable "uploads_cors_origins" {
  description = "Site origins (https://<domain>) allowed to upload to the uploads bucket from the browser. Empty creates no CORS rule."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for o in var.uploads_cors_origins : can(regex("^https://[a-z0-9.-]+$", o))])
    error_message = "uploads_cors_origins must be https origins without a path."
  }
}
