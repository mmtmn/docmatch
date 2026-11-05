#!/usr/bin/env bash
# Publishes the three docmatch images of the Experimentar mode (deploy/local: docmatch-api,
# docmatch-harness, docmatch-web) to GHCR for linux/amd64 and linux/arm64, so the installer pulls
# them instead of compiling. The registry is ghcr.io/<owner of the origin remote>, the one the
# installer derives by itself (internal/install.RegistryFromRemote and release.sh); nothing is typed.
#
# Tags: <revision> (12 characters of the commit), <version> with --version (a release binary pulls
# its own version: use the same --version as release.sh) and latest (unless --no-latest). Each image
# carries org.opencontainers.image.source (the repository: GHCR links the package to it), .revision
# and .version, as labels and as annotations of the multi-architecture index.
#
# Needs docker buildx with a docker-container builder (created as docmatch-multiarch when missing)
# and `docker login ghcr.io` with a token that has write:packages. The architecture this machine is
# not runs under QEMU: install the emulators once with
#   docker run --privileged --rm tonistiigi/binfmt --install arm64   (or amd64 on an arm64 machine)
# The Go and Rust builds of the foreign architecture are slow under emulation; BuildKit's cache
# mounts keep the next builds incremental.
#
# --dry-run prints every command and runs none (no Docker needed); a real run refuses a checkout
# with local changes, since the images are tagged with the commit.
#
# While the repository is private the packages are private too: anonymous pulls are denied and the
# installer compiles the images from the code (or `docker login ghcr.io` with a read:packages token
# lets it pull). Once the repository is public, make each package public (GitHub > Packages >
# docmatch-<svc> > Package settings > Change visibility) and `docmatch` (Experimentar) downloads the
# images, on Intel and on Apple silicon or arm64 Linux, without compiling.
#
# Uso: tools/installer/publish-images.sh [--version 0.1.0] [--registry ghcr.io/dono]
#        [--platforms linux/amd64,linux/arm64] [--only api,harness,web] [--no-latest] [--dry-run]
set -euo pipefail
# Inside a git hook (make ci in pre-push) git exports GIT_DIR: with it, rev-parse --show-toplevel
# answers the current folder instead of the repository root.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_COMMON_DIR GIT_PREFIX GIT_OBJECT_DIRECTORY

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(git -C "$here" rev-parse --show-toplevel 2>/dev/null || (cd "$here/../.." && pwd))"
# shellcheck source=lib.sh
. "$here/lib.sh"

log() { printf '==> %s\n' "$*" >&2; }
die() { printf 'erro: %s\n' "$*" >&2; exit 1; }

version="${DOCMATCH_RELEASE_VERSION:-}"
registry_flag="${DOCMATCH_REGISTRY:-}"
platforms="linux/amd64,linux/arm64"
only="api,harness,web"
latest=1
dry_run=0
builder="docmatch-multiarch"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --version) version="$2"; shift ;;
    --registry) registry_flag="$2"; shift ;;
    --platforms) platforms="$2"; shift ;;
    --only) only="$2"; shift ;;
    --no-latest) latest=0 ;;
    --dry-run) dry_run=1 ;;
    -h|--help) sed -n '2,29p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) die "argumento desconhecido: $1" ;;
  esac
  shift
done

