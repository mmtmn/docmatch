#!/usr/bin/env bash
# Creates the Terraform state bucket docmatch-tfstate-<account_id> (versioned, SSE-S3, public
# access blocked, TLS only, lifecycle for old versions) and writes backend.hcl for every
# environment. Idempotent: safe to re-run. Nothing else in the account is touched.
#
# Uso: infra/scripts/bootstrap-state.sh [--dry-run] [--envs "dev prod"]
# Variáveis: ACCOUNT_ID (opcional, descoberta automática), AWS_REGION (obrigatória).
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib.sh
source "$here/lib.sh"

dry_run=0
envs="dev prod"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --dry-run) dry_run=1 ;;
    --envs) envs="$2"; shift ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) dm_die "argumento desconhecido: $1" ;;
  esac
  shift
done

dm_require aws
region="$(dm_region)"
account_id="$(dm_account_id)"
bucket="docmatch-tfstate-${account_id}"
dm_log "conta ${account_id}, região ${region}, bucket ${bucket}"

run() {
  if [[ $dry_run -eq 1 ]]; then
    dm_log "[dry-run] $*"
  else
    "$@"
  fi
}

if aws s3api head-bucket --bucket "$bucket" 2>/dev/null; then
  dm_log "bucket já existe; reaplicando a configuração"
else
  dm_log "criando o bucket"
  if [[ "$region" == "us-east-1" ]]; then
    run aws s3api create-bucket --bucket "$bucket" --region "$region" >/dev/null
  else
    run aws s3api create-bucket --bucket "$bucket" --region "$region" \
      --create-bucket-configuration "LocationConstraint=${region}" >/dev/null
  fi
fi

run aws s3api put-public-access-block --bucket "$bucket" --public-access-block-configuration \
  'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'

run aws s3api put-bucket-ownership-controls --bucket "$bucket" \
  --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'

run aws s3api put-bucket-versioning --bucket "$bucket" --versioning-configuration Status=Enabled

run aws s3api put-bucket-encryption --bucket "$bucket" --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'

run aws s3api put-bucket-lifecycle-configuration --bucket "$bucket" --lifecycle-configuration '{
  "Rules": [
    {
      "ID": "expire-old-state-versions",
      "Status": "Enabled",
      "Filter": {"Prefix": ""},
      "NoncurrentVersionExpiration": {"NoncurrentDays": 90, "NewerNoncurrentVersions": 10},
      "AbortIncompleteMultipartUpload": {"DaysAfterInitiation": 7},
      "Expiration": {"ExpiredObjectDeleteMarker": true}
    }
  ]
}'

policy="$(cat <<EOF
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DenyInsecureTransport",
      "Effect": "Deny",
      "Principal": "*",
      "Action": "s3:*",
      "Resource": ["arn:aws:s3:::${bucket}", "arn:aws:s3:::${bucket}/*"],
      "Condition": {"Bool": {"aws:SecureTransport": "false"}}
    }
  ]
}
EOF
)"
run aws s3api put-bucket-policy --bucket "$bucket" --policy "$policy"

run aws s3api put-bucket-tagging --bucket "$bucket" --tagging \
  'TagSet=[{Key=Project,Value=docmatch},{Key=ManagedBy,Value=bootstrap-state.sh}]'

root="$(dm_repo_root)"
for env in $envs; do
  dir="$root/infra/terraform/envs/$env"
  [[ -d "$dir" ]] || dm_die "ambiente desconhecido: $env"
  file="$dir/backend.hcl"
  if [[ $dry_run -eq 1 ]]; then
    dm_log "[dry-run] escreveria $file"
    continue
  fi
  cat > "$file" <<EOF
# Gerado por infra/scripts/bootstrap-state.sh em $(date -u +%FT%TZ). Não commitar.
bucket       = "${bucket}"
key          = "${env}/terraform.tfstate"
region       = "${region}"
encrypt      = true
use_lockfile = true
EOF
  dm_log "escrito $file"
done

dm_log "pronto. Próximo passo: make -C infra init-dev (ou init-prod)"
