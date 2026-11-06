#!/usr/bin/env bash
# Publish the same verified npm tarball via the site's existing artifact bucket.
# Registry publication is separate: npm publish <tarball> --access public.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
version="${1:?uso: bash tools/terminal/release.sh VERSION [ENV]}"
environment="${2:-prod}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'versão inválida' >&2; exit 1; }
[[ "$environment" =~ ^[a-z0-9-]+$ ]] || { echo 'ambiente inválido' >&2; exit 1; }
infra="$root/infra/terraform/envs/$environment"
base="$(terraform -chdir="$infra" output -raw site_url)/install"
bucket="$(terraform -chdir="$infra" output -raw site_bucket_name)"
distribution="$(terraform -chdir="$infra" output -raw cloudfront_distribution_id)"
output="$root/output/terminal-$version"
mkdir -p "$output"

# A version is immutable, including the binaries whose hashes npm ships. Fail closed on
# credentials/network errors too; only an empty successful listing permits a new release.
existing="$(aws s3api list-objects-v2 --bucket "$bucket" --prefix "install/v$version/" --max-items 1 --query 'Contents[].Key' --output text)"
[[ -z "$existing" || "$existing" == None ]] || { echo 'versão já publicada; escolha uma nova versão' >&2; exit 1; }
bash "$root/tools/installer/release.sh" --env "$environment" --version "$version" --base-url "$base" --out "$output/binaries"
node "$root/tools/terminal/pack.mjs" "$output/binaries" "$base" "$output/package"
tarball="$output/package/docmatch-$version.tgz"
aws s3 cp "$tarball" "s3://$bucket/install/v$version/docmatch.tgz" --content-type application/gzip --cache-control 'public, max-age=31536000, immutable' --no-progress | cat
aws s3 cp "$tarball" "s3://$bucket/install/docmatch.tgz" --content-type application/gzip --cache-control 'public, max-age=300, must-revalidate' --no-progress | cat
aws cloudfront create-invalidation --distribution-id "$distribution" --paths /install/docmatch.tgz | cat
printf 'npm install -g %s/docmatch.tgz\n' "$base"
