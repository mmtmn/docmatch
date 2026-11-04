variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch-."
  }
}

variable "env" {
  description = "Environment name passed to the functions as DM_ENV."
  type        = string
}

variable "region" {
  description = "Region of the functions (used to build ARNs in IAM policies)."
  type        = string
}

variable "account_id" {
  description = "AWS account id (used to build ARNs in IAM policies)."
  type        = string
}

variable "subnet_id" {
  description = "Isolated subnet (no internet route) where the in-VPC functions run."
  type        = string
}

variable "security_group_id" {
  description = "Security group of the in-VPC functions (egress only to the S3 gateway endpoint)."
  type        = string
}

variable "uploads_bucket_name" {
  description = "Name of the uploads bucket (quarantine/, clean/, derived/)."
  type        = string
}

variable "uploads_bucket_arn" {
  description = "ARN of the uploads bucket."
  type        = string
}

variable "signatures_bucket_name" {
  description = "Name of the bucket holding ClamAV signature bundles."
  type        = string
}

variable "signatures_bucket_arn" {
  description = "ARN of the signatures bucket."
  type        = string
}

variable "image_tag" {
  description = "Image tag present in every ECR repository. Empty creates repositories, queues, roles and log groups but no functions (first apply, before any image is pushed)."
  type        = string
  default     = ""
}

variable "functions" {
  description = "Per-function sizing. memory in MB, timeout in seconds, ephemeral (/tmp) in MB, reserved concurrency, and whether the function runs inside the isolated subnet."
  type = map(object({
    memory    = number
    timeout   = number
    ephemeral = number
    reserved  = number
    in_vpc    = bool
  }))
  default = {
    # ClamAV needs the whole signature database in memory.
    scanner = { memory = 2048, timeout = 300, ephemeral = 2048, reserved = 2, in_vpc = true }
    # ffmpeg/transcription pre-processing of media files up to the plan limits.
    mediarunner = { memory = 3008, timeout = 900, ephemeral = 10240, reserved = 2, in_vpc = true }
    # Generated tools: no secrets, no internet, S3 only.
    toolrunner = { memory = 1024, timeout = 300, ephemeral = 2048, reserved = 2, in_vpc = true }
    # Fetches customer URLs; runs outside the VPC so it never sees the private network.
    fetcher = { memory = 512, timeout = 60, ephemeral = 512, reserved = 2, in_vpc = false }
    # freshclam every 6 hours; writes bundles to the signatures bucket.
    signature-updater = { memory = 1024, timeout = 600, ephemeral = 2048, reserved = 1, in_vpc = false }
  }

  validation {
    condition     = alltrue([for k in keys(var.functions) : contains(["scanner", "mediarunner", "toolrunner", "fetcher", "signature-updater"], k)])
    error_message = "functions keys must be scanner, mediarunner, toolrunner, fetcher and signature-updater."
  }
}

variable "signature_update_schedule" {
  description = "EventBridge schedule expression for the signature updater."
  type        = string
  default     = "rate(6 hours)"
}

variable "log_retention_days" {
  description = "Retention of the function log groups."
  type        = number
  default     = 30
}

variable "ecr_keep_images" {
  description = "Number of tagged images kept per repository."
  type        = number
  default     = 10
}

variable "ecr_force_delete" {
  description = "Delete repositories even if they contain images (dev only)."
  type        = bool
  default     = false
}

variable "guardrail_policy_arns" {
  description = "Deny policies attached to every function role."
  type        = list(string)
  default     = []
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
