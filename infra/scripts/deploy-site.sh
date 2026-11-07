#!/usr/bin/env bash
# Builds apps/web for the environment and publishes it to the static site bucket, then invalidates
# the CloudFront distribution. Site origin (also the API's: CloudFront routes /v1/* to the
# instance), S3/R2 upload origins (CSP connect-src), security contact, bucket and distribution
# all come from the Terraform outputs of the environment; nothing is typed. --dist publishes an
# existing build instead. Hashed assets under _astro/ are immutable (one year); everything else
# revalidates in 5 minutes.
#
# Uso: infra/scripts/deploy-site.sh [--env prod] [--dist apps/web/dist] [--no-invalidate] [--dry-run]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib.sh
source "$here/lib.sh"

env="prod"
dist=""
invalidate=1
dry_run=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) env="$2"; shift ;;
    --dist) dist="$2"; shift ;;
    --no-invalidate) invalidate=0 ;;
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) dm_die "argumento desconhecido: $1" ;;
  esac
  shift
done

dm_require aws terraform make node
root="$(dm_repo_root)"
env_dir="$root/infra/terraform/envs/$env"
[[ -d "$env_dir" ]] || dm_die "ambiente desconhecido: $env"
export AWS_PAGER=""

if [[ -z "$dist" ]]; then
  site_url="$(terraform -chdir="$env_dir" output -raw site_url)"
  upload_origins="$(terraform -chdir="$env_dir" output -raw upload_origins)"
  security_contact="$(terraform -chdir="$env_dir" output -raw security_contact_email)"
  [[ -n "$site_url" && -n "$upload_origins" ]] || dm_die "outputs do Terraform vazios; o ambiente já foi aplicado?"
  dm_log "build do site para $site_url"
  DM_PUBLIC_BASE_URL="$site_url" PUBLIC_DM_API_BASE_URL="$site_url" DM_UPLOAD_ORIGINS="$upload_origins" \
    PUBLIC_DM_SECURITY_CONTACT="$security_contact" PUBLIC_DM_FEATURE_RESULTS=false \
    make -s -C "$root/apps/web" build
  dist="$root/apps/web/dist"
fi
[[ -f "$dist/index.html" ]] || dm_die "build do site não encontrado em $dist"
for guide in usar desenvolver; do
  [[ -s "$dist/docs/$guide/index.html" ]] || dm_die "build incompleto: falta o guia $guide; reconstrua site e livros juntos"
done
# CloudFront supplies frame-ancestors; each HTML page must supply the strict hashed policy.
node "$root/apps/web/tools/check-csp.mjs" "$dist"

bucket="$(terraform -chdir="$env_dir" output -raw site_bucket_name)"
distribution_id="$(terraform -chdir="$env_dir" output -raw cloudfront_distribution_id)"
[[ -n "$bucket" && -n "$distribution_id" ]] || dm_die "outputs do Terraform vazios; o ambiente já foi aplicado?"
dm_log "ambiente $env: bucket $bucket, distribuição $distribution_id"

sync=(aws s3 sync)
[[ $dry_run -eq 1 ]] && sync+=(--dryrun)

if [[ -d "$dist/_astro" ]]; then
  "${sync[@]}" "$dist/_astro" "s3://$bucket/_astro" \
    --cache-control "public, max-age=31536000, immutable" --size-only --no-progress | cat
fi
# Installer and incident status pages have separate publishers; never delete them here.
"${sync[@]}" "$dist" "s3://$bucket" --exclude "_astro/*" --exclude "install/*" --exclude "status/*" --delete --exact-timestamps \
  --cache-control "public, max-age=300, must-revalidate" --no-progress | cat

if [[ $invalidate -eq 1 && $dry_run -eq 0 ]]; then
  invalidation_id="$(aws cloudfront create-invalidation --distribution-id "$distribution_id" \
    --paths "/*" --query 'Invalidation.Id' --output text | cat)"
  dm_log "invalidação $invalidation_id criada; aguardando"
  aws cloudfront wait invalidation-completed --distribution-id "$distribution_id" --id "$invalidation_id"
fi
dm_log "site publicado"
