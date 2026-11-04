#!/usr/bin/env bash
# Publishes apps/web/dist to the static site bucket and invalidates the CloudFront distribution.
# Bucket and distribution come from the Terraform outputs of the environment; nothing is typed.
# Hashed assets under _astro/ are immutable (one year); everything else revalidates in 5 minutes.
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

dm_require aws terraform
root="$(dm_repo_root)"
dist="${dist:-$root/apps/web/dist}"
env_dir="$root/infra/terraform/envs/$env"
[[ -d "$env_dir" ]] || dm_die "ambiente desconhecido: $env"
[[ -f "$dist/index.html" ]] || dm_die "build do site não encontrado em $dist (rode make -C apps/web build)"

bucket="$(terraform -chdir="$env_dir" output -raw site_bucket_name)"
distribution_id="$(terraform -chdir="$env_dir" output -raw cloudfront_distribution_id)"
[[ -n "$bucket" && -n "$distribution_id" ]] || dm_die "outputs do Terraform vazios; o ambiente já foi aplicado?"
dm_log "ambiente $env: bucket $bucket, distribuição $distribution_id"

extra=()
[[ $dry_run -eq 1 ]] && extra+=(--dryrun)

if [[ -d "$dist/_astro" ]]; then
  aws s3 sync "$dist/_astro" "s3://$bucket/_astro" "${extra[@]}" \
    --cache-control "public, max-age=31536000, immutable" --size-only --no-progress
fi
aws s3 sync "$dist" "s3://$bucket" "${extra[@]}" --exclude "_astro/*" --delete --exact-timestamps \
  --cache-control "public, max-age=300, must-revalidate" --no-progress

if [[ $invalidate -eq 1 && $dry_run -eq 0 ]]; then
  invalidation_id="$(aws cloudfront create-invalidation --distribution-id "$distribution_id" \
    --paths "/*" --query 'Invalidation.Id' --output text)"
  dm_log "invalidação $invalidation_id criada; aguardando"
  aws cloudfront wait invalidation-completed --distribution-id "$distribution_id" --id "$invalidation_id"
fi
dm_log "site publicado"
