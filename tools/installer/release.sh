#!/usr/bin/env bash
# Publishes the installer: cross-compiles docmatch for linux, darwin and windows on amd64 and
# arm64 (CGO off), writes SHA256SUMS.txt, renders install.sh and install.ps1 with the download base
# and copies everything to the site bucket under /install/ (latest) and /install/v<version>/
# (pinned). Like infra/scripts/deploy-site.sh, the site origin, bucket and distribution come from
# the Terraform outputs of the environment; nothing is typed.
#
# --dry-run builds and renders into --out and prints what would be published, without calling
# Terraform or AWS (the base defaults to a .invalid placeholder unless --base-url is given).
#
# Uso: tools/installer/release.sh [--env prod] [--version 0.1.0] [--base-url URL] [--out DIR] [--dry-run]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel 2>/dev/null || (cd "$here/../.." && pwd))"

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'erro: %s\n' "$*" >&2; exit 1; }

env="prod"
version="${DOCMATCH_RELEASE_VERSION:-}"
base="${DOCMATCH_INSTALL_BASE:-}"
out=""
dry_run=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --env) env="$2"; shift ;;
    --version) version="$2"; shift ;;
    --base-url) base="$2"; shift ;;
    --out) out="$2"; shift ;;
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "argumento desconhecido: $1" ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || die "comando necessário: go"
if command -v sha256sum >/dev/null 2>&1; then sha=(sha256sum); else sha=(shasum -a 256); fi

sha_short="$(git -C "$root" rev-parse --short=12 HEAD 2>/dev/null || echo desconhecido)"
version="${version#v}"
[[ -n "$version" ]] || version="0.0.0-${sha_short}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "versão inválida: $version (use X.Y.Z)"

# Image registry and clone URL from the origin remote (GitHub): ghcr.io/<owner>.
remote="$(git -C "$root" remote get-url origin 2>/dev/null || true)"
remote="${remote%.git}"
host="" owner="" repo=""
if [[ "$remote" =~ ^[a-z]+://([^/@]+@)?([^/:]+)(:[0-9]+)?/([^/]+)/([^/]+)$ ]]; then
  host="${BASH_REMATCH[2]}" owner="${BASH_REMATCH[4]}" repo="${BASH_REMATCH[5]}"
elif [[ "$remote" =~ ^[^@]+@([^:]+):([^/]+)/([^/]+)$ ]]; then
  host="${BASH_REMATCH[1]}" owner="${BASH_REMATCH[2]}" repo="${BASH_REMATCH[3]}"
fi
registry="" repo_url=""
if [[ "$host" == "github.com" ]]; then
  registry="ghcr.io/$(tr '[:upper:]' '[:lower:]' <<<"$owner")"
  repo_url="https://${host}/${owner}/${repo}"
fi

env_dir="$root/infra/terraform/envs/$env"
if [[ -z "$base" ]]; then
  if [[ $dry_run -eq 1 ]]; then
    base="https://site.invalid/install"
  else
    command -v terraform >/dev/null 2>&1 || die "comando necessário: terraform"
    [[ -d "$env_dir" ]] || die "ambiente desconhecido: $env"
    site_url="$(terraform -chdir="$env_dir" output -raw site_url)"
    [[ -n "$site_url" ]] || die "output site_url vazio; o ambiente já foi aplicado?"
    base="${site_url%/}/install"
  fi
fi
base="${base%/}"
# Rendered with sed below: only plain URL characters.
[[ "$base" =~ ^https?://[A-Za-z0-9._~:/-]+$ ]] || die "base inválida: $base"

out="${out:-$here/dist/$version}"
rm -rf "$out"
mkdir -p "$out"

ldflags="-s -w -X main.version=${version} -X main.registry=${registry} -X main.repoURL=${repo_url}"
log "docmatch $version (registro ${registry:-local}, código ${repo_url:-sem clone})"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  goos="${target%/*}" goarch="${target#*/}"
  ext=".bin"
  [[ "$goos" == windows ]] && ext=".exe"
  name="docmatch-${goos}-${goarch}${ext}"
  (cd "$here" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$out/$name" .)
  log "  $name ($(du -h "$out/$name" | cut -f1))"
done
(cd "$out" && "${sha[@]}" docmatch-* > SHA256SUMS.txt)
sed "s|__DOCMATCH_INSTALL_BASE__|${base}|g" "$here/install.sh" > "$out/install.sh"
sed "s|__DOCMATCH_INSTALL_BASE__|${base}|g" "$here/install.ps1" > "$out/install.ps1"
chmod 0755 "$out/install.sh"
printf '%s\n' "$version" > "$out/VERSION.txt"
log "SHA256SUMS.txt, install.sh e install.ps1 em $out (base $base)"

content_type() {
  case "$1" in
    *.sh) echo "text/x-shellscript; charset=utf-8" ;;
    *.ps1|*.txt) echo "text/plain; charset=utf-8" ;;
    *) echo "application/octet-stream" ;;
  esac
}

if [[ $dry_run -eq 1 ]]; then
  bucket="<site_bucket_name do Terraform ($env)>"
  distribution_id="<cloudfront_distribution_id do Terraform ($env)>"
else
  command -v aws >/dev/null 2>&1 || die "comando necessário: aws"
  command -v terraform >/dev/null 2>&1 || die "comando necessário: terraform"
  export AWS_PAGER=""
  bucket="$(terraform -chdir="$env_dir" output -raw site_bucket_name)"
  distribution_id="$(terraform -chdir="$env_dir" output -raw cloudfront_distribution_id)"
  [[ -n "$bucket" && -n "$distribution_id" ]] || die "outputs do Terraform vazios; o ambiente já foi aplicado?"
fi

run() {
  if [[ $dry_run -eq 1 ]]; then
    printf '(dry-run)'
    printf ' %q' "$@"
    printf '\n'
  else
    # The aws CLI misbehaves when its stdout is not a pipe: always through cat.
    "$@" | cat
  fi
}

# Pinned copies never change; the latest ones revalidate in 5 minutes.
for f in "$out"/*; do
  file="$(basename "$f")"
  run aws s3 cp "$f" "s3://$bucket/install/v$version/$file" --content-type "$(content_type "$file")" \
    --cache-control "public, max-age=31536000, immutable" --no-progress
  run aws s3 cp "$f" "s3://$bucket/install/$file" --content-type "$(content_type "$file")" \
    --cache-control "public, max-age=300, must-revalidate" --no-progress
done
run aws cloudfront create-invalidation --distribution-id "$distribution_id" --paths "/install/*"

log "pronto. Linux e macOS: curl -fsSL $base/install.sh | sh"
log "Windows (PowerShell): irm $base/install.ps1 | iex"
