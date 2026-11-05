#!/usr/bin/env bash
# Publishes the installer: cross-compiles docmatch for linux, darwin and windows on amd64 and
# arm64 (CGO off), writes SHA256SUMS.txt, renders install.sh and install.ps1 with the download base
# and copies everything to the site bucket under /install/ (latest) and /install/v<version>/
# (pinned). Like infra/scripts/deploy-site.sh, the site origin, bucket and distribution come from
# the Terraform outputs of the environment; nothing is typed.
#
# The base is also stamped into the binary (main.installBase), so `docmatch update` downloads from
# where install.sh came from. The release checks that the binary of this machine answers
# `docmatch <version>` to --version and version, and renders the package-manager files (a Homebrew
# formula and a winget manifest, from the templates in packaging/templates and SHA256SUMS.txt) into
# <out>/packaging; a real release also copies them to tools/installer/packaging/ to be committed.
#
# --dry-run builds and renders into --out and prints what would be published, without calling
# Terraform or AWS (the base defaults to a .invalid placeholder unless --base-url is given). It
# never touches tracked files: the default --out is tools/installer/dist/<version> (ignored).
#
# Uso: tools/installer/release.sh [--env prod] [--version 0.1.0] [--base-url URL] [--out DIR] [--dry-run]
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel 2>/dev/null || (cd "$here/../.." && pwd))"
# shellcheck source=lib.sh
. "$here/lib.sh"

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
    -h|--help) sed -n '2,18p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
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

# Image registry (ghcr.io/<owner>), clone URL and owner from the origin remote (GitHub).
docmatch_repo_info "$root"

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

ldflags="-s -w -X main.version=${version} -X main.registry=${registry} -X main.repoURL=${repo_url} -X main.installBase=${base}"
log "docmatch $version (registro ${registry:-local}, código ${repo_url:-sem clone})"
host_target="$(cd "$here" && go env GOOS)/$(cd "$here" && go env GOARCH)"
host_binary=""
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  goos="${target%/*}" goarch="${target#*/}"
  ext=".bin"
  [[ "$goos" == windows ]] && ext=".exe"
  name="docmatch-${goos}-${goarch}${ext}"
  (cd "$here" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -ldflags "$ldflags" -o "$out/$name" .)
  log "  $name ($(du -h "$out/$name" | cut -f1))"
  [[ "$target" == "$host_target" ]] && host_binary="$out/$name"
done
(cd "$out" && "${sha[@]}" docmatch-* > SHA256SUMS.txt)
sed "s|__DOCMATCH_INSTALL_BASE__|${base}|g" "$here/install.sh" > "$out/install.sh"
sed "s|__DOCMATCH_INSTALL_BASE__|${base}|g" "$here/install.ps1" > "$out/install.ps1"
chmod 0755 "$out/install.sh"
printf '%s\n' "$version" > "$out/VERSION.txt"
log "SHA256SUMS.txt, install.sh e install.ps1 em $out (base $base)"

# The version the binary reports is the one VERSION.txt announces and `docmatch update` compares.
if [[ -n "$host_binary" ]]; then
  for arg in --version version; do
    got="$("$host_binary" "$arg" 2>&1)" || die "$(basename "$host_binary") $arg falhou: $got"
    [[ "$got" == "docmatch $version" ]] || die "$(basename "$host_binary") $arg respondeu \"$got\" (esperado \"docmatch $version\")"
  done
  log "$(basename "$host_binary") responde docmatch $version (--version e version)"
else
  log "aviso: nenhum binário roda nesta máquina ($host_target); versão não conferida"
fi

# Package managers: a Homebrew formula and a winget manifest pointing at the pinned copies.
sum_of() { # sum_of <file name>: its SHA-256 from SHA256SUMS.txt
  local s
  s="$(awk -v n="$1" '$2 == n || $2 == "*" n { print $1 }' "$out/SHA256SUMS.txt")"
  [[ -n "$s" ]] || die "$1 não está no SHA256SUMS.txt"
  printf '%s' "$s"
}
pinned="$base/v$version"
homepage="${base%/install}"
publisher="${repo_owner:-docmatch}"
package_id="${publisher}.docmatch"
render() { # render <template> <destination>
  mkdir -p "$(dirname "$2")"
  sed -e "s|__VERSION__|${version}|g" -e "s|__BASE__|${pinned}|g" -e "s|__HOMEPAGE__|${homepage}|g" \
    -e "s|__PACKAGE_ID__|${package_id}|g" -e "s|__PUBLISHER__|${publisher}|g" -e "s|__DATE__|$(date -u +%F)|g" \
    -e "s|__SHA_DARWIN_ARM64__|$(sum_of docmatch-darwin-arm64.bin)|g" \
    -e "s|__SHA_DARWIN_AMD64__|$(sum_of docmatch-darwin-amd64.bin)|g" \
    -e "s|__SHA_LINUX_ARM64__|$(sum_of docmatch-linux-arm64.bin)|g" \
    -e "s|__SHA_LINUX_AMD64__|$(sum_of docmatch-linux-amd64.bin)|g" \
    -e "s|__SHA_WINDOWS_AMD64__|$(sum_of docmatch-windows-amd64.exe | tr '[:lower:]' '[:upper:]')|g" \
    -e "s|__SHA_WINDOWS_ARM64__|$(sum_of docmatch-windows-arm64.exe | tr '[:lower:]' '[:upper:]')|g" \
    "$1" > "$2"
  ! grep -q '__[A-Z0-9_]*__' "$2" || die "$2 ficou com marcadores sem valor"
}
templates="$here/packaging/templates"
packaging="$out/packaging"
# winget-pkgs layout: manifests/<first letter>/<Publisher>/<Name>/<version>/<id>.*.yaml
winget_dir="winget/manifests/$(tr '[:upper:]' '[:lower:]' <<<"${publisher:0:1}")/$publisher/docmatch/$version"
render "$templates/docmatch.rb" "$packaging/homebrew/docmatch.rb"
render "$templates/winget.version.yaml" "$packaging/$winget_dir/$package_id.yaml"
render "$templates/winget.installer.yaml" "$packaging/$winget_dir/$package_id.installer.yaml"
render "$templates/winget.locale.pt-BR.yaml" "$packaging/$winget_dir/$package_id.locale.pt-BR.yaml"
log "Homebrew (homebrew/docmatch.rb) e winget ($winget_dir) em $packaging"

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

# Pinned copies never change; the latest ones revalidate in 5 minutes. packaging/ is not published
# (it goes to the tap and to winget-pkgs).
for f in "$out"/*; do
  [[ -f "$f" ]] || continue
  file="$(basename "$f")"
  run aws s3 cp "$f" "s3://$bucket/install/v$version/$file" --content-type "$(content_type "$file")" \
    --cache-control "public, max-age=31536000, immutable" --no-progress
  run aws s3 cp "$f" "s3://$bucket/install/$file" --content-type "$(content_type "$file")" \
    --cache-control "public, max-age=300, must-revalidate" --no-progress
done
run aws cloudfront create-invalidation --distribution-id "$distribution_id" --paths "/install/*"

# A real release keeps the package-manager files of the latest version in the repository.
if [[ $dry_run -eq 0 ]]; then
  rm -rf "$here/packaging/homebrew" "$here/packaging/winget"
  cp -R "$packaging/homebrew" "$packaging/winget" "$here/packaging/"
  log "Homebrew e winget copiados para tools/installer/packaging/ (faça o commit)"
fi

log "pronto. Linux e macOS: curl -fsSL $base/install.sh | sh"
log "Windows (PowerShell): irm $base/install.ps1 | iex"
