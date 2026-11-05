# docmatch hardened AMI: Ubuntu 24.04 LTS arm64 + the Ansible role infra/ansible/roles/hardening,
# gated by Lynis (hardening index >= var.lynis_min_score). Packer reaches the build instance through
# Session Manager; SSH is removed from the image at the end.

packer {
  required_version = ">= 1.16.0"

  required_plugins {
    amazon = {
      version = "~> 1.8"
      source  = "github.com/hashicorp/amazon"
    }
    ansible = {
      version = "~> 1.1"
      source  = "github.com/hashicorp/ansible"
    }
  }
}

locals {
  timestamp = regex_replace(timestamp(), "[- TZ:]", "")
  ami_name  = "${var.ami_name_prefix}-${local.timestamp}"
  tags = {
    Name            = local.ami_name
    Project         = "docmatch"
    Environment     = var.env
    ManagedBy       = "packer"
    "docmatch:role" = "app-ami"
    Base            = "{{ .SourceAMIName }}"
    LynisMinScore   = "${var.lynis_min_score}"
  }
}

source "amazon-ebs" "docmatch" {
  region        = var.region
  instance_type = var.instance_type

  ami_name                = local.ami_name
  ami_description         = "docmatch: Ubuntu 24.04 arm64 endurecida (Ansible + Lynis >= ${var.lynis_min_score})"
  ami_virtualization_type = "hvm"
  ena_support             = true
  imds_support            = "v2.0"
  encrypt_boot            = true
  deprecate_at            = timeadd(timestamp(), "${var.deprecate_after_days * 24}h")

  subnet_id                   = var.subnet_id
  security_group_ids          = [var.security_group_id]
  associate_public_ip_address = true
  iam_instance_profile        = var.iam_instance_profile

  ssh_username  = "ubuntu"
  ssh_interface = var.ssh_interface
  ssh_timeout   = "15m"

  source_ami_filter {
    filters = {
      name                = var.source_ami_name_filter
      architecture        = "arm64"
      virtualization-type = "hvm"
      root-device-type    = "ebs"
    }
    owners      = [var.source_ami_owner]
    most_recent = true
  }

  launch_block_device_mappings {
    device_name           = "/dev/sda1"
    volume_size           = var.root_volume_size
    volume_type           = "gp3"
    encrypted             = true
    delete_on_termination = true
  }

  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  run_tags = {
    Name            = "docmatch-packer-build"
    Project         = "docmatch"
    Environment     = var.env
    "docmatch:role" = "packer"
  }
  run_volume_tags = {
    Name    = "docmatch-packer-build"
    Project = "docmatch"
  }
  tags          = local.tags
  snapshot_tags = local.tags
}

build {
  name    = "docmatch"
  sources = ["source.amazon-ebs.docmatch"]

  # cloud-init must finish before apt is touched, or dpkg locks collide.
  provisioner "shell" {
    inline = [
      "cloud-init status --wait >/dev/null 2>&1 || true",
      "sudo apt-get -o DPkg::Lock::Timeout=300 update -q",
    ]
  }

  provisioner "ansible" {
    playbook_file   = "${path.root}/../ansible/site.yml"
    galaxy_file     = "${path.root}/../ansible/requirements.yml"
    user            = "ubuntu"
    use_proxy       = var.ansible_use_proxy
    extra_arguments = ["--extra-vars", jsonencode(var.ansible_extra_vars)]
    ansible_env_vars = [
      "ANSIBLE_CONFIG=${path.root}/../ansible/ansible.cfg",
      "ANSIBLE_HOST_KEY_CHECKING=False",
      "ANSIBLE_NOCOWS=1",
    ]
  }

  # The gate: no AMI below the Lynis threshold.
  provisioner "shell" {
    script           = "${path.root}/../scripts/lynis-gate.sh"
    execute_command  = "chmod +x {{ .Path }}; {{ .Vars }} sudo -E bash {{ .Path }}"
    environment_vars = ["LYNIS_MIN_SCORE=${var.lynis_min_score}"]
  }

  # Removes SSH and build residue, initialises the AIDE database last.
  provisioner "shell" {
    script            = "${path.root}/scripts/finalize.sh"
    execute_command   = "chmod +x {{ .Path }}; {{ .Vars }} sudo -E bash {{ .Path }}"
    expect_disconnect = true
  }

  post-processor "manifest" {
    output     = "${path.root}/manifest.json"
    strip_path = true
    custom_data = {
      lynis_min_score = "${var.lynis_min_score}"
      environment     = var.env
    }
  }
}
