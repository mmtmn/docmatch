# Exemplo usado por `make validate`. Para um build real, copie para build.auto.pkrvars.hcl (nunca
# commitado) com os outputs do ambiente dev:
#   terraform -chdir=infra/terraform/envs/dev output -raw app_subnet_id
#   terraform -chdir=infra/terraform/envs/dev output -raw app_security_group_id
#   terraform -chdir=infra/terraform/envs/dev output -raw packer_instance_profile_name
region               = "us-east-1"
env                  = "dev"
subnet_id            = "subnet-0123456789abcdef0"
security_group_id    = "sg-0123456789abcdef0"
iam_instance_profile = "docmatch-dev-packer"
lynis_min_score      = 90
