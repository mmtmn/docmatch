variable "name" {
  description = "Prefix for every resource name (e.g. docmatch-prod)."
  type        = string

  validation {
    condition     = can(regex("^docmatch-[a-z0-9-]+$", var.name))
    error_message = "name must start with docmatch-."
  }
}

variable "env" {
  description = "Environment name."
  type        = string
}

variable "account_id" {
  description = "AWS account id."
  type        = string
}

variable "logs_bucket_name" {
  description = "Bucket that receives CloudTrail logs (its policy must already allow the trail)."
  type        = string
}

variable "cloudtrail_name" {
  description = "Name of the trail; must match the name used in the logs bucket policy."
  type        = string
}

variable "alert_email" {
  description = "Mailbox for alarms and budget notifications."
  type        = string

  validation {
    condition     = can(regex("^[^@\\s]+@[^@\\s]+\\.[a-z]{2,}$", var.alert_email))
    error_message = "alert_email must be an e-mail address."
  }
}

variable "mrr_usd" {
  description = "Monthly recurring revenue in USD; the budget is max(budget_floor_usd, budget_fraction * mrr_usd)."
  type        = number
  default     = 0

  validation {
    condition     = var.mrr_usd >= 0
    error_message = "mrr_usd must be zero or positive."
  }
}

variable "budget_floor_usd" {
  description = "Minimum monthly budget in USD."
  type        = number
  default     = 20
}

variable "budget_fraction" {
  description = "Fraction of MRR allowed for infrastructure."
  type        = number
  default     = 0.10
}

variable "budget_project_tag" {
  description = "Value of the Project cost allocation tag the budget filters on. Empty tracks the whole account."
  type        = string
  default     = "docmatch"
}

variable "burst_budget_usd" {
  description = "Monthly budget in USD of the pool of on-demand workers (0 creates none). Size it from DM_BURST_MONTHLY_HOURS times the on-demand price of the largest pool type."
  type        = number
  default     = 0

  validation {
    condition     = var.burst_budget_usd >= 0
    error_message = "burst_budget_usd must be zero or positive."
  }
}

variable "burst_tag_key" {
  description = "Cost allocation tag key that marks the pool's resources (module compute)."
  type        = string
  default     = "docmatch:pool"
}

variable "burst_tag_value" {
  description = "Value of burst_tag_key on the pool's resources."
  type        = string
  default     = "burst"
}

# The switches below are booleans known at plan time; the names and ids they guard may be
# unknown until apply (a count derived from `id != ""` fails the first plan).
variable "app_alarms_enabled" {
  description = "Create the EC2 alarms on app_autoscaling_group_name (module compute, output app_enabled)."
  type        = bool
  default     = false
}

variable "app_autoscaling_group_name" {
  description = "Application Auto Scaling group watched by the EC2 alarms when app_alarms_enabled."
  type        = string
  default     = ""
}

variable "lambda_function_names" {
  description = "Sandbox function names aggregated into one errors alarm."
  type        = list(string)
  default     = []
}

variable "dead_letter_queue_names" {
  description = "SQS dead-letter queues by a static key (the alarm name suffix); any visible message raises an alarm."
  type        = map(string)
  default     = {}
}

variable "cloudfront_alarm_enabled" {
  description = "Create the 5xx error rate alarm on cloudfront_distribution_id."
  type        = bool
  default     = false
}

variable "cloudfront_distribution_id" {
  description = "Distribution watched for 5xx error rate when cloudfront_alarm_enabled."
  type        = string
  default     = ""
}

variable "log_retention_days" {
  description = "Retention of the application log group."
  type        = number
  default     = 30
}

variable "tags" {
  description = "Extra tags merged into every resource."
  type        = map(string)
  default     = {}
}
