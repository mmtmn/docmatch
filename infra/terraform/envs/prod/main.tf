# docmatch — production environment. Composes the modules under ../../modules. Every account,
# domain or e-mail specific value is a variable (terraform.tfvars, never committed).

locals {
  env             = "prod"
  name            = "docmatch-${local.env}"
  origin_hostname = "origin.${var.domain}"
  cloudtrail_name = "${local.name}-trail"
  cloudtrail_arn  = "arn:aws:cloudtrail:${var.region}:${var.account_id}:trail/${local.cloudtrail_name}"
  tfstate_bucket  = "docmatch-tfstate-${var.account_id}"

  # Environment variables declared as secrets (SecureString, values set by the owner, never here).
  secret_names = [
    "DM_ORIGIN_SECRET",
    "DATABASE_URL",
    "DM_SESSION_SECRET",
    "DM_QUOTE_HMAC_KEY",
    "DM_APIKEY_PEPPER",
    "DM_WEBHOOK_SIGNING_KEY",
    "OPENROUTER_API_KEY",
    "R2_ACCESS_KEY_ID",
    "R2_SECRET_ACCESS_KEY",
    "GOOGLE_OAUTH_CLIENT_ID",
    "GOOGLE_OAUTH_CLIENT_SECRET",
    "APPLE_TEAM_ID",
    "APPLE_CLIENT_ID",
    "APPLE_KEY_ID",
    "APPLE_PRIVATE_KEY",
    "STRIPE_SECRET_KEY",
    "STRIPE_WEBHOOK_SECRET",
    "STRIPE_PUBLISHABLE_KEY",
    "RESEND_API_KEY",
  ]
}

module "storage" {
  source = "../../modules/storage"

  name            = local.name
  account_id      = var.account_id
  region          = var.region
  force_destroy   = false
  cloudtrail_name = local.cloudtrail_name
}

module "network" {
  source = "../../modules/network"

  name                  = local.name
  account_id            = var.account_id
  s3_bucket_name_prefix = "${local.name}-"
  enable_flow_logs      = var.enable_flow_logs
  flow_logs_bucket_arn  = module.storage.logs_bucket_arn
}

module "iam_guardrails" {
  source = "../../modules/iam-guardrails"

  name            = local.name
  allowed_regions = [var.region]
  cloudtrail_arn  = local.cloudtrail_arn
  protected_bucket_arns = [
    module.storage.bucket_arns["backups"],
    "arn:aws:s3:::${local.tfstate_bucket}",
  ]
  protected_kms_key_arns = compact([module.edge.dnssec_kms_key_arn])
}

module "edge" {
  source = "../../modules/edge"

  name                             = local.name
  env                              = local.env
  account_id                       = var.account_id
  domain                           = var.domain
  origin_hostname                  = local.origin_hostname
  site_bucket_name                 = module.storage.bucket_names["site"]
  site_bucket_arn                  = module.storage.bucket_arns["site"]
  site_bucket_regional_domain_name = module.storage.bucket_regional_domain_names["site"]
  logs_bucket_arn                  = module.storage.logs_bucket_arn
  dnssec_enabled                   = var.dnssec_enabled
  wait_for_certificate_validation  = var.wait_for_certificate_validation
  dmarc_rua_email                  = var.dmarc_rua_email
  extra_records                    = var.extra_dns_records
}

module "sandbox" {
  source = "../../modules/sandbox"

  name                   = local.name
  env                    = local.env
  region                 = var.region
  account_id             = var.account_id
  subnet_id              = module.network.sandbox_subnet_id
  security_group_id      = module.network.sandbox_security_group_id
  uploads_bucket_name    = module.storage.bucket_names["uploads"]
  uploads_bucket_arn     = module.storage.bucket_arns["uploads"]
  signatures_bucket_name = module.storage.bucket_names["signatures"]
  signatures_bucket_arn  = module.storage.bucket_arns["signatures"]
  image_tag              = var.lambda_image_tag
  guardrail_policy_arns  = module.iam_guardrails.role_policy_arns
}

module "secrets" {
  source = "../../modules/secrets"

  name         = local.name
  env          = local.env
  secret_names = local.secret_names

  config_values = {
    DM_ENV                    = local.env
    DM_DOMAIN                 = var.domain
    DM_PUBLIC_BASE_URL        = "https://${var.domain}"
    DM_API_BASE_URL           = "https://${var.domain}/api"
    DM_ORIGIN_SECRET_HEADER   = module.edge.origin_secret_header_name
    AWS_REGION                = var.region
    DM_S3_UPLOADS_BUCKET      = module.storage.bucket_names["uploads"]
    DM_S3_BACKUPS_BUCKET      = module.storage.bucket_names["backups"]
    DM_SCAN_VERDICTS_QUEUE    = module.sandbox.verdict_queue_url
    DM_SANDBOX_MEDIARUNNER    = module.sandbox.function_names["mediarunner"]
    DM_SANDBOX_TOOLRUNNER     = module.sandbox.function_names["toolrunner"]
    DM_SANDBOX_FETCHER        = module.sandbox.function_names["fetcher"]
    DM_R2_MEDIA_BUCKET        = var.cloudflare_account_id != "" ? module.r2[0].media_bucket_name : "${local.name}-media"
    DM_LLM_MODE               = var.llm_mode
    DM_EMAIL_FROM             = var.email_from
    DM_SECURITY_CONTACT       = var.security_contact_email
    DM_ALERT_EMAIL            = var.alert_email
    DM_EGRESS_ALLOWLIST       = join(" ", var.egress_allowlist)
    DM_BACKUPS_PGBACKREST_KEY = "pgbackrest"
  }
}

module "compute" {
  source = "../../modules/compute"

  name                    = local.name
  env                     = local.env
  region                  = var.region
  account_id              = var.account_id
  ami_id                  = var.ami_id
  subnet_id               = module.network.app_subnet_id
  availability_zone       = module.network.availability_zone
  security_group_id       = module.network.app_security_group_id
  ssm_path_prefix         = module.secrets.path_prefix
  uploads_bucket_arn      = module.storage.bucket_arns["uploads"]
  backups_bucket_arn      = module.storage.bucket_arns["backups"]
  verdict_queue_arn       = module.sandbox.verdict_queue_arn
  invokable_function_arns = module.sandbox.invokable_function_arns
  hosted_zone_id          = module.edge.hosted_zone_id
  origin_hostname         = local.origin_hostname
  log_group_arn           = module.observability.app_log_group_arn
  guardrail_policy_arns   = module.iam_guardrails.role_policy_arns
  worker_desired_capacity = var.worker_desired_capacity
  create_packer_profile   = false
}

module "observability" {
  source = "../../modules/observability"

  name                       = local.name
  env                        = local.env
  account_id                 = var.account_id
  logs_bucket_name           = module.storage.logs_bucket_name
  cloudtrail_name            = local.cloudtrail_name
  alert_email                = var.alert_email
  mrr_usd                    = var.mrr_usd
  app_autoscaling_group_name = module.compute.app_autoscaling_group_name
  lambda_function_names      = module.sandbox.enabled ? values(module.sandbox.function_names) : []
  dead_letter_queue_names    = [module.sandbox.verdict_dlq_name, module.sandbox.lambda_dlq_name]
  cloudfront_distribution_id = module.edge.distribution_id
}

module "r2" {
  source = "../../modules/r2"
  count  = var.cloudflare_account_id != "" ? 1 : 0

  name                  = local.name
  cloudflare_account_id = var.cloudflare_account_id
  domain                = var.domain
}
