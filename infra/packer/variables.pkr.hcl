# Variables of the docmatch AMI build. Account-specific values (subnet, security group, instance
# profile) come from the Terraform outputs of the dev environment and are passed with
# -var/-var-file or PKR_VAR_* (see example.pkrvars.hcl and infra/README.md).

variable "region" {
  description = "Region where the AMI is built and registered."
  type        = string
  default     = "us-east-1"
}

variable "env" {
  description = "Environment tag of the build (the AMI itself is environment-neutral)."
  type        = string
  default     = "dev"
}

variable "instance_type" {
  description = "Build instance type (arm64)."
  type        = string
  default     = "t4g.small"
}

variable "subnet_id" {
  description = "Public subnet of the build instance (Terraform output app_subnet_id)."
  type        = string
}

variable "security_group_id" {
  description = "Security group of the build instance (Terraform output app_security_group_id): no ingress needed, SSM tunnel only. Empty with ssh_interface = \"public_ip\": a temporary group admits SSH from this machine's public IP only."
  type        = string
  default     = ""
}

variable "iam_instance_profile" {
  description = "Instance profile with Session Manager permissions (Terraform output packer_instance_profile_name)."
  type        = string
}

variable "source_ami_name_filter" {
  description = "Name filter of the Canonical Ubuntu 24.04 LTS arm64 images."
  type        = string
  default     = "ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-arm64-server-*"
}

# Canonical's AWS account id: a well-known public constant that identifies official Ubuntu
# images; override only when mirroring images from another trusted owner.
variable "source_ami_owner" {
  description = "AWS account id that owns the source images (Canonical)."
  type        = string
  default     = "099720109477"
}

variable "ami_name_prefix" {
  description = "Prefix of the AMI name; a UTC timestamp is appended."
  type        = string
  default     = "docmatch-ubuntu-24.04-arm64"
}

variable "root_volume_size" {
  description = "Root volume size in GiB."
  type        = number
  default     = 16
}

variable "ssh_interface" {
  description = "How Packer reaches the instance: session_manager (no SSH exposed) or public_ip."
  type        = string
  default     = "session_manager"
}

variable "ansible_use_proxy" {
  description = "Whether the Ansible provisioner goes through Packer's SSH proxy adapter. Set to true if direct connection through the SSM tunnel fails."
  type        = bool
  default     = false
}

variable "ansible_extra_vars" {
  description = "Extra vars for the hardening role."
  type        = map(string)
  default = {
    hardening_profile = "prod"
  }
}

variable "lynis_min_score" {
  description = "Minimum Lynis hardening index; the build fails below it."
  type        = number
  default     = 90
}

variable "deprecate_after_days" {
  description = "Days until the AMI is marked deprecated (rolling AMIs monthly keeps this honest)."
  type        = number
  default     = 90
}