version="${version#v}"
if [[ -n "$version" ]]; then
  [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "versão inválida: $version (use X.Y.Z)"
fi
[[ "$platforms" =~ ^linux/[a-z0-9]+(,linux/[a-z0-9]+)*$ ]] || die "plataformas inválidas: $platforms"

docmatch_repo_info "$root"
registry="${registry_flag:-$registry}"
registry="${registry%/}"
if [[ -z "$registry" ]]; then
  [[ $dry_run -eq 1 ]] || die "o remoto origin não é do GitHub: use --registry (ex.: ghcr.io/dono)"
  registry="ghcr.io/dono"
  log "aviso: o remoto origin não é do GitHub; no dry-run vale $registry (use --registry)"
fi
[[ "$registry" =~ ^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+$ ]] || die "registro inválido: $registry"

revision="$(git -C "$root" rev-parse --short=12 HEAD 2>/dev/null)" || die "sem git: a revisão vem do commit"
if [[ -n "$(git -C "$root" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
  [[ $dry_run -eq 1 ]] || die "há mudanças locais: publique a partir de um commit limpo ($revision)"
  log "aviso: há mudanças locais; uma publicação de verdade recusaria este checkout"
fi

IFS=',' read -r -a services <<<"$only"
for svc in "${services[@]}"; do
  case "$svc" in
    api|harness|web) [[ -f "$root/deploy/local/$svc.Dockerfile" ]] || die "falta deploy/local/$svc.Dockerfile" ;;
    *) die "imagem desconhecida: $svc (api, harness ou web)" ;;
  esac
done

run() {
  if [[ $dry_run -eq 1 ]]; then
    printf '(dry-run)'
    printf ' %q' "$@"
    printf '\n'
  else
    "$@"
  fi
}

if [[ $dry_run -eq 0 ]]; then
  command -v docker >/dev/null 2>&1 || die "comando necessário: docker"
  docker buildx version >/dev/null 2>&1 || die "o docker buildx é necessário (pacote docker-buildx ou o Docker Desktop)"
fi

# Multi-platform builds need a builder with the docker-container driver (the default docker driver
# builds only for this machine unless the containerd image store is on).
if [[ $dry_run -eq 1 ]]; then
  log "o builder $builder é criado se ainda não existir:"
  run docker buildx create --name "$builder" --driver docker-container
elif ! docker buildx inspect "$builder" >/dev/null 2>&1; then
  run docker buildx create --name "$builder" --driver docker-container
fi
run docker buildx inspect --bootstrap "$builder"
if [[ $dry_run -eq 0 ]]; then
  available="$(docker buildx inspect "$builder" | sed -n 's/^Platforms:[[:space:]]*//p')"
  IFS=',' read -r -a wanted <<<"$platforms"
  for p in "${wanted[@]}"; do
    [[ ",${available// /}," == *",$p,"* ]] || die "o builder $builder não constrói $p: docker run --privileged --rm tonistiigi/binfmt --install ${p#linux/}"
  done
fi

image_version="${version:-$revision}"
log "imagens $registry/docmatch-{${only}} para $platforms (revisão $revision, versão $image_version)"
[[ -n "$repo_url" ]] || log "aviso: sem o endereço do repositório (remoto fora do GitHub); o pacote não fica ligado a ele"
for svc in "${services[@]}"; do
  image="$registry/docmatch-$svc"
  tags=(-t "$image:$revision")
  [[ -n "$version" ]] && tags+=(-t "$image:$version")
  [[ $latest -eq 1 ]] && tags+=(-t "$image:latest")
  meta=(
    --label "org.opencontainers.image.revision=$revision"
    --label "org.opencontainers.image.version=$image_version"
    --annotation "index:org.opencontainers.image.revision=$revision"
    --annotation "index:org.opencontainers.image.version=$image_version"
    --annotation "index:org.opencontainers.image.licenses=AGPL-3.0-only"
  )
  if [[ -n "$repo_url" ]]; then
    meta+=(--label "org.opencontainers.image.source=$repo_url" --annotation "index:org.opencontainers.image.source=$repo_url")
  fi
  log "docmatch-$svc"
  run docker buildx build --builder "$builder" --platform "$platforms" \
    -f "$root/deploy/local/$svc.Dockerfile" \
    --build-arg "VERSION=$image_version" --build-arg "REVISION=$revision" \
    "${meta[@]}" "${tags[@]}" --push "$root"
done

if [[ $dry_run -eq 1 ]]; then
  log "dry-run: nada foi compilado nem enviado"
else
  log "pronto: $registry/docmatch-{${only}}:$revision${version:+ e :$version}"
  log "com o repositório privado os pacotes são privados; torne-os públicos quando o repositório abrir"
fi
