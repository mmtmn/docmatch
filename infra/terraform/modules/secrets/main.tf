# Secrets: SSM Parameter Store paths under /docmatch/<env>/. Secret values never pass through
# Terraform. The parameters are declared with a placeholder through the write-only argument
# `value_wo`, which Terraform neither stores in state nor reads back on refresh. A plain `value`
# with lifecycle ignore_changes would still copy the real secret into the state file at every
# refresh, so write-only is the stricter option here. Bumping value_wo_version would overwrite a
# parameter with the placeholder again; it is intentionally pinned to 1.

locals {
  path_prefix = "/docmatch/${var.env}"
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(var.secret_names)

  name             = "${local.path_prefix}/${each.value}"
  description      = "docmatch ${var.env}: ${each.value} (secret; set with the AWS CLI, never through Terraform)"
  type             = "SecureString"
  tier             = "Standard"
  value_wo         = var.placeholder
  value_wo_version = 1

  tags = merge(var.tags, { Name = "${var.name}-${lower(replace(each.value, "_", "-"))}", "docmatch:secret" = "true" })
}

# Optional secrets (a feature that stays off without them, e.g. DM_BURST_POOL_SECRET) start as
# the empty sentinel: render-env renders it as "" and never blocks the boot, so creating one can
# never take production down before the owner fills it.
resource "aws_ssm_parameter" "optional_secret" {
  for_each = toset(var.optional_secret_names)

  name             = "${local.path_prefix}/${each.value}"
  description      = "docmatch ${var.env}: ${each.value} (optional secret; empty keeps its feature off; set with the AWS CLI)"
  type             = "SecureString"
  tier             = "Standard"
  value_wo         = var.empty_placeholder
  value_wo_version = 1

  tags = merge(var.tags, { Name = "${var.name}-${lower(replace(each.value, "_", "-"))}", "docmatch:secret" = "true" })
}

resource "aws_ssm_parameter" "config" {
  for_each = var.config_values

  name        = "${local.path_prefix}/${each.key}"
  description = "docmatch ${var.env}: ${each.key} (configuration managed by Terraform)"
  type        = "String"
  tier        = "Standard"
  value       = each.value

  tags = merge(var.tags, { Name = "${var.name}-${lower(replace(each.key, "_", "-"))}", "docmatch:secret" = "false" })
}
