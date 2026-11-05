variable "name" {
  description = "Prefix for resource names (e.g. docmatch-prod)."
  type        = string
}

variable "env" {
  description = "Environment name (dev, staging, prod); becomes the SSM path segment /docmatch/<env>."
  type        = string

  validation {
    condition     = contains(["dev", "staging", "prod"], var.env)
    error_message = "env must be dev, staging or prod."
  }
}

variable "secret_names" {
  description = "Environment variable names declared as SecureString parameters. Their values are never set by Terraform; the owner writes them with the AWS CLI (see infra/README.md)."
  type        = list(string)

  validation {
    condition     = alltrue([for n in var.secret_names : can(regex("^[A-Z][A-Z0-9_]*$", n))])
    error_message = "secret names must be UPPER_SNAKE_CASE environment variable names."
  }
}

variable "optional_secret_names" {
  description = "Secret parameters whose feature stays off while they are empty: created with empty_placeholder, which never blocks the boot (render-env renders it as an empty value)."
  type        = list(string)
  default     = []

  validation {
    condition     = alltrue([for n in var.optional_secret_names : can(regex("^[A-Z][A-Z0-9_]*$", n))])
    error_message = "optional secret names must be UPPER_SNAKE_CASE environment variable names."
  }
}

variable "empty_placeholder" {
  description = "Sentinel SSM holds for an empty value (SSM refuses \"\"); render-env.sh renders it as an empty variable (DM_EMPTY_VALUE)."
  type        = string
  default     = "__EMPTY__"
}

variable "config_values" {
  description = "Non-secret environment variables (bucket names, URLs, modes) managed by Terraform as String parameters."
  type        = map(string)
  default     = {}

  validation {
    condition     = alltrue([for n, v in var.config_values : can(regex("^[A-Z][A-Z0-9_]*$", n))])
    error_message = "config keys must be UPPER_SNAKE_CASE environment variable names."
  }
}

variable "placeholder" {
  description = "Value written once when a secret parameter is created. The render script refuses to start services while any secret still holds it."
  type        = string
  default     = "__UNSET__"
}

variable "tags" {
  description = "Extra tags merged into every parameter."
  type        = map(string)
  default     = {}
}
