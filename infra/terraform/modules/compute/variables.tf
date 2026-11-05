variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch-."
  }
}

variable "env" {
  description = "Environment name written to the instance (DM_ENV)."
  type        = string
}

variable "region" {
  description = "Region of the instances."
  type        = string
}

variable "account_id" {
  description = "AWS account id (used to build ARNs in IAM policies)."
  type        = string
}

variable "ami_id" {
  description = "Hardened AMI produced by infra/packer. Empty creates everything except the launch templates and Auto Scaling groups (first apply, before the Packer build)."
  type        = string
  default     = ""

  validation {
    condition     = var.ami_id == "" || can(regex("^ami-[0-9a-f]{8,17}$", var.ami_id))
    error_message = "ami_id must be empty or an AMI id."
  }
}

variable "instance_type" {
  description = "Instance type of the single application instance."
  type        = string
  default     = "t4g.small"
}

variable "subnet_id" {
  description = "Public subnet of the application instance."
  type        = string
}

variable "availability_zone" {
  description = "Availability zone of the subnet; the data volume is created there."
  type        = string
}

variable "security_group_id" {
  description = "Security group of the application instance."
  type        = string
}

variable "root_volume_size" {
  description = "Root volume size in GiB (gp3, encrypted)."
  type        = number
  default     = 16
}

variable "data_volume_size" {
  description = "Size in GiB of the persistent gp3 data volume (PostgreSQL)."
  type        = number
  default     = 20
}

variable "ssm_path_prefix" {
  description = "SSM Parameter Store path the instance renders into /run/docmatch/env (e.g. /docmatch/prod)."
  type        = string
}

variable "releases_bucket_name" {
  description = "Name of the releases bucket; the instance installs releases/current at boot."
  type        = string
}

variable "releases_bucket_arn" {
  description = "ARN of the releases bucket (application binaries under releases/<sha>/)."
  type        = string
}

variable "uploads_bucket_arn" {
  description = "ARN of the uploads bucket (quarantine/, clean/, derived/, verdicts/)."
  type        = string
}

variable "verdicts_prefix" {
  description = "Key prefix of the verdict objects the scanner writes in the uploads bucket (module sandbox); the worker reads and deletes them."
  type        = string
  default     = "verdicts/"
}

variable "backups_bucket_arn" {
  description = "ARN of the backups bucket (pgBackRest repository under pgbackrest/)."
  type        = string
}

variable "verdict_queue_arn" {
  description = "ARN of the scan verdicts queue (S3 notifications on the verdicts prefix) consumed by the worker."
  type        = string
}

variable "invokable_function_arns" {
  description = "Sandbox function ARNs the application may invoke. Empty until images exist."
  type        = list(string)
  default     = []
}

variable "hosted_zone_id" {
  description = "Route 53 zone where the origin record lives and where Caddy answers DNS-01 challenges."
  type        = string
}

variable "origin_hostname" {
  description = "FQDN CloudFront connects to (the instance's Elastic IP), e.g. origin.docmatch.com.br."
  type        = string
}

variable "log_group_arn" {
  description = "CloudWatch log group the instance may write to. Empty grants nothing."
  type        = string
  default     = ""
}

variable "guardrail_policy_arns" {
  description = "Deny policies attached to every instance role, keyed by a static name (module iam-guardrails, role_policy_arns). The keys drive for_each, so only the ARNs may be unknown until apply."
  type        = map(string)
  default     = {}
}

variable "worker_instance_types" {
  description = "Spot instance types for the stage-1 worker group, in order of preference."
  type        = list(string)
  default     = ["t4g.medium", "m7g.medium", "c7g.large"]
}

variable "worker_max_size" {
  description = "Maximum number of spot workers."
  type        = number
  default     = 2
}

variable "worker_desired_capacity" {
  description = "Desired number of spot workers (0 at the tiny stage; raise by hand or by a later queue-depth policy)."
  type        = number
  default     = 0
}

variable "enable_ebs_snapshots" {
  description = "Daily EBS snapshots of the data volume through Data Lifecycle Manager."
  type        = bool
  default     = true
}

variable "snapshot_retention_count" {
  description = "Number of daily snapshots kept."
  type        = number
  default     = 7
}

variable "snapshot_time_utc" {
  description = "Time of day (UTC, HH:MM) of the daily snapshot. 05:00 UTC is 02:00 in Sao Paulo."
  type        = string
  default     = "05:00"
}

variable "create_packer_profile" {
  description = "Create the instance profile used by the Packer build instance (SSM only). Needed in one environment."
  type        = bool
  default     = false
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
