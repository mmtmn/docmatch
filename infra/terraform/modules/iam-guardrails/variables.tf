variable "name" {
  description = "Prefix for policy and group names (e.g. docmatch-prod)."
  type        = string
}

variable "allowed_regions" {
  description = "Regions where regional API calls are allowed. Everything else is denied (global services excepted)."
  type        = list(string)
  default     = ["us-east-1"]
}

variable "cloudtrail_arn" {
  description = "ARN of the trail whose stopping/deletion is denied."
  type        = string
}

variable "protected_bucket_arns" {
  description = "Buckets whose deletion is denied (backups, Terraform state). Empty in dev to allow destroy. The length must be known at plan time (the ARNs themselves may not be)."
  type        = list(string)
  default     = []
}

variable "protected_kms_key_arns" {
  description = "KMS keys whose deletion or disabling is denied (the DNSSEC key). Build it as `enabled ? [arn] : []`, never with compact() over an unknown ARN: the length must be known at plan time."
  type        = list(string)
  default     = []
}

variable "create_operators_group" {
  description = "Create the IAM group that human operators join; all deny policies are attached to it."
  type        = bool
  default     = true
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
