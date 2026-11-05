variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch- (the account hosts other projects)."
  }
}

variable "account_id" {
  description = "AWS account id that owns the VPC; used to pin the S3 gateway endpoint policy."
  type        = string

  validation {
    condition     = can(regex("^[0-9]{12}$", var.account_id))
    error_message = "account_id must be the 12-digit AWS account id."
  }
}

variable "vpc_cidr" {
  description = "IPv4 CIDR of the VPC. IPv6 is always an Amazon-provided /56."
  type        = string
  default     = "10.40.0.0/16"
}

variable "app_subnet_cidr" {
  description = "IPv4 CIDR of the public subnet that hosts the application instance."
  type        = string
  default     = "10.40.0.0/24"
}

variable "sandbox_subnet_cidr" {
  description = "IPv4 CIDR of the isolated subnet for the sandbox Lambda functions (no internet route)."
  type        = string
  default     = "10.40.16.0/24"
}

variable "availability_zone" {
  description = "Availability zone for both subnets. Empty picks the first available zone of the region."
  type        = string
  default     = ""
}

variable "cloudfront_prefix_list_name" {
  description = "Name of the AWS-managed prefix list with the CloudFront origin-facing IP ranges."
  type        = string
  # Public, well-known name of the managed prefix list; the id differs per region and is looked up.
  default = "com.amazonaws.global.cloudfront.origin-facing"
}

variable "s3_bucket_name_prefix" {
  description = "Only buckets whose name starts with this prefix are reachable through the S3 gateway endpoint."
  type        = string
}

variable "s3_endpoint_on_app_route_table" {
  description = "Also route the application subnet's S3 traffic through the gateway endpoint. The endpoint policy admits only the project's buckets, so third-party artifacts served straight from S3 in the region would be denied; keep it off until a Packer build and a boot have been verified with it on."
  type        = bool
  default     = false
}

variable "enable_flow_logs" {
  description = "Create VPC flow logs (REJECT traffic only) delivered to the logs bucket. Off by default to stay within the bill target."
  type        = bool
  default     = false
}

variable "flow_logs_bucket_arn" {
  description = "ARN of the S3 bucket that receives flow logs. Required when enable_flow_logs is true."
  type        = string
  default     = ""
}

variable "burst_internal_port" {
  description = "TCP port of the application's internal endpoint for the burst workers (DM_BURST_LISTEN)."
  type        = number
  default     = 8444

  validation {
    condition     = var.burst_internal_port > 1024 && var.burst_internal_port < 65536 && var.burst_internal_port != 8443
    error_message = "burst_internal_port must be an unprivileged port other than the admin port (8443)."
  }
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
